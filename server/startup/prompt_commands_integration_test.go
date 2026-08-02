package startup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestEmbeddedRemotePromptCommandCatalogUsesServerWorkspace(t *testing.T) {
	workspace := newServeWorkspace(t)
	promptRoot := filepath.Join(workspace, ".kent", "prompts")
	if err := os.MkdirAll(promptRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptRoot, "remote_demo.md"), []byte("remote body"), 0o600); err != nil {
		t.Fatal(err)
	}

	server, err := StartWithOptions(
		context.Background(),
		Request{WorkspaceRoot: workspace, WorkspaceRootExplicit: true},
		envAuthHandler{},
		noopOnboarding,
		Options{},
	)
	if err != nil {
		t.Fatalf("StartWithOptions: %v", err)
	}
	releaseServeTestPortForConfig(server.Config())
	if err := server.ServeBackground(); err != nil {
		_ = server.Close()
		t.Fatalf("ServeBackground: %v", err)
	}
	defer func() { _ = server.Close() }()

	cfg := server.Config()
	var remote *client.Remote
	if !testsetup.Until(time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		remote, err = client.DialRemoteURLForProjectWorkspace(
			context.Background(),
			config.ServerRPCURL(cfg),
			server.ProjectID(),
			workspace,
		)
		return err == nil
	}) {
		t.Fatalf("DialRemoteURLForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()

	catalog, err := remote.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
	if err != nil {
		t.Fatalf("GetPromptCommandCatalog: %v", err)
	}
	found := false
	for _, entry := range catalog.Commands {
		if entry.Name == "prompt:remote_demo" && entry.Preview == "remote body" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("catalog = %+v, missing server workspace command", catalog.Commands)
	}
}
