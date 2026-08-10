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

		_, err := EnsurePersistedWorkflowAssignment(
			t.Context(),
			store,
			workflowAssignmentForCommitReceiptTest(),
			persistedWorkflowAssignmentContextForTest(t),
		)
		if err == nil {
			t.Fatal("EnsurePersistedWorkflowAssignment did not return preparation failure")
		}
	})

	t.Run("uncommitted assignment append failure", func(t *testing.T) {
		store := mustCreateTestSession(t)
		seedPersistedWorkflowBaseContextForCommitReceiptTest(t, store)
		engine, err := newPersistedSteeringEngine(store)
		if err != nil {
			t.Fatalf("prepare persisted steering engine: %v", err)
		}
		message, err := buildWorkflowAssignmentMessage(workflowAssignmentForCommitReceiptTest())
		if err != nil {
			t.Fatalf("build workflow assignment: %v", err)
		}
		mustBlockTestEventLogAppends(t, store)

		steer := completePersistedWorkflowAssignment(engine, message)
		receipt, waitErr := steer.Wait(t.Context())
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

		ensured, err := EnsurePersistedWorkflowAssignment(
			t.Context(),
			store,
			workflowAssignmentForCommitReceiptTest(),
			persistedWorkflowAssignmentContextForTest(t),
		)
		if !errors.Is(err, observerErr) {
			t.Fatalf("workflow assignment completion error = %v, want %v", err, observerErr)
		}
		if !ensured.Receipt.Committed {
			t.Fatalf("workflow assignment receipt = %+v, want committed", ensured.Receipt)
		}
	})
}

func TestEnsurePersistedWorkflowAssignmentAppendsAbsentIdentityAndReusesMatch(t *testing.T) {
	store := mustCreateTestSession(t)
	assignment := workflowAssignmentForCommitReceiptTest()

	first, err := EnsurePersistedWorkflowAssignment(
		t.Context(),
		store,
		assignment,
		persistedWorkflowAssignmentContextForTest(t),
	)
	if err != nil || !first.Receipt.Committed || !first.Appended {
		t.Fatalf("first assignment ensure = %+v, %v; want committed append", first, err)
	}
	second, err := EnsurePersistedWorkflowAssignment(
		t.Context(),
		store,
		assignment,
		persistedWorkflowAssignmentContextForTest(t),
	)
	if err != nil || !second.Receipt.Committed || second.Appended {
		t.Fatalf("matching assignment ensure = %+v, %v; want committed no-op", second, err)
	}
	if got := persistedWorkflowAssignmentSourcePaths(t, store); !slices.Equal(got, []string{assignment.Prompt.Identity}) {
		t.Fatalf("persisted assignment identities = %v, want one %q", got, assignment.Prompt.Identity)
	}
}

func TestEnsurePersistedWorkflowAssignmentAppendsDifferentIdentity(t *testing.T) {
	store := mustCreateTestSession(t)
	first := workflowAssignmentForCommitReceiptTest()
	if _, err := EnsurePersistedWorkflowAssignment(
		t.Context(),
		store,
		first,
		persistedWorkflowAssignmentContextForTest(t),
	); err != nil {
		t.Fatalf("ensure first assignment: %v", err)
	}
	second := workflowAssignmentForCommitReceiptTest()
	second.ContextMode = workflow.ContextModeContinueSession
	second.Prompt.Instructions.CurrentNode.NodeID = "node-assignment-next"
	second.Prompt.Identity = workflowruntime.CurrentNodePromptIdentity(second.Prompt.Instructions.CurrentNode)
	ensured, err := EnsurePersistedWorkflowAssignment(
		t.Context(),
		store,
		second,
		persistedWorkflowAssignmentContextForTest(t),
	)
	if err != nil || !ensured.Receipt.Committed || !ensured.Appended {
		t.Fatalf("different assignment ensure = %+v, %v; want committed append", ensured, err)
	}
	if got := persistedWorkflowAssignmentSourcePaths(t, store); !slices.Equal(got, []string{
		first.Prompt.Identity,
		second.Prompt.Identity,
	}) {
		t.Fatalf("persisted assignment identities = %v", got)
	}
}

