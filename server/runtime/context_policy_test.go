package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/serverapi"
)

func TestEngineContextPolicySnapshotIsCohesiveAndCanonical(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeCompactionClient{
		caps: llm.ProviderCapabilities{
			ProviderID:               "openai-compatible",
			SupportsResponsesCompact: false,
		},
	}, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   200_000,
		AutoCompactTokenLimit: 180_000,
		CompactionMode:        "native",
	})

	policy := engine.ContextPolicySnapshot()
	if policy.ContextWindowTokens != 200_000 ||
		policy.AutomaticThresholdTokens != 180_000 ||
		policy.CompactionMode != serverapi.ChatContextCompactionModeLocal {
		t.Fatalf("ContextPolicySnapshot() = %+v", policy)
	}
	if engine.CompactionMode() != "local" {
		t.Fatalf("CompactionMode() = %q, want local canonical fallback", engine.CompactionMode())
	}
}
