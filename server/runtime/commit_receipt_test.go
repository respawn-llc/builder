package runtime

import (
	"errors"
	"slices"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestPersistedWorkflowAssignmentFailureReporting(t *testing.T) {
	t.Run("preparation failure is returned directly", func(t *testing.T) {
		store := mustCreateTestSession(t)
		mustBlockTestEventLogAppends(t, store)

		_, err := PersistWorkflowAssignment(
			store,
			workflowAssignmentForCommitReceiptTest(),
			persistedWorkflowAssignmentContextForTest(t),
		)
		if err == nil {
			t.Fatal("PersistWorkflowAssignment did not return preparation failure")
		}
	})

	t.Run("uncommitted assignment append failure", func(t *testing.T) {
		store := mustCreateTestSession(t)
		seedPersistedWorkflowBaseContextForCommitReceiptTest(t, store)
		engine, err := newPersistedSteeringEngine(store)
		if err != nil {
			t.Fatalf("prepare persisted steering engine: %v", err)
		}
		mustBlockTestEventLogAppends(t, store)

		message, err := buildWorkflowAssignmentMessage(workflowAssignmentForCommitReceiptTest())
		if err != nil {
			t.Fatalf("build workflow assignment: %v", err)
		}
		receipt, waitErr := engine.steerWithCommitReceipt("", steerMessagesWithPersistenceIntent(steeringMessageEventDefault,
			true,
			[]llm.Message{message},
		))
		if waitErr == nil {
			t.Fatal("workflow assignment completion did not surface append failure")
		}
		if receipt.Committed {
			t.Fatalf("workflow assignment receipt = %+v, want uncommitted", receipt)
		}
	})

	t.Run("committed observer failure", func(t *testing.T) {
		observerErr := errors.New("workflow assignment observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
		seedPersistedWorkflowBaseContextForCommitReceiptTest(t, store)
		gate.FailNext(observerErr)

		receipt, waitErr := PersistWorkflowAssignment(
			store,
			workflowAssignmentForCommitReceiptTest(),
			persistedWorkflowAssignmentContextForTest(t),
		)
		if !errors.Is(waitErr, observerErr) {
			t.Fatalf("workflow assignment completion error = %v, want %v", waitErr, observerErr)
		}
		if !receipt.Committed {
			t.Fatalf("workflow assignment receipt = %+v, want committed", receipt)
		}
	})
}

func TestPersistedWorkflowAssignmentDoesNotRepairExistingSession(t *testing.T) {
	store := mustCreateTestSession(t)
	assignment := workflowAssignmentForCommitReceiptTest()
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		t.Fatalf("build workflow assignment: %v", err)
	}
	receipt, err := SteerPersistedMessage(store, "", message)
	if err != nil || !receipt.Committed {
		t.Fatalf("seed existing workflow assignment = %+v, %v; want committed", receipt, err)
	}

	receipt, err = PersistWorkflowAssignment(
		store,
		assignment,
		persistedWorkflowAssignmentContextForTest(t),
	)
	if err != nil {
		t.Fatalf("PersistWorkflowAssignment: %v", err)
	}
	if !receipt.Committed {
		t.Fatalf("workflow assignment = %+v, want committed", receipt)
	}

	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	recent, err := eventLog.ReadRecentRecords(10)
	if err != nil {
		t.Fatalf("read recent records: %v", err)
	}
	messageTypes := make([]session.MessageType, 0, len(recent.Records))
	for _, record := range recent.Records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("read event payload: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if ok && message.MessageType != nil {
			messageTypes = append(messageTypes, *message.MessageType)
		}
	}
	want := []session.MessageType{
		session.MessageTypeWorkflowMode,
		session.MessageTypeWorkflowMode,
	}
	if !slices.Equal(messageTypes, want) {
		t.Fatalf("existing Session message types = %v, want assignments only %v", messageTypes, want)
	}
}

func workflowAssignmentForCommitReceiptTest() WorkflowAssignment {
	reference := workflow.CurrentNodeReference{
		TaskID: "task-assignment-receipt",
		NodeID: "node-assignment-receipt",
	}
	return WorkflowAssignment{
		ContextMode:    workflow.ContextModeNewSession,
		CompletionMode: workflowruntime.CompletionModeTool,
		Prompt: workflowruntime.PromptContract{
			Identity:       workflowruntime.CurrentNodePromptIdentity(reference),
			CompletionMode: workflowruntime.CompletionModeTool,
			Instructions: workflowruntime.TaskInstructions{
				CurrentNode:      reference,
				WorkflowID:       runtimeids.NewWorkflowID(),
				TransitionPrompt: "Perform the assigned workflow step.",
			},
		},
	}
}

func persistedWorkflowAssignmentContextForTest(t *testing.T) PersistedWorkflowAssignmentContext {
	t.Helper()
	return PersistedWorkflowAssignmentContext{
		Workdir:         t.TempDir(),
		GlobalConfigDir: t.TempDir(),
		Model:           "gpt-5",
	}
}

func seedPersistedWorkflowBaseContextForCommitReceiptTest(t *testing.T, store *session.Store) {
	t.Helper()
	receipt, err := SteerPersistedMessage(store, "", llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeEnvironment),
		Content:     textutil.Value("test environment context"),
	})
	if err != nil || !receipt.Committed {
		t.Fatalf("seed persisted workflow base context = %+v, %v; want committed", receipt, err)
	}
}

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

		err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringMessageEventDefault,
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

		err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringMessageEventDefault,
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
	t.Parallel()
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
