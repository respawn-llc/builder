package runtime

import (
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/textutil"
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

	before := engine.LiveChatContextSnapshot()
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- engine.steerOrdered(
			"compact-step",
			steerMessagesWithPersistenceIntent(
				steeringPriorityUser,
				steeringMessageEventDefault,
				true,
				[]llm.Message{{
					Role:    llm.RoleUser,
					Content: textutil.Value(strings.Repeat("context ", 20_000)),
				}},
			),
			steerCompactionActivityIntent(true, "manual", 1),
			steerEventIntent(Event{
				Kind:       EventCompactionStarted,
				StepID:     "compact-step",
				Compaction: &CompactionStatus{Mode: "manual", Count: 1},
			}),
		)
	}()

	for {
		snapshot := engine.LiveChatContextSnapshot()
		if snapshot.UsedTokens != before.UsedTokens && !snapshot.CompactionRunning {
			t.Fatalf("mixed live Context snapshot = %+v", snapshot)
		}
		select {
		case err := <-transitionDone:
			if err != nil {
				t.Fatalf("apply production Context transition: %v", err)
			}
			after := engine.LiveChatContextSnapshot()
			if after.UsedTokens <= before.UsedTokens || !after.CompactionRunning {
				t.Fatalf("completed live Context transition = %+v, before %+v", after, before)
			}
			return
		default:
		}
	}
}