func TestEnsurePersistedWorkflowAssignmentRestoresCompactedActiveIdentity(t *testing.T) {
	store := mustCreateTestSession(t)
	assignment := workflowAssignmentForCommitReceiptTest()
	message, err := buildWorkflowAssignmentMessage(assignment)
	if err != nil {
		t.Fatalf("build workflow assignment: %v", err)
	}
	_, receipt, err := appendTestCompactionHistoryReplacement(
		t,
		store,
		"compact",
		historyReplacementPayload{
			Engine: "local",
			Mode:   string(session.CompactionModeManual),
			Items:  llm.ItemsFromMessages([]llm.Message{message}),
		},
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("append compacted active assignment = %+v, %v", receipt, err)
	}
	ensured, err := EnsurePersistedWorkflowAssignment(
		t.Context(),
		store,
		assignment,
		persistedWorkflowAssignmentContextForTest(t),
	)
	if err != nil || !ensured.Receipt.Committed || ensured.Appended {
		t.Fatalf("compacted matching assignment ensure = %+v, %v; want committed no-op", ensured, err)
	}
}

func TestEnsureWorkflowAssignmentUsesLiveStructuredIdentity(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
	})
	first := workflowAssignmentForCommitReceiptTest()
	ensured, err := engine.EnsureWorkflowAssignment(t.Context(), first)
	if err != nil || !ensured.Receipt.Committed || !ensured.Appended {
		t.Fatalf("live first assignment ensure = %+v, %v; want committed append", ensured, err)
	}
	ensured, err = engine.EnsureWorkflowAssignment(t.Context(), first)
	if err != nil || !ensured.Receipt.Committed || ensured.Appended {
		t.Fatalf("live matching assignment ensure = %+v, %v; want committed no-op", ensured, err)
	}
	second := workflowAssignmentForCommitReceiptTest()
	second.ContextMode = workflow.ContextModeContinueSession
	second.Prompt.Instructions.CurrentNode.NodeID = "node-assignment-live-next"
	second.Prompt.Identity = workflowruntime.CurrentNodePromptIdentity(second.Prompt.Instructions.CurrentNode)
	ensured, err = engine.EnsureWorkflowAssignment(t.Context(), second)
	if err != nil || !ensured.Receipt.Committed || !ensured.Appended {
		t.Fatalf("live different assignment ensure = %+v, %v; want committed append", ensured, err)
	}
	if got := activeWorkflowAssignmentSourcePaths(engine.transcriptRuntimeState().SnapshotItems()); !slices.Equal(got, []string{
		first.Prompt.Identity,
		second.Prompt.Identity,
	}) {
		t.Fatalf("live assignment identities = %v", got)
	}
}

func activeWorkflowAssignmentSourcePaths(items []llm.ResponseItem) []string {
	paths := make([]string, 0, 2)
	for _, item := range items {
		if item.Type != llm.ResponseItemTypeMessage ||
			item.MessageType == nil ||
			*item.MessageType != llm.MessageTypeWorkflowMode ||
			item.SourcePath == nil {
			continue
		}
		paths = append(paths, *item.SourcePath)
	}
	return paths
}

func persistedWorkflowAssignmentSourcePaths(t *testing.T, store *session.Store) []string {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	window, err := eventLog.ReadNewestSegmentBackward(nil)
	if err != nil {
		t.Fatalf("read active transcript segment: %v", err)
	}
	paths := make([]string, 0, 2)
	for _, record := range window.Records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("read event payload: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if !ok ||
			message.MessageType == nil ||
			*message.MessageType != session.MessageTypeWorkflowMode ||
			message.SourcePath == nil {
			continue
		}
		paths = append(paths, *message.SourcePath)
	}
	return paths
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
