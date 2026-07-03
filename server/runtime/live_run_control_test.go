package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/runtimeids"
)

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
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "fast final", Phase: llm.MessagePhaseFinal},
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
	if assistant.Content != "fast final" {
		t.Fatalf("assistant content = %q, want fast final", assistant.Content)
	}
	if captureErr != nil || handle == nil {
		t.Fatalf("CaptureActiveRunResult handle=%v err=%v, want captured active run", handle, captureErr)
	}
	result, err := handle.Wait()
	if err != nil {
		t.Fatalf("captured live wait after fast completion: %v", err)
	}
	if result.AssistantMessage.Content != "fast final" {
		t.Fatalf("captured final = %q, want fast final", result.AssistantMessage.Content)
	}
}

func TestTerminalWorkflowQueueFailureCompletesTaggedLiveItems(t *testing.T) {
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
	item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer after workflow", liveRunTestRequestID(t), nil)
	if err != nil || !accepted || item.ID == "" {
		t.Fatalf("QueueUserMessageForActiveRun item=%+v accepted=%t err=%v", item, accepted, err)
	}
	completed := *snapshot
	completed.Status = RunStatusCompleted
	completed.FinishedAt = startedAt.Add(time.Second)
	eng.liveRun.finishStep(&completed, RunStatusCompleted, nil, false)
	handle, err := eng.CaptureActiveRunResult(context.Background())
	if err != nil {
		t.Fatalf("CaptureActiveRunResult: %v", err)
	}
	eng.mu.Lock()
	eng.workflowTerminal = WorkflowTerminalState{
		Completed:   true,
		RunID:       "workflow-run",
		Generation:  1,
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

func TestEmitRunStateStepOpensActiveLiveRunGroup(t *testing.T) {
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
		t.Fatal("timed out waiting for live run step")
	}
	if !eng.HasActiveLiveRunGroup() {
		t.Fatal("EmitRunState=true step did not open active live-run group")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("live run step: %v", err)
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group stayed active after idle completion")
	}
}

func TestMaintenanceStepDoesNotOpenActiveLiveRunGroup(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: false, ActiveKind: ActiveKindRuntimeMaintenance}, func(context.Context, string) error {
			close(started)
			<-release
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for maintenance step")
	}
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("EmitRunState=false step opened active live-run group")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("maintenance step: %v", err)
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

func TestQueueUserMessageForActiveRunAdmissionKeepsGroupOpenAcrossStepFinish(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	stepStarted := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			close(stepStarted)
			<-releaseStep
			return nil
		})
	}()
	select {
	case <-stepStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step")
	}

	beforeStarted := make(chan struct{})
	releaseBefore := make(chan struct{})
	queueDone := make(chan error, 1)
	go func() {
		item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "steer", liveRunTestRequestID(t), func() error {
			close(beforeStarted)
			<-releaseBefore
			return nil
		})
		if err == nil && (!accepted || item.ID == "" || item.Text != "steer") {
			err = errors.New("unexpected accepted queue item")
		}
		queueDone <- err
	}()
	select {
	case <-beforeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for beforeQueue")
	}

	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("step: %v", err)
	}
	if !eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group closed while admitted steering was blocked in beforeQueue")
	}
	close(releaseBefore)
	if err := <-queueDone; err != nil {
		t.Fatalf("active-run queue: %v", err)
	}
	if !eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group closed before tagged queued steering could drain")
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

func TestQueueUserMessageForActiveRunStopCancelsBlockedAdmissionBeforeQueueMutation(t *testing.T) {
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
	if !errors.Is(queued.err, context.Canceled) || queued.accepted || queued.item.ID != "" {
		t.Fatalf("blocked admission result = item=%+v accepted=%t err=%v, want canceled without queue metadata", queued.item, queued.accepted, queued.err)
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
		response: llm.Response{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "final answer", Phase: llm.MessagePhaseFinal}},
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
	if waited.ResultKind != LiveRunResultAssistantFinalAnswer || waited.AssistantMessage.Content != "final answer" {
		t.Fatalf("wait result = %+v", waited)
	}
}

func TestWaitForActiveRunResultWaitsForTaggedQueuedDrainResult(t *testing.T) {
	store := mustCreateTestSession(t)
	client := newBlockingThenBlockedQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	submitDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(context.Background(), "hello")
		submitDone <- err
	}()
	client.waitStarted(t)

	queued := eng.QueueUserMessageWithClientRequestID("steer into drain", "req-queued")
	if queued.ID == "" {
		t.Fatal("busy queued message returned empty id")
	}
	waitDone := make(chan struct {
		result LiveRunResult
		err    error
	}, 1)
	go func() {
		result, err := eng.WaitForActiveRunResult(context.Background())
		waitDone <- struct {
			result LiveRunResult
			err    error
		}{result: result, err: err}
	}()

	client.release()
	client.waitSecondStarted(t)
	select {
	case waited := <-waitDone:
		t.Fatalf("wait completed before tagged queued drain model result: result=%+v err=%v", waited.result, waited.err)
	default:
	}
	client.releaseSecond()
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	waited := <-waitDone
	if waited.err != nil {
		t.Fatalf("WaitForActiveRunResult: %v", waited.err)
	}
	if waited.result.AssistantMessage.Content != "queued work handled" {
		t.Fatalf("wait result = %+v, want queued work handled", waited.result)
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
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
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
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped accepted steering remained queued")
	}
	assertStoppedQueuedStatus(t, statuses, item.ID)
}

