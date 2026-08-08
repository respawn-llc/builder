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
	item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer after workflow", liveRunTestRequestID(t), nil)
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

	queued, err := eng.QueueUserMessageWithClientRequestID("queue", liveRunTestRequestID(t).String())
	if err != nil {
		t.Fatalf("QueueUserMessageWithClientRequestID: %v", err)
	}
	steered, accepted, err := eng.QueueUserMessageForActiveRun(
		context.Background(),
		"steer",
		liveRunTestRequestID(t),
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

	_, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer", liveRunTestRequestID(t), func() error {
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
	if _, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer", liveRunTestRequestID(t), func() error { return beforeErr }); !errors.Is(err, beforeErr) || accepted {
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

func TestQueueUserMessageForActiveRunStopRejectsBlockedPreAcceptanceWithoutMutation(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle

	started := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(started)
			<-releaseStep
			return nil
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
		item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "must not queue", liveRunTestRequestID(t), func() error {
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
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("stopped active step: %v", err)
	}

	replacementStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	replacementDone := make(chan error, 1)
	go func() {
		replacementDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(replacementStarted)
			<-releaseReplacement
			return nil
		})
	}()
	select {
	case <-replacementStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for replacement active step")
	}

	close(releaseBefore)
	queued := <-queueDone
	if !errors.Is(queued.err, ErrNoActiveLiveRun) || queued.accepted || queued.item.ID != "" {
		t.Fatalf("blocked admission result = item=%+v accepted=%t err=%v, want no-active rejection without Queue Item", queued.item, queued.accepted, queued.err)
	}
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped blocked admission queued stale work into replacement run")
	}
	close(releaseReplacement)
	if err := <-replacementDone; err != nil {
		t.Fatalf("replacement active step: %v", err)
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

func TestTryInterruptActiveRunFailsAcceptedSteeringWhileStepRuns(t *testing.T) {
	store := mustCreateTestSession(t)
	statuses := make(chan QueuedUserMessageStatusEvent, 4)
	scopeLifecycle := &liveRunScopeLifecycleProbe{}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
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
	installTestAgentStepOrigin(t, eng, eng.stepLifecycle.Snapshot())
	scopeLifecycle.setScope(eng.agentSteps.current.scopeID)

	item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "do not run", liveRunTestRequestID(t), nil)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("active step error = %v, want context canceled", err)
	}
	var observed []QueuedUserMessageStatusEvent
	for {
		select {
		case status := <-statuses:
			observed = append(observed, status)
			if status.QueueItemID == item.ID &&
				status.Status == QueuedUserMessageFailed &&
				status.FailureReason == QueuedUserMessageFailureStopped {
				goto settled
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("statuses = %+v, want stopped failure for %q", observed, item.ID)
		}
	}
settled:
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped accepted steering remained queued")
	}
}

func TestTryInterruptActiveRunReturnsAfterStopDispositionIsAccepted(t *testing.T) {
	store := mustCreateTestSession(t)
	statuses := make(chan QueuedUserMessageStatusEvent, 4)
	scopeLifecycle := &liveRunScopeLifecycleProbe{}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
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
		stepDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, _ string) error {
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()
	<-started
	installTestAgentStepOrigin(t, eng, eng.stepLifecycle.Snapshot())
	scopeLifecycle.setScope(eng.agentSteps.current.scopeID)
	item, accepted, err := eng.QueueUserMessageForActiveRun(
		context.Background(),
		"stop asynchronously",
		liveRunTestRequestID(t),
		nil,
	)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}

	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	blocker, err := runtimecommand.Submit(
		context.Background(),
		eng.runtimeEvents,
		struct{}{},
		func(
			_ runtimecommand.Admission,
			_ struct{},
			complete func(struct{}, error),
		) error {
			close(handlerEntered)
			<-releaseHandler
			complete(struct{}{}, nil)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("submit blocking Runtime Event: %v", err)
	}
	<-handlerEntered

	stopDone := make(chan error, 1)
	go func() {
		stopped, stopErr := eng.TryInterruptActiveRun()
		if stopErr == nil && !stopped {
			stopErr = errors.New("active Stop reported idle")
		}
		stopDone <- stopErr
	}()
	select {
	case stopErr := <-stopDone:
		if stopErr != nil {
			t.Fatalf("TryInterruptActiveRun: %v", stopErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop waited for its disposition handler instead of returning after queue receipt")
	}
	select {
	case status := <-statuses:
		if status.QueueItemID == item.ID && status.Status == QueuedUserMessageFailed {
			t.Fatal("Stop disposition ran while an earlier Runtime Event handler was blocked")
		}
	default:
	}

	close(releaseHandler)
	if _, err := blocker.Await(context.Background()); err != nil {
		t.Fatalf("blocking Runtime Event: %v", err)
	}
	if err := <-stepDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped step error = %v, want context canceled", err)
	}
	for {
		select {
		case status := <-statuses:
			if status.QueueItemID == item.ID &&
				status.Status == QueuedUserMessageFailed &&
				status.FailureReason == QueuedUserMessageFailureStopped {
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Stop disposition did not settle Queue Item %s", item.ID)
		}
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

func liveRunTestRequestID(t *testing.T) runtimeids.RuntimeClientRequestID {
	t.Helper()
	id, err := runtimeids.ParseRuntimeClientRequestID("018fdd67-89ab-4cde-8123-456789abcdef")
	if err != nil {
		t.Fatalf("parse live-run test request id: %v", err)
	}
	return id
}
