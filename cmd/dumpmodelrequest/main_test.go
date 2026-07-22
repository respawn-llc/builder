package main

import (
	"encoding/json"
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
	lockedVerbosity := true
	locked := &session.LockedContract{ProviderContract: session.LockedProviderCapabilities{
		ProviderID:                "locked",
		SupportsResponsesAPI:      true,
		SupportsProviderVerbosity: &lockedVerbosity,
	}}
	for _, providerID := range []string{"openai", "openai-compatible", "chatgpt-codex"} {
		t.Run(providerID, func(t *testing.T) {
			caps, forced, err := resolveInspectionProviderCapabilities(auth.EmptyState(), config.Settings{OpenAIBaseURL: "https://example.invalid/v1"}, locked, providerID)
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

func TestResolveInspectionProviderCapabilitiesPrefersLockedContractOverChangedConfiguration(t *testing.T) {
	lockedVerbosity := true
	caps, forced, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{ProviderCapabilities: config.ProviderCapabilitiesOverride{
			ProviderID:                "configured",
			SupportsResponsesAPI:      true,
			SupportsProviderVerbosity: false,
		}},
		&session.LockedContract{ProviderContract: session.LockedProviderCapabilities{
			ProviderID:                "locked",
			SupportsResponsesAPI:      true,
			SupportsProviderVerbosity: &lockedVerbosity,
		}},
		"",
	)
	if err != nil {
		t.Fatalf("resolveInspectionProviderCapabilities: %v", err)
	}
	if forced {
		t.Fatal("locked contract unexpectedly forced an inspector override")
	}
	if got, want := caps.ProviderID, "locked"; got != want {
		t.Fatalf("provider id = %q, want %q", got, want)
	}
	if !caps.SupportsProviderVerbosity {
		t.Fatalf("locked provider verbosity support = false, want true")
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

func TestResumedInspectionWirePayloadUsesLockedVerbosityAcrossConfigChanges(t *testing.T) {
	lockedVerbosity := true
	caps, _, err := resolveInspectionProviderCapabilities(
		auth.EmptyState(),
		config.Settings{ProviderCapabilities: config.ProviderCapabilitiesOverride{
			ProviderID:                "configured",
			SupportsResponsesAPI:      true,
			SupportsProviderVerbosity: false,
		}},
		&session.LockedContract{ProviderContract: session.LockedProviderCapabilities{
			ProviderID:                "locked",
			SupportsResponsesAPI:      true,
			SupportsProviderVerbosity: &lockedVerbosity,
		}},
		"",
	)
	if err != nil {
		t.Fatalf("resolve inspection provider capabilities: %v", err)
	}
	wire, err := llm.MarshalOpenAIWirePayload(
		llm.OpenAIRequest{Model: "operator-alias", ToolChoiceMode: llm.ToolChoiceModeAutomatic},
		false,
		"high",
		llm.OpenAIAuthMode{},
		caps,
	)
	if err != nil {
		t.Fatalf("marshal wire payload: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(wire, &payload); err != nil {
		t.Fatalf("decode wire payload: %v", err)
	}
	text, ok := payload["text"].(map[string]any)
	if !ok {
		t.Fatalf("expected text config in wire payload, got %#v", payload)
	}
	if got := text["verbosity"]; got != "high" {
		t.Fatalf("text.verbosity = %#v, want high", got)
	}
}

func TestValidateOpenAIResponsesInspectionProviderRejectsUnsupportedProvider(t *testing.T) {
	err := validateOpenAIResponsesInspectionProvider(llm.ProviderCapabilities{ProviderID: "anthropic"})
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
}

func TestInspectionHeadlessModeIncludesPersistedHeadlessSessions(t *testing.T) {
	if !inspectionHeadlessMode(true, false) {
		t.Fatal("persisted headless session was not inspected as headless")
	}
	if !inspectionHeadlessMode(false, true) {
		t.Fatal("workflow session was not inspected as headless")
	}
	if inspectionHeadlessMode(false, false) {
		t.Fatal("interactive session was inspected as headless")
	}
}