func TestTryInterruptActiveRunFailsAcceptedSteeringInTaggedQueueGap(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	client := newBlockingThenQueuedClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	eng.pauseQueuedUserAutoDrain()

	started := make(chan struct{})
	releaseStep := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
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
	item, accepted, err := eng.QueueUserMessageForActiveRun(context.Background(), "do not drain", liveRunTestRequestID(t), nil)
	if err != nil || !accepted {
		t.Fatalf("QueueUserMessageForActiveRun accepted=%t err=%v", accepted, err)
	}
	close(releaseStep)
	if err := <-done; err != nil {
		t.Fatalf("active step: %v", err)
	}
	if !eng.HasActiveLiveRunGroup() {
		t.Fatal("live-run group closed before stop in tagged queue gap")
	}
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want tagged-gap stop", stopped, err)
	}
	eng.resumeQueuedUserAutoDrain()
	waitEngineLifecycleTasks(t, eng)
	if eng.HasQueuedUserWork() {
		t.Fatal("stopped tagged-gap steering remained queued")
	}
	if got := client.callCount(); got != 0 {
		t.Fatalf("stopped tagged-gap steering executed %d model calls", got)
	}
	assertStoppedQueuedStatus(t, statuses, item.ID)
}

func TestTryInterruptActiveRunDefersPublishingQueueItemFailureUntilAcceptedStatus(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
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

	item := QueuedUserMessage{ID: runtimeids.NewQueueItemID().String(), Text: "race-safe steer", ClientRequestID: "req-publishing"}
	tagged := eng.liveRun.beginQueueItemPublication(mustQueueItemID(item.ID), func(queueItemID string) {
		eng.markQueuedUserInjectionForAutoDrain(queueItemID)
	})
	if !tagged || item.ID == "" {
		t.Fatalf("publishing queue setup tagged=%t item=%+v", tagged, item)
	}
	item = eng.messageFlow.QueueUserMessageWithID(item)
	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	if !eng.HasQueuedUserWork() || !eng.hasQueuedUserAutoDrainIDs() {
		t.Fatal("publishing item was failed before acceptance was emitted")
	}

	eng.emitQueuedUserMessageStatus(item, QueuedUserMessageAccepted, "", false)
	queueItemID := mustQueueItemID(item.ID)
	if !eng.liveRun.finishQueueItemPublication(queueItemID) {
		t.Fatal("publishing item was not marked stopped after concurrent stop")
	}
	eng.failStoppedLiveRunQueueItems(map[runtimeids.QueueItemID]struct{}{queueItemID: {}})
	if eng.HasQueuedUserWork() || eng.hasQueuedUserAutoDrainIDs() {
		t.Fatal("stopped publishing item left queued work or stale auto-drain id")
	}
	assertQueuedStatusOrder(t, statuses, item.ID, []QueuedUserMessageStatus{QueuedUserMessageAccepted, QueuedUserMessageFailed})
	assertStoppedQueuedStatus(t, statuses, item.ID)
	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("active step: %v", err)
	}
}

func TestDroppedStoppedLiveRunQueueItemsClearAutoDrainState(t *testing.T) {
	store := mustCreateTestSession(t)
	var statuses []QueuedUserMessageStatusEvent
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.QueuedUserMessageStatus != nil {
				statuses = append(statuses, *evt.QueuedUserMessageStatus)
			}
		},
	})
	item := QueuedUserMessage{ID: runtimeids.NewQueueItemID().String(), Text: "stopped drained", ClientRequestID: "req-stopped"}
	queueItemID := mustQueueItemID(item.ID)
	eng.markQueuedUserInjectionForAutoDrain(item.ID)
	eng.liveRun.mu.Lock()
	eng.liveRun.markStoppedQueueItemsLocked(map[runtimeids.QueueItemID]struct{}{queueItemID: {}})
	eng.liveRun.mu.Unlock()

	remaining := eng.dropStoppedLiveRunQueueItems([]queuedUserSteeringIntent{{message: item}})
	if len(remaining) != 0 {
		t.Fatalf("remaining stopped drained items = %+v, want none", remaining)
	}
	if eng.hasQueuedUserAutoDrainIDs() {
		t.Fatal("stopped drained item left stale auto-drain id")
	}
	assertStoppedQueuedStatus(t, statuses, item.ID)

	idle := eng.QueueUserMessage("idle after stopped drain")
	if eng.hasQueuedUserAutoDrainIDs() {
		t.Fatal("later idle queue was marked for auto-drain by stale stopped state")
	}
	if !eng.DiscardQueuedUserMessage(idle.ID) {
		t.Fatal("later idle queue was not left pending")
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
