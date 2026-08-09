package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/runtimecommand"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/textutil"

	"github.com/google/uuid"
)

type liveRunScopeLifecycleProbe struct {
	mu    sync.Mutex
	scope runtimeids.ExecutionScopeID
	live  bool
}

func (*liveRunScopeLifecycleProbe) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (*liveRunScopeLifecycleProbe) StepEnded(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (p *liveRunScopeLifecycleProbe) AgentStepScopeLive(
	context.Context,
	runtimeids.ExecutionScopeID,
) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live
}

func (p *liveRunScopeLifecycleProbe) CurrentAgentExecutionScope(
	context.Context,
) (runtimeids.ExecutionScopeID, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scope, p.live
}

func (p *liveRunScopeLifecycleProbe) setScope(scope runtimeids.ExecutionScopeID) {
	p.mu.Lock()
	p.scope = scope
	p.live = true
	p.mu.Unlock()
}

func (p *liveRunScopeLifecycleProbe) setLive(live bool) {
	p.mu.Lock()
	p.live = live
	p.mu.Unlock()
}

func TestInstantStopAgendaContract(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{name: "cancellation before input commits", run: testStopBeforeInputCommit},
		{name: "accepted Pending Work cannot deliver after cancellation", run: testStopBeforePendingDelivery},
		{name: "durably delivered input remains valid", run: testStopAfterDurableDelivery},
		{name: "disposition wins over later discard", run: testStopDispositionBeforeDiscard},
		{name: "runtime close preserves accepted disposition", run: testStopDispositionBeforeRuntimeClose},
	}
	for _, test := range tests {
		t.Run(test.name, test.run)
	}
}

func TestLiveRunWaitIdleReturnsNoActive(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if _, err := eng.WaitForActiveRunResult(context.Background()); !errors.Is(err, ErrNoActiveLiveRun) {
		t.Fatalf("WaitForActiveRunResult idle error = %v, want ErrNoActiveLiveRun", err)
	}
}

func TestCapturedActiveRunResultSurvivesFastCompletion(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("fast final"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})

	var handle *LiveRunWaitHandle
	var captureErr error
	assistant, err := eng.SubmitUserMessageWithHooks(context.Background(), "fast prompt", func() {
		handle, captureErr = eng.CaptureActiveRunResult(context.Background())
	}, nil)
	if err != nil {
		t.Fatalf("SubmitUserMessageWithHooks: %v", err)
	}
	if messageContent(assistant) != "fast final" {
		t.Fatalf("assistant content = %q, want fast final", messageContent(assistant))
	}
	if captureErr != nil || handle == nil {
		t.Fatalf("CaptureActiveRunResult handle=%v err=%v, want captured active run", handle, captureErr)
	}
	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live wait after fast completion: %v", err)
	}
	if messageContent(result.AssistantMessage) != "fast final" {
		t.Fatalf("captured final = %q, want fast final", messageContent(result.AssistantMessage))
	}
}

func TestTerminalWorkflowQueueFailureSettlesCanonicalPendingInput(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	startedAt := time.Now().UTC()
	snapshot := &RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindWorkflowTurn,
		StartedAt:  startedAt,
	}
	eng.liveRun.beginStep(snapshot)
	installTestAgentStepOrigin(t, eng, snapshot)
	item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer after workflow", nil)
	if err != nil || !accepted || item.ID == "" {
		t.Fatalf("QueueUserMessageForActiveRun item=%+v accepted=%t err=%v", item, accepted, err)
	}
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	completed := *snapshot
	completed.Status = RunStatusCompleted
	completed.FinishedAt = startedAt.Add(time.Second)
	eng.liveRun.finishStep(&completed, RunStatusCompleted, nil, false)
	eng.mu.Lock()
	eng.workflowTerminal = WorkflowTerminalState{
		Completed:   true,
		Source:      WorkflowCompletionSourceTool,
		CompletedAt: time.Now().UTC(),
	}
	eng.mu.Unlock()

	if !eng.failQueuedUserWorkIfTerminal() {
		t.Fatal("terminal workflow did not fail queued user work")
	}
	if _, err := handle.Wait(); !errors.Is(err, ErrLiveRunNoFinalAnswer) {
		t.Fatalf("live wait error = %v, want no-final after terminal queue failure", err)
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group stayed active after terminal queue failure")
	}
}

