package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
)

func TestPersistedMessageAppliesProjectionByCommitReceipt(t *testing.T) {
	t.Parallel()
	t.Run("uncommitted error", func(t *testing.T) {
		store := mustCreateTestSession(t)
		var events []Event
		eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		})
		mustBlockTestEventLogAppends(t, store)

		err := eng.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("uncommitted")}}))
		if err == nil {
			t.Fatal("persisted message did not surface the event-log append failure")
		}
		if rows := mustTranscriptHydrationSnapshot(t, eng).CommittedRows; len(rows) != 0 {
			t.Fatalf("uncommitted message projected rows: %+v", rows)
		}
		if len(events) != 0 {
			t.Fatalf("uncommitted message published events: %+v", events)
		}
	})

	t.Run("committed observer error", func(t *testing.T) {
		observerErr := errors.New("message observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
		var events []Event
		eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		})
		gate.FailNext(observerErr)

		err := eng.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("committed")}}))
		if !errors.Is(err, observerErr) {
			t.Fatalf("persisted message error = %v, want observer error", err)
		}
		if rows := mustTranscriptHydrationSnapshot(t, eng).CommittedRows; len(rows) != 1 {
			t.Fatalf("committed message projected rows: %+v", rows)
		}
		if len(events) != 1 || events[0].Kind != EventConversationUpdated {
			t.Fatalf("committed message events: %+v", events)
		}
	})
}

func TestRuntimeSetterCallerCancellationStopsOnlyWait(t *testing.T) {
	tests := []struct {
		name    string
		apply   func(context.Context, *Engine) error
		applied func(*Engine) bool
	}{
		{
			name: "Session name",
			apply: func(ctx context.Context, engine *Engine) error {
				_, err := engine.SetSessionName(ctx, "renamed")
				return err
			},
			applied: func(engine *Engine) bool { return engine.SessionName() == "renamed" },
		},
		{
			name: "Thinking",
			apply: func(ctx context.Context, engine *Engine) error {
				return engine.SetThinkingLevel(ctx, "low")
			},
			applied: func(engine *Engine) bool { return engine.ThinkingLevel() == "low" },
		},
		{
			name: "Auto-compaction",
			apply: func(ctx context.Context, engine *Engine) error {
				_, _, err := engine.SetAutoCompactionEnabled(ctx, false)
				return err
			},
			applied: func(engine *Engine) bool { return !engine.AutoCompactionEnabled() },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
				Model:                   "gpt-5",
				ThinkingLevel:           "high",
				SupportedThinkingValues: []string{"low", "high"},
			})
			if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
				t.Fatalf("pause Runtime FIFO: %v", err)
			}

			caller, cancel := context.WithCancel(t.Context())
			result := make(chan error, 1)
			go func() {
				result <- test.apply(caller, engine)
			}()
			waitForPendingRuntimeOperation(t, engine)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled setter wait error = %v, want canceled", err)
				}
			case <-time.After(runtimeTestSynchronizationTimeout):
				t.Fatal("canceled setter caller remained blocked")
			}
			if test.applied(engine) {
				t.Fatal("setter applied before the paused Runtime FIFO drained")
			}

			if err := engine.drainRuntimeOperations(t.Context()); err != nil {
				t.Fatalf("drain accepted setter: %v", err)
			}
			if !test.applied(engine) {
				t.Fatal("accepted setter did not continue after caller cancellation")
			}
		})
	}
}

func waitForPendingRuntimeOperation(t *testing.T, engine *Engine) {
	t.Helper()
	deadline := time.After(runtimeTestSynchronizationTimeout)
	for !engine.HasPendingRuntimeOperations() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for accepted Runtime operation")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func mustTranscriptHydrationSnapshot(t *testing.T, engine *Engine) TranscriptHydrationSnapshot {
	t.Helper()
	var snapshot TranscriptHydrationSnapshot
	if err := engine.WithTranscriptHydrationSnapshot(func(value TranscriptHydrationSnapshot) error {
		snapshot = value
		return nil
	}); err != nil {
		t.Fatalf("read transcript hydration snapshot: %v", err)
	}
	return snapshot
}
