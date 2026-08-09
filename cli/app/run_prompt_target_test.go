package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/cli/app/internal/startupconfig"
	"core/shared/config"
	"core/shared/protocol"
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

func TestStartRunPromptClientTranslatesRootMismatchReason(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	closeServer := publishConfiguredRemoteForWorkspace(t, workspace, protocol.CapabilityFlags{
		AuthBootstrap: true,
		ProjectAttach: true,
		RunPrompt:     true,
	})
	defer closeServer()
	service, closeFn, err := startRunPromptClient(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		ConfigRoot:            t.TempDir(),
	})
	if !errors.Is(err, errRunServerRootMismatch) || err == errRunServerRootMismatch {
		t.Fatalf("error = %v, want root mismatch with diagnostic reason", err)
	}
	if service != nil || closeFn != nil {
		t.Fatal("root mismatch returned a client or close operation")
	}
}