func TestExplicitQueueWaitsForTurnWhileLiveSteerAppliesAtStepBoundary(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *event.QueuedUserMessageStatus)
			}
		},
	})
	snapshot := &RunSnapshot{
		RunID:      uuid.NewString(),
		StepID:     uuid.NewString(),
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  time.Now().UTC(),
	}
	eng.liveRun.beginStep(snapshot)
	installTestAgentStepOrigin(t, eng, snapshot)

	queued, err := eng.QueueUserMessage("queue")
	if err != nil {
		t.Fatalf("QueueUserMessage: %v", err)
	}
	steered, accepted, err := eng.QueueUserMessageForActiveRun(
		context.Background(),
		"steer",
		nil,
	)
	if err != nil {
		t.Fatalf("QueueUserMessageForActiveRun: %v", err)
	}
	if !accepted {
		t.Fatal("QueueUserMessageForActiveRun rejected live Steer")
	}

	active := *eng.agentSteps.current
	if _, err := submitRuntimeEvent(
		eng,
		active,
		func(admission runtimeEventAdmission, step activeAgentStep) (humanBoundaryApplyResult, error) {
			return eng.applyHumanBoundary(admission, step.origin.StepID, stepBoundarySelection(step.scopeID, step.origin))
		},
	); err != nil {
		t.Fatalf("apply Step Boundary: %v", err)
	}
	pending := eng.boundaryAgenda.pendingHuman()
	if len(pending) != 1 || pending[0].ID != queued.ID {
		t.Fatalf("pending after Step Boundary = %+v, want Queue Item %s", pending, queued.ID)
	}
	if !queuedStatusObserved(statuses, steered.ID, QueuedUserMessageSubmitted) {
		t.Fatalf("statuses = %+v, want submitted Steer %s", statuses, steered.ID)
	}
	if queuedStatusObserved(statuses, queued.ID, QueuedUserMessageSubmitted) {
		t.Fatalf("Queue Item %s submitted at Step Boundary", queued.ID)
	}

	if _, err := submitRuntimeEvent(
		eng,
		active,
		func(admission runtimeEventAdmission, step activeAgentStep) (humanBoundaryApplyResult, error) {
			return eng.applyHumanBoundary(admission, step.origin.StepID, turnBoundarySelection(step.scopeID, step.origin))
		},
	); err != nil {
		t.Fatalf("apply Turn Boundary: %v", err)
	}
	if !queuedStatusObserved(statuses, queued.ID, QueuedUserMessageSubmitted) {
		t.Fatalf("statuses = %+v, want submitted Queue Item %s", statuses, queued.ID)
	}
}

func queuedStatusObserved(
	statuses []QueuedUserMessageStatusEvent,
	queueItemID string,
	status QueuedUserMessageStatus,
) bool {
	for _, observed := range statuses {
		if observed.QueueItemID == queueItemID && observed.Status == status {
			return true
		}
	}
	return false
}

func TestTryInterruptActiveRunNoopsAfterStepLeavesActiveState(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.liveRun.beginStep(&RunSnapshot{
		RunID:      "018fdd67-89ab-4cde-8123-456789abc001",
		StepID:     "018fdd67-89ab-4cde-8123-456789abc002",
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindUserTurn,
		StartedAt:  time.Now().UTC(),
	})

	stopped, err := eng.TryInterruptActiveRun()
	if err != nil {
		t.Fatalf("TryInterruptActiveRun: %v", err)
	}
	if stopped {
		t.Fatal("stop reported stopped after active step was already gone")
	}
}

func TestTryInterruptActiveRunCancelsCompactionStep(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	stepCtxSeen := make(chan context.Context, 1)
	done := make(chan error, 1)
	eng.ensureOrchestrationCollaborators()
	go func() {
		done <- eng.stepLifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindCompaction}, func(ctx context.Context, stepID string) error {
			stepCtxSeen <- ctx
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	var stepCtx context.Context
	select {
	case stepCtx = <-stepCtxSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for compaction step")
	}

	stopped, err := eng.TryInterruptActiveRun()
	if err != nil {
		t.Fatalf("TryInterruptActiveRun: %v", err)
	}
	if !stopped {
		t.Fatal("compaction live stop reported idle")
	}
	select {
	case <-stepCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("compaction step context was not canceled")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("compaction step error = %v, want context canceled", err)
	}
}

func TestExclusiveStepEmitRunStateControlsActiveLiveRunGroup(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	for _, test := range []struct {
		name        string
		options     exclusiveStepOptions
		wantActive  bool
		waitMessage string
	}{
		{
			name:        "emit run state",
			options:     exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
			wantActive:  true,
			waitMessage: "live run step",
		},
		{
			name:        "maintenance",
			options:     exclusiveStepOptions{EmitRunState: false, ActiveKind: ActiveKindRuntimeMaintenance},
			wantActive:  false,
			waitMessage: "maintenance step",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- lifecycle.Run(context.Background(), test.options, func(context.Context, string) error {
					close(started)
					<-release
					return nil
				})
			}()

			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatalf("timed out waiting for %s", test.waitMessage)
			}
			if active := eng.HasActiveLiveRunGroup(); active != test.wantActive {
				t.Fatalf("active live-run group = %t, want %t", active, test.wantActive)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("%s: %v", test.waitMessage, err)
			}
			if eng.HasActiveLiveRunGroup() {
				t.Fatal("live-run group stayed active after idle completion")
			}
		})
	}
}

