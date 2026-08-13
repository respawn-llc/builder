package runtime

import (
	"sync"
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
		&fakeClient{},
		newTestToolRegistry(t),
		Config{
			Model:                 "gpt-5",
			ContextWindowTokens:   100_000,
			AutoCompactTokenLimit: 75_000,
			CompactionMode:        string(config.CompactionModeLocal),
			AutoCompactionEnabled: &autoCompaction,
		},
	)
	engine.setLastUsage(llm.Usage{InputTokens: 64_000})
	engine.compactionRuntimeState().SetCount(7)
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
	engine.compactionRuntimeState().SetActive("compact-step", "manual", 8)

	engine.compactionRuntimeState().SetContextFacts(session.SessionContextFacts{})
	absent := engine.LiveChatContextSnapshot()
	if absent.Policy.CompactionMode != serverapi.ChatContextCompactionModeLocal ||
		absent.UsedTokens != 64_000 ||
		absent.AutoCompactionEnabled ||
		absent.CompletedCompactionCount != 0 ||
		!absent.CompactionRunning ||
		absent.ManualCompactEligible {
		t.Fatalf("absent-gated live snapshot = %+v", absent)
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

func TestLiveChatContextSnapshotCannotMixUsageAndCompactionTransitions(t *testing.T) {
	t.Parallel()

	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t),
		Config{Model: "gpt-5", CompactionMode: string(config.CompactionModeLocal)},
	)
	presentCount := 0
	presentEligibility := false
	engine.compactionRuntimeState().SetContextFacts(session.SessionContextFacts{
		CompletedCompactionCount: &presentCount,
		ManualCompactEligible:    &presentEligibility,
	})

	const transitions = 500
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for value := 1; value <= transitions; value++ {
			engine.contextSnapshotMu.Lock()
			engine.usageTrackingState().Apply(llm.Usage{InputTokens: value}, 0, 0)
			if value%2 == 1 {
				engine.compactionRuntimeState().SetActive("compact-step", "manual", value)
			} else {
				engine.compactionRuntimeState().ClearActive("compact-step")
			}
			engine.contextSnapshotMu.Unlock()
		}
	}()

	for range transitions {
		snapshot := engine.LiveChatContextSnapshot()
		if snapshot.CompactionRunning != (snapshot.UsedTokens%2 == 1) {
			t.Fatalf("mixed live Context snapshot = %+v", snapshot)
		}
	}
	wg.Wait()
}
