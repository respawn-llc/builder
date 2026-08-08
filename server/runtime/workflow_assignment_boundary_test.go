package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/textutil"
)

func TestWorkflowAssignmentAppliesAtAgentStepBoundary(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := &hookClient{
		response: llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("source complete"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		beforeReturn: func() error {
			close(started)
			<-release
			return nil
		},
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)

	runDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "finish source")
		runDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("source Agent Step did not start")
	}

	steer, err := engine.SteerWorkflowAssignment(workflowAssignmentForCommitReceiptTest())
	if err != nil {
		t.Fatalf("SteerWorkflowAssignment: %v", err)
	}
	if pending := pendingWorkflowAssignmentsForTest(engine.boundaryAgenda); len(pending) != 1 {
		t.Fatalf("pending Workflow assignments = %d, want one canonical item", len(pending))
	}
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelWait()
	if receipt, waitErr := steer.Wait(waitCtx); !errors.Is(waitErr, context.DeadlineExceeded) || receipt.Committed {
		t.Fatalf("pre-Boundary Workflow assignment = %+v, %v; want pending", receipt, waitErr)
	}

	close(release)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("source Agent Turn: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("source Agent Turn did not finish after Boundary release")
	}
	receipt, err := steer.Wait(context.Background())
	if err != nil || !receipt.Committed {
		t.Fatalf("Workflow assignment settlement = %+v, %v; want committed", receipt, err)
	}
	if pending := pendingWorkflowAssignmentsForTest(engine.boundaryAgenda); len(pending) != 0 {
		t.Fatalf("pending Workflow assignments after Boundary = %d, want zero", len(pending))
	}
	assertWorkflowAssignmentRecordCount(t, engine, 1)
}

func TestWorkflowAssignmentAppliesImmediatelyOnIdleRuntime(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)

	steer, err := engine.SteerWorkflowAssignment(workflowAssignmentForCommitReceiptTest())
	if err != nil {
		t.Fatalf("SteerWorkflowAssignment: %v", err)
	}
	receipt, err := steer.Wait(context.Background())
	if err != nil || !receipt.Committed {
		t.Fatalf("idle Workflow assignment settlement = %+v, %v; want committed", receipt, err)
	}
	if pending := pendingWorkflowAssignmentsForTest(engine.boundaryAgenda); len(pending) != 0 {
		t.Fatalf("idle pending Workflow assignments = %d, want zero", len(pending))
	}
	assertWorkflowAssignmentRecordCount(t, engine, 1)
}

func TestWorkflowAssignmentApplicationFailureSettlesTypedSteer(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	mustBlockTestEventLogAppends(t, store)

	steer, err := engine.SteerWorkflowAssignment(workflowAssignmentForCommitReceiptTest())
	if err != nil {
		t.Fatalf("accepted Workflow assignment returned application failure directly: %v", err)
	}
	receipt, err := steer.Wait(context.Background())
	if err == nil || receipt.Committed {
		t.Fatalf("failed Workflow assignment settlement = %+v, %v; want typed uncommitted failure", receipt, err)
	}
	if pending := pendingWorkflowAssignmentsForTest(engine.boundaryAgenda); len(pending) != 0 {
		t.Fatalf("failed Workflow assignment remained pending: %+v", pending)
	}
}