func TestQueueUserMessageForActiveRunRejectsIdleWithoutBeforeQueue(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	called := false

	_, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer", func() error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrNoActiveLiveRun) || accepted {
		t.Fatalf("idle active-run queue accepted=%t err=%v, want no-active rejection", accepted, err)
	}
	if called {
		t.Fatal("beforeQueue was called for idle active-run queue")
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("idle active-run queue mutated queued user work")
	}
}

func TestQueueUserMessageForActiveRunRollsBackBeforeQueueError(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for step")
	}

	beforeErr := errors.New("history failed")
	if _, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer", func() error { return beforeErr }); !errors.Is(err, beforeErr) || accepted {
		t.Fatalf("beforeQueue error accepted=%t err=%v, want rollback error", accepted, err)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("rollback left queued user work")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("step: %v", err)
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("rollback kept live-run group open after step completion")
	}
}

func testStopBeforeInputCommit(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle

	started := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, _ string) error {
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}

	beforeStarted := make(chan struct{})
	releaseBefore := make(chan struct{})
	queueDone := make(chan struct {
		item     QueuedUserMessage
		accepted bool
		err      error
	}, 1)
	go func() {
		item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "must not queue", func() error {
			close(beforeStarted)
			<-releaseBefore
			return nil
		})
		queueDone <- struct {
			item     QueuedUserMessage
			accepted bool
			err      error
		}{item: item, accepted: accepted, err: err}
	}()
	select {
	case <-beforeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for beforeQueue")
	}
	blockerEntered := make(chan struct{})
	releaseBlocker := make(chan struct{})
	blocker, err := runtimecommand.Submit(
		context.Background(),
		eng.runtimeEvents,
		struct{}{},
		func(_ runtimecommand.Admission, _ struct{}, complete func(struct{}, error)) error {
			close(blockerEntered)
			<-releaseBlocker
			complete(struct{}{}, nil)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit blocking Runtime Event: %v", err)
	}
	<-blockerEntered
	stopActiveRunWithoutWaiting(t, eng)
	close(releaseBefore)
	close(releaseBlocker)
	if _, err := blocker.Await(context.Background()); err != nil {
		t.Fatalf("blocking Runtime Event: %v", err)
	}
	if err := <-stepDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped active step error = %v, want context canceled", err)
	}
	queued := <-queueDone
	if !errors.Is(queued.err, ErrNoActiveLiveRun) || queued.accepted || queued.item.ID != "" {
		t.Fatalf("blocked admission result = item=%+v accepted=%t err=%v, want no-active rejection without Queue Item", queued.item, queued.accepted, queued.err)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped blocked admission entered the Boundary Agenda")
	}
	waitEngineLifecycleTasks(t, eng)
	if got := client.callCount(); got != 0 {
		t.Fatalf("stopped blocked admission executed %d model calls", got)
	}
}

func TestWaitForActiveRunResultReturnsAssistantFinalAnswer(t *testing.T) {
	store := mustCreateTestSession(t)
	modelEntered := make(chan struct{})
	releaseModel := make(chan struct{})
	client := &hookClient{
		response: llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("final answer"), Phase: textutil.Value(llm.MessagePhaseFinal)}},
		beforeReturn: func() error {
			close(modelEntered)
			<-releaseModel
			return nil
		},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "hello")
		submitDone <- err
	}()

	select {
	case <-modelEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for model")
	}
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	close(releaseModel)
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	waited, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live run result: %v", err)
	}
	if waited.ResultKind != LiveRunResultAssistantFinalAnswer || messageContent(waited.AssistantMessage) != "final answer" {
		t.Fatalf("wait result = %+v", waited)
	}
}

