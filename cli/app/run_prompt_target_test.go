package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/cli/app/internal/startupconfig"
	"core/shared/config"
)

func TestStartRunPromptClientMissingWorkspaceContextSessionFailsBeforeAttach(t *testing.T) {
	home := newAppTestHome(t)
	workspace := t.TempDir()
	configPath := filepath.Join(home, config.ConfigDirName, "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, _, err := startRunPromptClient(context.Background(), Options{
		WorkspaceRoot:             workspace,
		WorkspaceRootExplicit:     true,
		WorkspaceContextSessionID: "session-from-env",
		AgentRole:                 sessionLifecycleStringPtr("missing"),
	})
	if !errors.Is(err, startupconfig.ErrWorkspaceContextSessionMissing) {
		t.Fatalf("error = %v, want missing workspace context session", err)
	}
}

func TestStartupConfigRequestThreadsPersistenceRoot(t *testing.T) {
	req := startupConfigRequest(Options{ConfigRoot: "/tmp/iso-root"})
	if req.LoadOptions.ConfigRoot != "/tmp/iso-root" {
		t.Fatalf("LoadOptions.ConfigRoot = %q, want /tmp/iso-root", req.LoadOptions.ConfigRoot)
	}
}
