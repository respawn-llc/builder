package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"

	"github.com/google/uuid"
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
	case <-time.After(runtimeTestSynchronizationTimeout):
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
	case <-time.After(runtimeTestSynchronizationTimeout):
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
	eng.stepLifecycle = lifecycle

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
			options:     exclusiveStepOptions{EmitRunState: false, ActiveKind: ActiveKindCompaction},
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
			case <-time.After(runtimeTestSynchronizationTimeout):
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
	case <-time.After(runtimeTestSynchronizationTimeout):
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
	case <-time.After(runtimeTestSynchronizationTimeout):
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

func TestInstantStopBroadcastsRemovedHumanInputInAcceptanceOrder(t *testing.T) {
	toolCall := llm.ToolCall{
		ID:    "hold-before-interrupt",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("working", toolCall),
		finalTextResponse("must not run"),
	}}
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	var (
		eventsMu sync.Mutex
		events   []Event
	)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t, tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand,
		Handler: blockingTool{
			name:    toolspec.ToolExecCommand,
			started: toolStarted,
			release: releaseTool,
		},
	}), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		},
	})

	initialDone := make(chan error, 1)
	go func() {
		_, err := eng.SubmitUserMessage(t.Context(), "start")
		initialDone <- err
	}()
	select {
	case <-toolStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("preceding Agent Step did not reach its held tool")
	}

	type queuedResult struct {
		item     QueuedUserMessage
		accepted bool
		err      error
	}
	firstApplying := make(chan struct{})
	releaseFirstApplication := make(chan struct{})
	firstDone := make(chan queuedResult, 1)
	go func() {
		item, accepted, err := eng.QueueUserMessageForActiveRun(t.Context(), "first", func() error {
			close(firstApplying)
			<-releaseFirstApplication
			return nil
		})
		firstDone <- queuedResult{item: item, accepted: accepted, err: err}
	}()
	waitForAcceptedRuntimeOperationCount(t, eng, 1)
	close(releaseTool)
	select {
	case <-firstApplying:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("first queued input did not apply at the preceding Step Boundary")
	}

	secondDone := make(chan queuedResult, 1)
	go func() {
		item, accepted, err := eng.QueueUserMessageForActiveRun(t.Context(), "second", nil)
		secondDone <- queuedResult{item: item, accepted: accepted, err: err}
	}()
	transitionStarted := make(chan struct{})
	releaseTransition := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- runScheduledWorktreeTransitionForTest(t.Context(), eng, func() error {
			close(transitionStarted)
			<-releaseTransition
			return nil
		})
	}()
	waitForAcceptedRuntimeOperationCount(t, eng, 2)
	close(releaseFirstApplication)
	first := <-firstDone
	if first.err != nil || !first.accepted {
		t.Fatalf("queue first input accepted=%t err=%v", first.accepted, first.err)
	}
	second := <-secondDone
	if second.err != nil || !second.accepted {
		t.Fatalf("queue second input accepted=%t err=%v", second.accepted, second.err)
	}
	select {
	case <-transitionStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Worktree transition did not hold the next provider boundary")
	}

	stopped, err := eng.TryInterruptActiveRun()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveRun stopped=%t err=%v, want active stop", stopped, err)
	}
	close(releaseTransition)
	if err := <-transitionDone; err != nil {
		t.Fatalf("Worktree transition: %v", err)
	}
	if err := <-initialDone; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("preceding Agent turn error = %v, want success or context cancellation", err)
	}

	eventsMu.Lock()
	defer eventsMu.Unlock()
	var interruptions []HumanInputInterruptedEvent
	for _, event := range events {
		if event.HumanInputInterrupted != nil {
			interruptions = append(interruptions, *event.HumanInputInterrupted)
		}
		if event.QueuedUserMessageStatus != nil &&
			event.QueuedUserMessageStatus.Status == QueuedUserMessageFailed &&
			(event.QueuedUserMessageStatus.QueueItemID == first.item.ID ||
				event.QueuedUserMessageStatus.QueueItemID == second.item.ID) {
			t.Fatalf("Instant Stop emitted obsolete queued-message failure: %+v", event.QueuedUserMessageStatus)
		}
	}
	if len(interruptions) != 1 {
		t.Fatalf("human-input interruption events = %+v, want one", interruptions)
	}
	items := interruptions[0].Items
	if len(items) != 2 ||
		items[0].QueueItemID != first.item.ID || items[0].Text != "first" ||
		items[1].QueueItemID != second.item.ID || items[1].Text != "second" {
		t.Fatalf("interrupted inputs = %+v, want accepted inputs in server order", items)
	}
}

func TestTryInterruptActiveAgentTurnPreservesGoalLoopInterruptBookkeeping(t *testing.T) {
	store := mustCreateNamedTestSession(t, "workspace-x", "/tmp/workspace-x")
	client := newScriptedGoalLoopClient()
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := eng.SetGoal(t.Context(), "interrupt ordinary goal Agent Turn", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := eng.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	client.waitStarted(t, 1)

	stopped, err := eng.TryInterruptActiveAgentTurn()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveAgentTurn stopped=%t err=%v, want active goal stop", stopped, err)
	}
	waitGoalLoopRunning(t, eng, false)
	if !eng.GoalLoopSuspended() {
		t.Fatal("ordinary Agent-Turn interrupt did not suspend the active goal loop")
	}
	if got := client.callCount(); got != 1 {
		t.Fatalf("model calls after goal interrupt = %d, want 1", got)
	}
}

func TestTryInterruptActiveAgentTurnLeavesStaleGoalLiveRunGroupRunning(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.ensureOrchestrationCollaborators()
	activeRunID := uuid.NewString()
	activeStepID := uuid.NewString()
	canceled := false
	eng.stepLifecycle = &defaultExclusiveStepLifecycle{
		engine: eng,
		active: &exclusiveRunState{
			sequence:   2,
			activeKind: ActiveKindUserTurn,
			cancel: func() {
				canceled = true
			},
			runID:     activeRunID,
			stepID:    activeStepID,
			startedAt: time.Now().UTC(),
		},
	}
	eng.liveRun.beginStep(&RunSnapshot{
		RunID:      uuid.NewString(),
		StepID:     uuid.NewString(),
		Status:     RunStatusRunning,
		ActiveKind: ActiveKindGoalLoop,
		GoalLoop:   true,
		StartedAt:  time.Now().UTC(),
	})
	eng.liveRun.mu.Lock()
	staleGroup := eng.liveRun.current
	staleDone := staleGroup.done
	eng.liveRun.mu.Unlock()

	stopped, err := eng.TryInterruptActiveAgentTurn()
	if err != nil || !stopped {
		t.Fatalf("TryInterruptActiveAgentTurn stopped=%t err=%v, want exact active step interrupted", stopped, err)
	}
	if !canceled {
		t.Fatal("exact active Agent Turn was not canceled")
	}
	eng.liveRun.mu.Lock()
	current := eng.liveRun.current
	status := staleGroup.status
	eng.liveRun.mu.Unlock()
	if current != staleGroup || status != RunStatusRunning {
		t.Fatalf("stale live-run group changed: current=%p stale=%p status=%s", current, staleGroup, status)
	}
	select {
	case <-staleDone:
		t.Fatal("stale goal live-run group was completed by a succeeding Agent-Turn interrupt")
	default:
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
