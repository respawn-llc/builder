package chatcontext

import (
	"os"
	"path/filepath"
	"testing"

	"core/shared/config"
)

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
