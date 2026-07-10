package main

import (
	"os"
	"path/filepath"
	"testing"

	"core/server/auth"
	"core/shared/config"
)

func TestWriteOutputUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatalf("seed output: %v", err)
	}
	if _, err := writeOutput(path, capturedRequest{SessionID: "session"}); err != nil {
		t.Fatalf("writeOutput: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("output permissions = %o, want 600", got)
	}
}

func TestResolveProviderCapabilitiesUsesRuntimeBaseURLResolution(t *testing.T) {
	caps, err := resolveProviderCapabilities(
		auth.EmptyState(),
		config.Settings{
			Model:            "gpt-5.5",
			ProviderOverride: "openai",
			OpenAIBaseURL:    "https://example.invalid/v1",
		},
		"",
	)
	if err != nil {
		t.Fatalf("resolveProviderCapabilities: %v", err)
	}
	if got, want := caps.ProviderID, "openai-compatible"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}
