package runtime

import (
	"slices"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

func TestPersistedWorkflowAssignmentFailureReporting(t *testing.T) {
	t.Run("preparation failure is returned directly", func(t *testing.T) {
		store := mustCreateTestSession(t)
		mustBlockTestEventLogAppends(t, store)

		_, err := SteerPersistedWorkflowAssignment(
			store,
			workflowAssignmentForCommitReceiptTest(),
			persistedWorkflowAssignmentContextForTest(t),
		)
		if err == nil {
			t.Fatal("SteerPersistedWorkflowAssignment did not return preparation failure")
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

	steer, err := SteerPersistedWorkflowAssignment(
		store,
		assignment,
		persistedWorkflowAssignmentContextForTest(t),
	)
	if err != nil {
		t.Fatalf("SteerPersistedWorkflowAssignment: %v", err)
	}
	if receipt, err := steer.Wait(t.Context()); err != nil || !receipt.Committed {
		t.Fatalf("wait for workflow assignment = %+v, %v; want committed", receipt, err)
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

func TestUncommittedPersistedMessageDoesNotApplyProjection(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
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
