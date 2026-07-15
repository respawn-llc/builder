package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/prompts"
	"core/server/auth"
	"core/shared/config"
)

func TestBuildAuthSupportUsesDefaultIssuerAndEnvClientID(t *testing.T) {
	support, err := BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), func(key string) string {
		switch key {
		case "KENT_OAUTH_CLIENT_ID":
			return "client-test"
		case "KENT_OAUTH_ISSUER":
			return "https://attacker.example"
		default:
			return ""
		}
	}, func() time.Time {
		return time.Unix(123, 0)
	})
	if err != nil {
		t.Fatalf("build auth support: %v", err)
	}
	if got := support.OAuthOptions.Issuer; got != auth.DefaultOpenAIIssuer {
		t.Fatalf("oauth issuer = %q, want %q", got, auth.DefaultOpenAIIssuer)
	}
	if got := support.OAuthOptions.ClientID; got != "client-test" {
		t.Fatalf("oauth client id = %q", got)
	}
	if _, err := support.AuthManager.Load(context.Background()); err != nil {
		t.Fatalf("load auth manager state: %v", err)
	}
}

func TestBuildRuntimeSupportUsesConfigSettings(t *testing.T) {
	support, err := BuildRuntimeSupport(config.App{Settings: config.Settings{
		PriorityRequestMode: true,
		ShellOutputMaxChars: 321,
		BGShellsOutput:      config.BGShellsOutputVerbose,
		Shell: config.ShellSettings{
			PostprocessingMode: config.ShellPostprocessingModeBuiltin,
		},
	}})
	if err != nil {
		t.Fatalf("build runtime support: %v", err)
	}
	t.Cleanup(func() {
		_ = support.Background.Close()
	})
	if support.FastModeState == nil || !support.FastModeState.Enabled() {
		t.Fatal("expected runtime support to carry enabled fast mode state")
	}
	if support.Background == nil {
		t.Fatal("expected background manager")
	}
	if support.BackgroundRouter == nil {
		t.Fatal("expected background router")
	}
}

func TestBuildGeneratedSupportUsesSharedSyncPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	result, err := BuildGeneratedSupport(context.Background(), "")
	if err != nil {
		t.Fatalf("BuildGeneratedSupport: %v", err)
	}
	wantSkillsRoot := filepath.Join(home, config.ConfigDirName, ".generated", "skills")
	if result.GeneratedSkillsRoot != wantSkillsRoot {
		t.Fatalf("generated skills root = %q, want %q", result.GeneratedSkillsRoot, wantSkillsRoot)
	}
	if entries, err := os.ReadDir(wantSkillsRoot); err != nil {
		t.Fatalf("expected generated skills root to be seeded: %v", err)
	} else if len(entries) == 0 {
		t.Fatal("expected generated skills root to contain at least one skill")
	}
	if result.RecoveredWarning != "" {
		t.Fatalf("did not expect recovered warning on clean seed, got %+v", result)
	}
	if prompts.RecoveredWarning() == "" {
		t.Fatal("expected generated warning text to be available")
	}
}

func TestResolveConfigDoesNotCreateLegacyWorkspaceContainer(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	loaded, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	plan, err := ResolveConfig(Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("resolve config: %v", err)
	}
	if plan.Config.WorkspaceRoot == "" {
		t.Fatal("expected workspace root")
	}
	if plan.Config.Settings.Model == "" {
		t.Fatal("expected resolved config to carry a non-empty model")
	}
	if plan.Config.Settings.Model != loaded.Settings.Model {
		t.Fatalf("resolved config model = %q, want %q", plan.Config.Settings.Model, loaded.Settings.Model)
	}
	if plan.ContainerDir != "" {
		t.Fatalf("container dir = %q, want empty after legacy workspace containers removal", plan.ContainerDir)
	}
}
