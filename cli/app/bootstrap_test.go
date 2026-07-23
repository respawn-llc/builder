package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"core/server/auth"
	"core/server/metadata"
)

func TestBootstrapAppIgnoresOAuthIssuerOverrideEnv(t *testing.T) {
	t.Setenv("KENT_OAUTH_ISSUER", "https://attacker.example")
	t.Setenv("KENT_OAUTH_CLIENT_ID", "client-test")
	_, workspace := newRegisteredAppWorkspace(t)

	readyAuth := readyMemoryAuthHandler()
	readyAuth.lookupEnv = os.Getenv
	boot, err := startEmbeddedServer(context.Background(), Options{WorkspaceRoot: workspace}, readyAuth, false)
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	defer func() { _ = boot.Close() }()
	if got := boot.OAuthOptions().Issuer; got != auth.DefaultOpenAIIssuer {
		t.Fatalf("oauth issuer = %q, want %q", got, auth.DefaultOpenAIIssuer)
	}
	if got := boot.OAuthOptions().ClientID; got != "client-test" {
		t.Fatalf("oauth client id = %q", got)
	}
	binding, err := metadata.ResolveBinding(context.Background(), boot.Config().PersistenceRoot, boot.Config().WorkspaceRoot)
	if err != nil {
		t.Fatalf("resolve bootstrap binding: %v", err)
	}
	containerDir := filepath.Join(filepath.Join(boot.Config().PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	if _, err := os.Stat(containerDir); err != nil {
		t.Fatalf("expected bootstrap container dir to exist: %v", err)
	}
}
