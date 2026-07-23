package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

func TestEnsureLockedLeavesProviderContractAbsentAfterTransientCapabilityFailure(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{
		capsErr: errors.New("transient provider capability failure"),
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("response"),
			},
			Usage: llm.Usage{WindowTokens: 200_000},
		}},
	}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5.3-codex"},
	)

	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit user turn: %v", err)
	}
	locked := store.Meta().Locked
	if locked == nil {
		t.Fatal("model dispatch did not create a lock")
	}
	if _, present := llm.ProviderCapabilitiesFromLocked(locked); present {
		t.Fatalf("transient capability failure persisted provider contract: %+v", locked.ProviderContract)
	}

	recovered := llm.ProviderCapabilities{
		ProviderID:              "recovered-provider",
		SupportsNativeWebSearch: true,
	}
	client.mu.Lock()
	client.capsErr = nil
	client.caps = recovered
	client.mu.Unlock()

	live, err := engine.providerCapabilities(context.Background())
	if err != nil {
		t.Fatalf("resolve recovered provider capabilities: %v", err)
	}
	if live.SupportsResponsesAPI ||
		live.SupportsResponsesCompact ||
		!live.SupportsNativeWebSearch ||
		live.IsOpenAIFirstParty {
		t.Fatalf("recovered live provider capabilities = %+v", live)
	}
	if _, present := llm.ProviderCapabilitiesFromLocked(store.Meta().Locked); present {
		t.Fatalf("live recovery persisted provider contract: %+v", store.Meta().Locked.ProviderContract)
	}
}
