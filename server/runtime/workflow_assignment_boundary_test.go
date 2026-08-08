package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/serverapi"
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
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
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
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelWait()
	if receipt, waitErr := steer.Wait(waitCtx); !errors.Is(waitErr, context.DeadlineExceeded) || receipt.Committed {
		t.Fatalf("pre-Boundary Workflow assignment = %+v, %v; want pending", receipt, waitErr)
	}
	assertWorkflowAssignmentRecordCount(t, store, 0)

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
	assertWorkflowAssignmentRecordCount(t, store, 1)
}

func TestWorkflowAssignmentAppliesImmediatelyOnIdleRuntime(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
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
	assertWorkflowAssignmentRecordCount(t, store, 1)
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
	if engine.longBoundary.selected != nil || !background.settled {
		t.Fatalf(
			"earlier long work ownership = selected:%v settled:%t",
			engine.longBoundary.selected,
			background.settled,
		)
	}
}

func TestWorkflowAssignmentSurvivesSourceScopeRelease(t *testing.T) {
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
	scopeID := runtimeids.NewExecutionScopeID()
	lifecycle := &workflowAssignmentScopeLifecycle{scopeID: scopeID}
	lifecycle.live.Store(true)
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5", StepLifecycle: lifecycle},
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
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if receipt, waitErr := steer.Wait(waitCtx); !errors.Is(waitErr, context.DeadlineExceeded) || receipt.Committed {
		cancelWait()
		t.Fatalf("pre-release Workflow assignment = %+v, %v; want pending", receipt, waitErr)
	}
	cancelWait()

	lifecycle.live.Store(false)
	if err := engine.AgentExecutionScopeReleased(scopeID); err != nil {
		t.Fatalf("release source Exact Execution Scope: %v", err)
	}
	if receipt, err := steer.Wait(context.Background()); err != nil || !receipt.Committed {
		t.Fatalf("post-release Workflow assignment settlement = %+v, %v; want committed", receipt, err)
	}
	assertWorkflowAssignmentRecordCount(t, store, 1)

	close(release)
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("source Agent Turn: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("source Agent Turn did not finish")
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
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
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
	assertWorkflowAssignmentRecordCount(t, store, 1)
	assertUserMessageRecordCount(t, store, "later human input", 0)
}

func assertWorkflowAssignmentRecordCount(t *testing.T, store *session.Store, want int) {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize workflow assignment event log: %v", err)
	}
	recent, err := eventLog.ReadRecentRecords(100)
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

func assertUserMessageRecordCount(t *testing.T, store *session.Store, text string, want int) {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize user-message event log: %v", err)
	}
	recent, err := eventLog.ReadRecentRecords(100)
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

type workflowAssignmentScopeLifecycle struct {
	scopeID runtimeids.ExecutionScopeID
	live    atomic.Bool
}

func (*workflowAssignmentScopeLifecycle) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (*workflowAssignmentScopeLifecycle) StepEnded(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (l *workflowAssignmentScopeLifecycle) AgentStepBegan(
	context.Context,
	serverapi.RuntimeStepOrigin,
) (runtimeids.ExecutionScopeID, error) {
	return l.scopeID, nil
}

func (*workflowAssignmentScopeLifecycle) AgentStepBoundary(
	context.Context,
	serverapi.RuntimeStepOrigin,
) (AgentStepBoundaryTransfer, error) {
	return nil, errors.New("released source Agent Step reached a Boundary")
}

func (l *workflowAssignmentScopeLifecycle) AgentStepScopeLive(
	context.Context,
	runtimeids.ExecutionScopeID,
) bool {
	return l.live.Load()
}

func (l *workflowAssignmentScopeLifecycle) CurrentAgentExecutionScope(
	context.Context,
) (runtimeids.ExecutionScopeID, bool) {
	return l.scopeID, l.live.Load()
}
