package runtime

import (
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestPersistedMessageAppliesProjectionByCommitReceipt(t *testing.T) {
	t.Run("uncommitted error", func(t *testing.T) {
		store := mustCreateTestSession(t)
		var events []Event
		eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		})
		mustBlockTestEventLogAppends(t, store)

		err := eng.steer("step-1", steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("uncommitted")}},
		))
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

		err := eng.steer("step-1", steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("committed")}},
		))
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

func TestCommittedControlFeedbackAppliesStateByCommitReceipt(t *testing.T) {
	type controlCase struct {
		name      string
		newEngine func(*testing.T, *session.Store) *Engine
		apply     func(*Engine) (bool, session.CommitReceipt, error)
		isApplied func(*Engine) bool
	}
	cases := []controlCase{
		{
			name: "fast mode",
			newEngine: func(t *testing.T, store *session.Store) *Engine {
				return mustNewExecTestEngine(t, store, &fakeClient{caps: llm.ProviderCapabilities{
					ProviderID:           "openai",
					SupportsResponsesAPI: true,
					IsOpenAIFirstParty:   true,
				}}, Config{Model: "gpt-5.3-codex"})
			},
			apply: func(engine *Engine) (bool, session.CommitReceipt, error) {
				return engine.SetFastModeEnabledWithCommittedFeedback(true, func(bool) string {
					return "feedback"
				})
			},
			isApplied: func(engine *Engine) bool { return engine.FastModeEnabled() },
		},
		{
			name: "questions",
			newEngine: func(t *testing.T, store *session.Store) *Engine {
				return mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
			},
			apply: func(engine *Engine) (bool, session.CommitReceipt, error) {
				changed, _, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedback(false, func(bool, bool) string {
					return "feedback"
				})
				return changed, receipt, err
			},
			isApplied: func(engine *Engine) bool { return !engine.QuestionsEnabled() },
		},
		{
			name: "reviewer",
			newEngine: func(t *testing.T, store *session.Store) *Engine {
				return mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{
					ID:      toolspec.ToolExecCommand,
					Handler: fakeTool{name: toolspec.ToolExecCommand},
				}), Config{
					Model: "gpt-5",
					Reviewer: ReviewerConfig{
						Frequency:     "off",
						Model:         "gpt-5",
						ThinkingLevel: "low",
						Client:        &fakeClient{},
					},
				})
			},
			apply: func(engine *Engine) (bool, session.CommitReceipt, error) {
				changed, _, receipt, err := engine.SetReviewerEnabledWithCommittedFeedback(true, func(bool, string, bool) string {
					return "feedback"
				})
				return changed, receipt, err
			},
			isApplied: func(engine *Engine) bool { return engine.ReviewerFrequency() == "edits" },
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observerErr := errors.New("control feedback observer failed")
			gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
			store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
			engine := testCase.newEngine(t, store)
			gate.FailNext(observerErr)

			changed, receipt, err := testCase.apply(engine)
			if !errors.Is(err, observerErr) {
				t.Fatalf("control error = %v, want observer error", err)
			}
			if !receipt.Committed || !changed || !testCase.isApplied(engine) {
				t.Fatalf("committed control feedback did not apply state: receipt=%+v changed=%v", receipt, changed)
			}
			if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 1 {
				t.Fatalf("committed control feedback projected rows: %+v", rows)
			}
		})
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