func TestWaitForActiveRunResultReturnsNoFinalAnswerForShellRun(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserShell}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for shell run")
	}
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() {
		_, err := handle.Wait()
		waitDone <- err
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("shell run: %v", err)
	}
	if err := <-waitDone; !errors.Is(err, ErrLiveRunNoFinalAnswer) {
		t.Fatalf("WaitForActiveRunResult shell error = %v, want ErrLiveRunNoFinalAnswer", err)
	}
}

func TestTryInterruptActiveRunCancelsActiveStepAndWaiters(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() {
		_, err := handle.Wait()
		waitDone <- err
	}()
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active step error = %v, want context canceled", err)
	}
	if err := <-waitDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context canceled", err)
	}
}

func testStopBeforePendingDelivery(t *testing.T) {
	statuses := make(chan QueuedUserMessageStatusEvent, 4)
	scopeLifecycle := &liveRunScopeLifecycleProbe{}
	client := &fakeClient{}
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:         "gpt-5",
		StepLifecycle: scopeLifecycle,
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses <- *evt.QueuedUserMessageStatus
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- lifecycle.Run(
			context.Background(),
			exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
			func(stepCtx context.Context, _ string) error {
				close(started)
				<-stepCtx.Done()
				return stepCtx.Err()
			},
		)
	}()
	<-started
	installTestAgentStepOrigin(t, eng, eng.stepLifecycle.Snapshot())
	active := *eng.agentSteps.current
	scopeLifecycle.setScope(active.scopeID)

	item, accepted, err := eng.QueueUserMessageForActiveRun(
		context.Background(),
		"must remain pending after cancellation",
		nil,
	)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}

	blockerEntered := make(chan struct{})
	releaseBlocker := make(chan struct{})
	blocker, err := runtimecommand.Submit(
		context.Background(),
		eng.runtimeEvents,
		struct{}{},
		func(_ runtimecommand.Admission, _ struct{}, complete func(struct{}, error)) error {
			close(blockerEntered)
			<-releaseBlocker
			complete(struct{}{}, nil)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit blocking Runtime Event: %v", err)
	}
	<-blockerEntered

	delivery, err := runtimecommand.Submit(
		context.Background(),
		eng.runtimeEvents,
		active,
		func(
			command runtimecommand.Admission,
			step activeAgentStep,
			complete func(humanBoundaryApplyResult, error),
		) error {
			result, applyErr := eng.applyHumanBoundary(
				runtimeEventAdmission{engine: eng, command: command},
				step.origin.StepID,
				stepBoundarySelection(step.scopeID, step.origin),
			)
			complete(result, applyErr)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit Boundary delivery: %v", err)
	}

	stopActiveRunWithoutWaiting(t, eng)
	scopeLifecycle.setLive(false)
	close(releaseBlocker)
	if _, err := blocker.Await(context.Background()); err != nil {
		t.Fatalf("blocking Runtime Event: %v", err)
	}
	if result, err := delivery.Await(context.Background()); !errors.Is(err, ErrNoActiveLiveRun) || result.applied != 0 {
		t.Fatalf("stopped Boundary delivery result=%+v err=%v, want not applied", result, err)
	}
	if err := <-stepDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped step error = %v, want context canceled", err)
	}

	var observed []QueuedUserMessageStatusEvent
	for len(observed) < 2 {
		select {
		case status := <-statuses:
			if status.QueueItemID == item.ID {
				observed = append(observed, status)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("statuses = %+v, want accepted then stopped failure", observed)
		}
	}
	assertQueuedStatusOrder(t, observed, item.ID, []QueuedUserMessageStatus{
		QueuedUserMessageAccepted,
		QueuedUserMessageFailed,
	})
	assertStoppedQueuedStatus(t, observed, item.ID)
	if observed[len(observed)-1].RestoreText != "must remain pending after cancellation" {
		t.Fatalf("stopped Queue Item recovery = %+v", observed[len(observed)-1])
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped Queue Item remained in the Boundary Agenda")
	}
	if got := fakeClientCallCount(client); got != 0 {
		t.Fatalf("Stop allowed %d post-cancellation provider launches", got)
	}
}

func testStopAfterDurableDelivery(t *testing.T) {
	fixture := newStopAgendaFixture(t)
	item := fixture.accept(t, "already delivered")
	if _, err := submitRuntimeEvent(
		fixture.engine,
		fixture.active,
		func(admission runtimeEventAdmission, step activeAgentStep) (humanBoundaryApplyResult, error) {
			return fixture.engine.applyHumanBoundary(
				admission,
				step.origin.StepID,
				stepBoundarySelection(step.scopeID, step.origin),
			)
		},
	); err != nil {
		t.Fatalf("deliver Queue Item: %v", err)
	}
	blocker, release := fixture.blockRuntimeEvents(t)
	fixture.stop(t)
	release()
	fixture.awaitBlocker(t, blocker)
	fixture.awaitStepCancellation(t)

	statuses := fixture.awaitStatus(t, item.ID, QueuedUserMessageSubmitted)
	assertQueuedStatusOrder(t, statuses, item.ID, []QueuedUserMessageStatus{
		QueuedUserMessageAccepted,
		QueuedUserMessageSubmitted,
	})
	if statuses[len(statuses)-1].RestoreText != "" {
		t.Fatalf("durably delivered Queue Item requested draft recovery: %+v", statuses)
	}
	fixture.assertSettled(t)
}

func testStopDispositionBeforeDiscard(t *testing.T) {
	fixture := newStopAgendaFixture(t)
	item := fixture.accept(t, "restore after Stop")
	blocker, release := fixture.blockRuntimeEvents(t)
	fixture.stop(t)
	discarded := make(chan bool, 1)
	go func() {
		discarded <- fixture.engine.DiscardQueuedUserMessage(item.ID)
	}()
	release()
	fixture.awaitBlocker(t, blocker)
	if <-discarded {
		t.Fatal("discard removed a Queue Item already owned by Stop disposition")
	}
	fixture.awaitStepCancellation(t)

	statuses := fixture.awaitStatus(t, item.ID, QueuedUserMessageFailed)
	assertQueuedStatusOrder(t, statuses, item.ID, []QueuedUserMessageStatus{
		QueuedUserMessageAccepted,
		QueuedUserMessageFailed,
	})
	assertStoppedQueuedStatus(t, statuses, item.ID)
	if statuses[len(statuses)-1].RestoreText != "restore after Stop" {
		t.Fatalf("stopped Queue Item recovery = %+v", statuses[len(statuses)-1])
	}
	fixture.assertSettled(t)
}

func testStopDispositionBeforeRuntimeClose(t *testing.T) {
	fixture := newStopAgendaFixture(t)
	item := fixture.accept(t, "restore while closing")
	blocker, release := fixture.blockRuntimeEvents(t)
	fixture.stop(t)
	closed := make(chan error, 1)
	go func() {
		closed <- fixture.engine.Close()
	}()
	release()
	fixture.awaitBlocker(t, blocker)
	if err := <-closed; err != nil {
		t.Fatalf("close Engine: %v", err)
	}
	fixture.awaitStepCancellation(t)

	statuses := fixture.awaitStatus(t, item.ID, QueuedUserMessageFailed)
	assertQueuedStatusOrder(t, statuses, item.ID, []QueuedUserMessageStatus{
		QueuedUserMessageAccepted,
		QueuedUserMessageFailed,
	})
	assertStoppedQueuedStatus(t, statuses, item.ID)
	if statuses[len(statuses)-1].RestoreText != "restore while closing" {
		t.Fatalf("closing Queue Item recovery = %+v", statuses[len(statuses)-1])
	}
	if !fixture.engine.boundaryAgenda.isClosed() {
		t.Fatal("runtime close left the Boundary Agenda open")
	}
	fixture.assertSettled(t)
}

type stopAgendaFixture struct {
	engine         *Engine
	scopeLifecycle *liveRunScopeLifecycleProbe
	client         *blockingThenQueuedClient
	active         activeAgentStep
	statuses       chan QueuedUserMessageStatusEvent
	stepDone       chan error
}

func newStopAgendaFixture(t *testing.T) *stopAgendaFixture {
	t.Helper()
	scopeLifecycle := &liveRunScopeLifecycleProbe{}
	client := newBlockingThenQueuedClient()
	statuses := make(chan QueuedUserMessageStatusEvent, 16)
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:         "gpt-5",
		StepLifecycle: scopeLifecycle,
		OnEvent: func(event Event) {
			if event.QueuedUserMessageStatus != nil {
				statuses <- *event.QueuedUserMessageStatus
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	engine.stepLifecycle = lifecycle
	started := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- lifecycle.Run(
			context.Background(),
			exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
			func(stepCtx context.Context, _ string) error {
				close(started)
				<-stepCtx.Done()
				return stepCtx.Err()
			},
		)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}
	installTestAgentStepOrigin(t, engine, engine.stepLifecycle.Snapshot())
	active := *engine.agentSteps.current
	scopeLifecycle.setScope(active.scopeID)
	return &stopAgendaFixture{
		engine:         engine,
		scopeLifecycle: scopeLifecycle,
		client:         client,
		active:         active,
		statuses:       statuses,
		stepDone:       stepDone,
	}
}

func (f *stopAgendaFixture) accept(t *testing.T, text string) QueuedUserMessage {
	t.Helper()
	item, accepted, err := f.engine.QueueUserMessageForActiveRun(
		context.Background(),
		text,
		nil,
	)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}
	return item
}

func (f *stopAgendaFixture) blockRuntimeEvents(
	t *testing.T,
) (*runtimecommand.Deferred[struct{}], func()) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	deferred, err := runtimecommand.Submit(
		context.Background(),
		f.engine.runtimeEvents,
		struct{}{},
		func(_ runtimecommand.Admission, _ struct{}, complete func(struct{}, error)) error {
			close(entered)
			<-release
			complete(struct{}{}, nil)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit blocking Runtime Event: %v", err)
	}
	<-entered
	return deferred, sync.OnceFunc(func() { close(release) })
}

func (f *stopAgendaFixture) stop(t *testing.T) {
	t.Helper()
	stopActiveRunWithoutWaiting(t, f.engine)
	f.scopeLifecycle.setLive(false)
}

func stopActiveRunWithoutWaiting(t *testing.T, engine *Engine) {
	t.Helper()
	stopped := make(chan error, 1)
	go func() {
		didStop, err := engine.TryInterruptActiveRun()
		if err == nil && !didStop {
			err = errors.New("active Stop reported idle")
		}
		stopped <- err
	}()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("TryInterruptActiveRun: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop waited for Pending Work disposition")
	}
}

