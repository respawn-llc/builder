package main

import (
	"os"
	"path/filepath"
	"testing"

	"core/server/auth"
	"core/server/llm"
	"core/server/session"
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

func TestResolveInspectionProviderCapabilitiesUsesRuntimeBaseURLResolution(t *testing.T) {
	caps, forced, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{
			Model:            "gpt-5.5",
			ProviderOverride: "openai",
			OpenAIBaseURL:    "https://example.invalid/v1",
		},
		nil,
		"",
	)
	if err != nil {
		t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
	}
	if forced {
		t.Fatal("runtime provider resolution unexpectedly forced a contract")
	}
	if got, want := caps.ProviderID, "openai-compatible"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}

func TestResolveInspectionProviderCapabilitiesAcceptsProviderContractOverrides(t *testing.T) {
	for _, providerID := range []string{"openai", "openai-compatible", "chatgpt-codex"} {
		t.Run(providerID, func(t *testing.T) {
			caps, forced, err := resolveInspectionProviderCapabilities(auth.EmptyState(), config.Settings{OpenAIBaseURL: "https://example.invalid/v1"}, nil, providerID)
			if err != nil {
				t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
			}
			if !forced {
				t.Fatal("provider override did not force its capability contract")
			}
			if got := caps.ProviderID; got != providerID {
				t.Fatalf("provider id = %q, want %q", got, providerID)
			}
		})
	}
}

func TestResolveInspectionProviderCapabilitiesPrefersConfiguredContractOverLockedContract(t *testing.T) {
	caps, forced, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{ProviderCapabilities: config.ProviderCapabilitiesOverride{ProviderID: "configured", SupportsResponsesAPI: true}},
		&session.LockedContract{ProviderContract: session.LockedProviderCapabilities{ProviderID: "locked"}},
		"",
	)
	if err != nil {
		t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
	}
	if forced {
		t.Fatal("configured contract unexpectedly forced an inspector override")
	}
	if got, want := caps.ProviderID, "configured"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}

func TestResolveInspectionProviderCapabilitiesUsesLockedContract(t *testing.T) {
	caps, forced, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{OpenAIBaseURL: "https://api.openai.com/v1"},
		&session.LockedContract{ProviderContract: session.LockedProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}},
		"",
	)
	if err != nil {
		t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
	}
	if forced {
		t.Fatal("locked contract unexpectedly forced an inspector override")
	}
	if got, want := caps.ProviderID, "openai-compatible"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}

func TestValidateOpenAIResponsesInspectionProviderRejectsUnsupportedProvider(t *testing.T) {
	err := validateOpenAIResponsesInspectionProvider(llm.ProviderCapabilities{ProviderID: "anthropic"})
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestResolveOpenAIWirePayloadCapabilitiesUsesLiveBaseURLContract(t *testing.T) {
	caps, err := resolveOpenAIWirePayloadCapabilities(
		llm.OpenAIAuthMode{},
		config.Settings{
			ProviderOverride: "openai",
			OpenAIBaseURL:    "https://example.invalid/v1",
		},
		"",
	)
	if err != nil {
		t.Fatalf("resolveOpenAIWirePayloadCapabilities: %v", err)
	}
	if got, want := caps.ProviderID, "openai-compatible"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}

func TestResolveOpenAIWirePayloadCapabilitiesHonorsExplicitProviderOverride(t *testing.T) {
	caps, err := resolveOpenAIWirePayloadCapabilities(
		llm.OpenAIAuthMode{},
		config.Settings{OpenAIBaseURL: "https://example.invalid/v1"},
		"openai",
	)
	if err != nil {
		t.Fatalf("resolveOpenAIWirePayloadCapabilities: %v", err)
	}
	if got, want := caps.ProviderID, "openai"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
}