func TestWorkflowAssignmentSettlesWhenEarlierIdleWorkTransferFails(t *testing.T) {
	launcher := newBackgroundExecutionLauncher()
	launcher.active = true
	launcher.scopeID = runtimeids.NewExecutionScopeID()
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5", StepLifecycle: launcher},
	)
	launcher.engine = engine
	background, err := newBackgroundNoticeAgendaItem(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("earlier technical notice"),
	})
	if err != nil {
		t.Fatalf("new background item: %v", err)
	}
	if err := engine.boundaryAgenda.accept(background); err != nil {
		t.Fatalf("accept background item: %v", err)
	}
	message, err := buildWorkflowAssignmentMessage(workflowAssignmentForCommitReceiptTest())
	if err != nil {
		t.Fatalf("build Workflow assignment: %v", err)
	}
	steer := newWorkflowAssignmentSteer()
	assignment := newWorkflowAssignmentAgendaItem(
		steerMessagesWithPersistenceIntent(
			steeringPriorityRuntimeContext,
			steeringMessageEventDefault,
			true,
			[]llm.Message{message},
		),
		steer,
	)

	accepted, err := submitRuntimeEvent(
		engine,
		assignment,
		func(
			admission runtimeEventAdmission,
			item *workflowAssignmentAgendaItem,
		) (WorkflowAssignmentSteer, error) {
			if err := admission.startWork(func(context.Context) {}); err != nil {
				return WorkflowAssignmentSteer{}, err
			}
			return engine.acceptWorkflowAssignmentAgendaItem(admission, item)
		},
	)
	if err != nil {
		t.Fatalf("accept Workflow assignment: %v", err)
	}
	receipt, err := accepted.Wait(context.Background())
	if err == nil || receipt.Committed {
		t.Fatalf("Workflow assignment settlement = %+v, %v; want typed transfer failure", receipt, err)
	}
	if pending := pendingWorkflowAssignmentsForTest(engine.boundaryAgenda); len(pending) != 0 {
		t.Fatalf("failed Workflow assignment remained pending: %+v", pending)
	}
	if engine.longBoundary.selected != nil || !background.settled {
		t.Fatalf(
			"earlier long work ownership = selected:%v settled:%t",
			engine.longBoundary.selected,
			background.settled,
		)
	}
}

func TestWorkflowAssignmentPreservesAdmissionOrderAgainstHumanInput(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	client := &hookClient{
		response: llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("source complete"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		beforeReturn: func() error {
			close(started)
			<-release
			return nil
		},
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)

	runDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "source input")
		runDone <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("source Agent Step did not start")
	}

	steer, err := engine.SteerWorkflowAssignment(workflowAssignmentForCommitReceiptTest())
	if err != nil {
		t.Fatalf("SteerWorkflowAssignment: %v", err)
	}
	queued, accepted, err := engine.QueueUserMessageForActiveRun(
		context.Background(),
		"later human input",
		runtimeids.NewRuntimeClientRequestID(),
		nil,
	)
	if err != nil || !accepted {
		t.Fatalf("queue later human input = %+v, accepted=%t, err=%v", queued, accepted, err)
	}

	close(release)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("source Agent Turn: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("source Agent Turn did not finish")
	}
	if receipt, err := steer.Wait(context.Background()); err != nil || !receipt.Committed {
		t.Fatalf("Workflow assignment settlement = %+v, %v", receipt, err)
	}
	assertWorkflowAssignmentRecordCount(t, engine, 1)
	assertUserMessageRecordCount(t, engine, "later human input", 0)
}

func assertWorkflowAssignmentRecordCount(t *testing.T, engine *Engine, want int) {
	t.Helper()
	recent, err := engine.eventLog.ReadRecentRecords(100)
	if err != nil {
		t.Fatalf("read workflow assignment records: %v", err)
	}
	got := 0
	for _, record := range recent.Records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("read workflow assignment payload: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if ok && message.MessageType != nil && *message.MessageType == session.MessageTypeWorkflowMode {
			got++
		}
	}
	if got != want {
		t.Fatalf("Workflow assignment records = %d, want %d", got, want)
	}
}

func assertUserMessageRecordCount(t *testing.T, engine *Engine, text string, want int) {
	t.Helper()
	recent, err := engine.eventLog.ReadRecentRecords(100)
	if err != nil {
		t.Fatalf("read user message records: %v", err)
	}
	got := 0
	for _, record := range recent.Records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("read user message payload: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if ok && message.Role == session.MessageRole(llm.RoleUser) && message.Content != nil && *message.Content == text {
			got++
		}
	}
	if got != want {
		t.Fatalf("user message %q records = %d, want %d", text, got, want)
	}
}