func (f *stopAgendaFixture) awaitBlocker(
	t *testing.T,
	blocker *runtimecommand.Deferred[struct{}],
) {
	t.Helper()
	if _, err := blocker.Await(context.Background()); err != nil {
		t.Fatalf("blocking Runtime Event: %v", err)
	}
}

func (f *stopAgendaFixture) awaitStepCancellation(t *testing.T) {
	t.Helper()
	if err := <-f.stepDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped step error = %v, want context canceled", err)
	}
}

func (f *stopAgendaFixture) awaitStatus(
	t *testing.T,
	queueItemID string,
	terminal QueuedUserMessageStatus,
) []QueuedUserMessageStatusEvent {
	t.Helper()
	var statuses []QueuedUserMessageStatusEvent
	for {
		select {
		case status := <-f.statuses:
			if status.QueueItemID != queueItemID {
				continue
			}
			statuses = append(statuses, status)
			if status.Status == terminal {
				return statuses
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("statuses = %+v, want %s for %s", statuses, terminal, queueItemID)
		}
	}
}

func (f *stopAgendaFixture) assertSettled(t *testing.T) {
	t.Helper()
	if f.engine.HasQueuedUserWork() {
		t.Fatal("Stop left a Queue Item in the Boundary Agenda projection")
	}
	if got := f.client.callCount(); got != 0 {
		t.Fatalf("Stop allowed %d post-cancellation provider launches", got)
	}
}

func TestTryInterruptActiveRunIdleNoops(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || stopped {
		t.Fatalf("TryInterruptActiveRun idle stopped=%t err=%v, want no-op", stopped, err)
	}
}

func assertStoppedQueuedStatus(t *testing.T, statuses []QueuedUserMessageStatusEvent, queueItemID string) {
	t.Helper()
	for _, status := range statuses {
		if status.QueueItemID == queueItemID && status.Status == QueuedUserMessageFailed && status.FailureReason == QueuedUserMessageFailureStopped {
			return
		}
	}
	t.Fatalf("missing stopped failure for queue item %q in statuses %+v", queueItemID, statuses)
}

func assertQueuedStatusOrder(t *testing.T, statuses []QueuedUserMessageStatusEvent, queueItemID string, want []QueuedUserMessageStatus) {
	t.Helper()
	got := make([]QueuedUserMessageStatus, 0, len(statuses))
	for _, status := range statuses {
		if status.QueueItemID == queueItemID {
			got = append(got, status.Status)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("status order for %q = %+v, want %+v", queueItemID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status order for %q = %+v, want %+v", queueItemID, got, want)
		}
	}
}
