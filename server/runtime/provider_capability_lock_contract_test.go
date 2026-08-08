package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/runtimecommand"
	"core/server/session"
	"core/server/tools"
)

func TestNewUsesPersistedProviderContractWhenLiveCapabilitiesUnavailable(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	persisted := llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:            "gpt-5.3-codex",
		ProviderContract: llm.LockedProviderCapabilitiesFromContract(persisted),
	}); err != nil {
		t.Fatalf("persist provider contract: %v", err)
	}

	client := &fakeClient{
		capsErr: errors.New("transient provider capability failure"),
	}
	engine, err := New(
		store,
		mustMaterializeTestEventLog(t, store),
		client,
		tools.NewRegistry(),
		Config{
			Model:         "gpt-5.3-codex",
			RuntimeEvents: runtimecommand.NewQueue(context.Background()),
		},
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	if !engine.FastModeAvailable() {
		t.Fatal("persisted supported provider was reported unavailable")
	}
}
