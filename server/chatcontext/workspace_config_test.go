package chatcontext

import (
	"os"
	"path/filepath"
	"testing"

	"core/shared/config"
)

func TestFixedRootWorkspaceResolverLoadsEveryExactWorkspaceFromStartupRoot(t *testing.T) {
	configRoot := t.TempDir()
	primary := t.TempDir()
	secondary := t.TempDir()
	t.Setenv(config.PersistenceRootEnvName, t.TempDir())
	if err := os.WriteFile(
		filepath.Join(configRoot, "config.toml"),
		[]byte("model_context_window = 120000\ncontext_compaction_threshold_tokens = 90000\npre_submit_compaction_lead_tokens = 20000\n"),
		0o600,
	); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(secondary, ".kent"), 0o755); err != nil {
		t.Fatalf("create secondary config dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(secondary, ".kent", "config.toml"),
		[]byte("model_context_window = 80000\ncontext_compaction_threshold_tokens = 60000\npre_submit_compaction_lead_tokens = 10000\n"),
		0o600,
	); err != nil {
		t.Fatalf("write secondary config: %v", err)
	}

	resolver := NewFixedRootWorkspaceResolver(configRoot, primary, config.LoadOptions{})
	primaryConfig, err := resolver.Resolve(primary)
	if err != nil {
		t.Fatalf("resolve primary: %v", err)
	}
	secondaryConfig, err := resolver.Resolve(secondary)
	if err != nil {
		t.Fatalf("resolve secondary: %v", err)
	}

	if primaryConfig.PersistenceRoot != configRoot || secondaryConfig.PersistenceRoot != configRoot {
		t.Fatalf(
			"persistence roots = %q/%q, want fixed startup root %q",
			primaryConfig.PersistenceRoot,
			secondaryConfig.PersistenceRoot,
			configRoot,
		)
	}
	if primaryConfig.WorkspaceRoot != primary {
		t.Fatalf("primary WorkspaceRoot = %q, want exact root %q", primaryConfig.WorkspaceRoot, primary)
	}
	if secondaryConfig.WorkspaceRoot != secondary {
		t.Fatalf("secondary WorkspaceRoot = %q, want exact root %q", secondaryConfig.WorkspaceRoot, secondary)
	}
	if primaryConfig.Settings.ModelContextWindow != 120_000 {
		t.Fatalf("primary context window = %d, want 120000", primaryConfig.Settings.ModelContextWindow)
	}
	if secondaryConfig.Settings.ModelContextWindow != 80_000 {
		t.Fatalf("secondary context window = %d, want workspace override 80000", secondaryConfig.Settings.ModelContextWindow)
	}
}

func TestFixedRootWorkspaceResolverRetainsStartupOverridesAcrossFreshLoads(t *testing.T) {
	configRoot := t.TempDir()
	workspace := t.TempDir()
	secondary := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(configRoot, "config.toml"),
		[]byte("model = \"file-model\"\nopenai_base_url = \"https://file.example/v1\"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	resolver := NewFixedRootWorkspaceResolver(configRoot, workspace, config.LoadOptions{
		Model:         "cli-model",
		OpenAIBaseURL: "https://cli.example/v1",
	})

	if err := os.MkdirAll(filepath.Join(workspace, ".kent"), 0o755); err != nil {
		t.Fatalf("create workspace config dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(workspace, ".kent", "config.toml"),
		[]byte("model_context_window = 80000\ncontext_compaction_threshold_tokens = 60000\npre_submit_compaction_lead_tokens = 10000\n"),
		0o600,
	); err != nil {
		t.Fatalf("write workspace config: %v", err)
	}

	resolved, err := resolver.Resolve(workspace)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Settings.Model != "cli-model" || resolved.Settings.OpenAIBaseURL != "https://cli.example/v1" {
		t.Fatalf("startup overrides were not retained: %+v", resolved.Settings)
	}
	if resolved.Settings.ModelContextWindow != 80_000 {
		t.Fatalf("fresh workspace context window = %d, want 80000", resolved.Settings.ModelContextWindow)
	}
	secondaryResolved, err := resolver.Resolve(secondary)
	if err != nil {
		t.Fatalf("Resolve secondary: %v", err)
	}
	if secondaryResolved.Settings.Model != "file-model" ||
		secondaryResolved.Settings.OpenAIBaseURL != "https://file.example/v1" {
		t.Fatalf("startup overrides leaked into secondary workspace: %+v", secondaryResolved.Settings)
	}
}

func TestFixedRootWorkspaceResolverReportsLoadFailure(t *testing.T) {
	configRoot := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "config.toml"), []byte("invalid = ["), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	if _, err := NewFixedRootWorkspaceResolver(configRoot, workspace, config.LoadOptions{}).Resolve(workspace); err == nil {
		t.Fatal("Resolve succeeded with invalid fixed-root config")
	}
}
