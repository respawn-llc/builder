package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestLiveChatContextSnapshotUsesRuntimeFactsBehindPersistencePresenceGates(t *testing.T) {
	t.Parallel()

	autoCompaction := false
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeCompactionClient{caps: llm.ProviderCapabilities{
			ProviderID:               "openai-compatible",
			SupportsResponsesCompact: false,
		}},
		newTestToolRegistry(t),
		Config{
			Model:                 "gpt-5",
			ContextWindowTokens:   100_000,
			AutoCompactTokenLimit: 75_000,
			CompactionMode:        string(config.CompactionModeNative),
			AutoCompactionEnabled: &autoCompaction,
		},
	)
	engine.setLastUsage(llm.Usage{InputTokens: 64_000})
	engine.compactionRuntimeState().SetCount(7)
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
	engine.compactionRuntimeState().SetActive("compact-step", "manual", 8)

	engine.compactionRuntimeState().SetContextFacts(session.SessionContextFacts{})
	absent := engine.LiveChatContextSnapshot()
	if absent.Policy.ContextWindowTokens != 100_000 ||
		absent.Policy.AutomaticThresholdTokens != 75_000 ||
		absent.Policy.CompactionMode != serverapi.ChatContextCompactionModeLocal ||
		absent.UsedTokens != 64_000 ||
		absent.AutoCompactionEnabled ||
		absent.CompletedCompactionCount != 0 ||
		!absent.CompactionRunning ||
		absent.ManualCompactEligible {
		t.Fatalf("absent-gated live snapshot = %+v", absent)
	}
	if engine.CompactionMode() != "local" {
		t.Fatalf("runtime Compaction Mode = %q, want canonical local fallback", engine.CompactionMode())
	}

	presentCount := 0
	presentEligibility := false
	engine.compactionRuntimeState().SetContextFacts(session.SessionContextFacts{
		CompletedCompactionCount: &presentCount,
		ManualCompactEligible:    &presentEligibility,
	})
	present := engine.LiveChatContextSnapshot()
	if present.CompletedCompactionCount != 7 || !present.ManualCompactEligible {
		t.Fatalf("present-gated live snapshot = %+v, want runtime count/eligibility", present)
	}
}
