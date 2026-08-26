package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func waitForAcceptedRuntimeOperationCount(t *testing.T, engine *Engine, want int) {
	t.Helper()
	deadline := time.Now().Add(runtimeTestSynchronizationTimeout)
	for time.Now().Before(deadline) {
		engine.runtimeFIFO.mu.Lock()
		got := engine.runtimeFIFO.pendingCount
		engine.runtimeFIFO.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	engine.runtimeFIFO.mu.Lock()
	got := engine.runtimeFIFO.pendingCount
	engine.runtimeFIFO.mu.Unlock()
	t.Fatalf("accepted Runtime operation count = %d, want %d", got, want)
}

func TestRuntimeOperationFIFOCompletesTypedOperationsInAcceptanceOrder(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := submitRuntimeOperation(fifo, func(context.Context) (int, error) {
		close(firstStarted)
		<-releaseFirst
		return 41, nil
	})
	select {
	case <-firstStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for first Runtime operation")
	}

	secondStarted := make(chan struct{})
	second := submitRuntimeOperation(fifo, func(context.Context) (string, error) {
		close(secondStarted)
		return "second", nil
	})
	select {
	case <-secondStarted:
		t.Fatal("second Runtime operation started before the first completed")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseFirst)
	firstResult, err := first.Await(t.Context())
	if err != nil || firstResult != 41 {
		t.Fatalf("first Runtime operation = %d, %v; want 41, nil", firstResult, err)
	}
	secondResult, err := second.Await(t.Context())
	if err != nil || secondResult != "second" {
		t.Fatalf("second Runtime operation = %q, %v; want second, nil", secondResult, err)
	}
}

func TestRuntimeOperationFIFODefersAcceptedOperationsUntilTheProtectedStepBoundary(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)
	if err := fifo.Pause(t.Context()); err != nil {
		t.Fatalf("pause Runtime operations: %v", err)
	}

	started := make(chan struct{})
	deferred := submitRuntimeOperation(fifo, func(context.Context) (string, error) {
		close(started)
		return "applied", nil
	})
	select {
	case <-started:
		t.Fatal("Runtime operation applied during a protected Agent Step")
	case <-time.After(25 * time.Millisecond):
	}

	if err := fifo.Drain(t.Context()); err != nil {
		t.Fatalf("drain Runtime operations at Step Boundary: %v", err)
	}
	result, err := deferred.Await(t.Context())
	if err != nil || result != "applied" {
		t.Fatalf("deferred Runtime operation = %q, %v; want applied, nil", result, err)
	}
}

func TestRuntimeOperationFIFOLongOwnerReentersAtTheCurrentTail(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)

	var mu sync.Mutex
	applied := make([]string, 0, 3)
	record := func(value string) {
		mu.Lock()
		applied = append(applied, value)
		mu.Unlock()
	}

	longOwnerStarted := make(chan struct{})
	releaseLongOwner := make(chan struct{})
	longOwnerDone := make(chan error, 1)
	scheduled := submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
		record("scheduled")
		go func() {
			close(longOwnerStarted)
			<-releaseLongOwner
			_, err := submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
				record("terminal")
				return struct{}{}, nil
			}).Await(t.Context())
			longOwnerDone <- err
		}()
		return struct{}{}, nil
	})
	if _, err := scheduled.Await(t.Context()); err != nil {
		t.Fatalf("schedule long owner: %v", err)
	}
	select {
	case <-longOwnerStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for long owner")
	}

	later := submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
		record("later")
		return struct{}{}, nil
	})
	close(releaseLongOwner)
	if _, err := later.Await(t.Context()); err != nil {
		t.Fatalf("apply later Runtime mutation: %v", err)
	}
	if err := <-longOwnerDone; err != nil {
		t.Fatalf("apply long-owner terminal mutation: %v", err)
	}

	mu.Lock()
	got := slices.Clone(applied)
	mu.Unlock()
	if want := []string{"scheduled", "later", "terminal"}; !slices.Equal(got, want) {
		t.Fatalf("Runtime operation order = %v, want %v", got, want)
	}
}

func TestWorktreeTransitionRunsBeforeQueuedHumanProviderTurn(t *testing.T) {
	toolCall := llm.ToolCall{
		ID:    "hold-preceding-step",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("working", toolCall),
		finalTextResponse("done"),
	}}
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand,
			Handler: blockingTool{
				name:    toolspec.ToolExecCommand,
				started: toolStarted,
				release: releaseTool,
			},
		}),
		Config{Model: "gpt-5"},
	)

	initialDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(t.Context(), "start")
		initialDone <- err
	}()
	select {
	case <-toolStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("preceding Agent Step did not reach its held tool")
	}

	humanApplying := make(chan struct{})
	releaseHumanApplication := make(chan struct{})
	humanDone := make(chan error, 1)
	go func() {
		_, accepted, err := engine.QueueUserMessageForActiveRun(
			t.Context(),
			"queued human turn",
			func() error {
				close(humanApplying)
				<-releaseHumanApplication
				return nil
			},
		)
		if err == nil && !accepted {
			err = errors.New("queued Human input was not accepted")
		}
		humanDone <- err
	}()
	waitForAcceptedRuntimeOperationCount(t, engine, 1)
	close(releaseTool)
	select {
	case <-humanApplying:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("queued Human input did not apply at the preceding Step Boundary")
	}

	transitionStarted := make(chan struct{})
	releaseTransition := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- engine.RunWorktreeTransition(t.Context(), func() error {
			close(transitionStarted)
			<-releaseTransition
			return nil
		})
	}()
	waitForAcceptedRuntimeOperationCount(t, engine, 2)
	close(releaseHumanApplication)
	if err := <-humanDone; err != nil {
		t.Fatalf("accept queued Human input: %v", err)
	}

	select {
	case <-transitionStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Worktree transition did not start at the preceding Step Boundary")
	}
	if got := fakeClientCallCount(client); got != 1 {
		t.Fatal("queued Human provider turn started while the Worktree transition held eligibility")
	}

	close(releaseTransition)
	if err := <-transitionDone; err != nil {
		t.Fatalf("Worktree transition: %v", err)
	}
	deadline := time.Now().Add(runtimeTestSynchronizationTimeout)
	for fakeClientCallCount(client) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := fakeClientCallCount(client); got != 2 {
		t.Fatal("queued Human provider turn did not start after the Worktree transition")
	}
	if err := <-initialDone; err != nil {
		t.Fatalf("preceding Agent turn: %v", err)
	}
}

func TestWorktreeTransitionWaitsForActiveAgentStepBoundary(t *testing.T) {
	toolCall := llm.ToolCall{
		ID:    "hold-active-step",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	client := &fakeClient{responses: []llm.Response{
		commentaryResponse("working", toolCall),
		finalTextResponse("done"),
	}}
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand,
			Handler: blockingTool{
				name:    toolspec.ToolExecCommand,
				started: toolStarted,
				release: releaseTool,
			},
		}),
		Config{Model: "gpt-5"},
	)

	initialDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(t.Context(), "start")
		initialDone <- err
	}()
	select {
	case <-toolStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("active Agent Step did not reach its held tool")
	}

	transitionStarted := make(chan struct{})
	releaseTransition := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- engine.RunWorktreeTransition(t.Context(), func() error {
			close(transitionStarted)
			<-releaseTransition
			return nil
		})
	}()
	waitForAcceptedRuntimeOperationCount(t, engine, 1)
	select {
	case <-transitionStarted:
		t.Fatal("Worktree transition preempted the active Agent Step")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseTool)
	select {
	case <-transitionStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Worktree transition did not receive the next Agent Step Boundary")
	}
	if got := fakeClientCallCount(client); got != 1 {
		t.Fatalf("ordinary provider continuation started before Worktree: calls=%d", got)
	}

	close(releaseTransition)
	if err := <-transitionDone; err != nil {
		t.Fatalf("Worktree transition: %v", err)
	}
	deadline := time.Now().Add(runtimeTestSynchronizationTimeout)
	for fakeClientCallCount(client) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := fakeClientCallCount(client); got != 2 {
		t.Fatalf("ordinary provider continuation calls=%d, want 2 after Worktree", got)
	}
	if err := <-initialDone; err != nil {
		t.Fatalf("active Agent turn: %v", err)
	}
}

func newHeldReviewerWorktreeEngine(t *testing.T, mainClient llm.Client) (*Engine, <-chan struct{}) {
	t.Helper()
	reviewerClient, reviewerStarted, releaseReviewer := newGatedHookClient(
		llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value(`{"suggestions":[]}`),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		llm.Response{},
	)
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		mainClient,
		tools.NewRegistry(),
		Config{
			Model: "gpt-5",
			Reviewer: ReviewerConfig{
				Frequency:     "all",
				Model:         "gpt-5",
				ThinkingLevel: "low",
				Client:        reviewerClient,
			},
		},
	)
	t.Cleanup(func() {
		releaseReviewer()
		waitEngineLifecycleTasks(t, engine)
	})
	return engine, reviewerStarted
}

func TestWorktreeTransitionRunsWhileReviewerIsActive(t *testing.T) {
	engine, reviewerStarted := newHeldReviewerWorktreeEngine(
		t,
		&fakeClient{responses: []llm.Response{finalTextResponse("done")}},
	)
	if _, err := engine.SubmitUserMessage(t.Context(), "start Reviewer"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	select {
	case <-reviewerStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Reviewer provider request")
	}

	transitionRan := false
	err := engine.RunWorktreeTransition(t.Context(), func() error {
		transitionRan = true
		return nil
	})
	if err != nil {
		t.Fatalf("RunWorktreeTransition: %v", err)
	}
	if !transitionRan {
		t.Fatal("Worktree transition did not run while Reviewer was active")
	}
}

type nonOverlappingStepLifecycleSink struct {
	mu     sync.Mutex
	active bool
}

func (s *nonOverlappingStepLifecycleSink) StepBegan(context.Context, StepLifecycleSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active {
		return errors.New("overlapping external Engine Step")
	}
	s.active = true
	return nil
}

func (s *nonOverlappingStepLifecycleSink) StepEnded(context.Context, StepLifecycleSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.active {
		return errors.New("external Engine Step ended while idle")
	}
	s.active = false
	return nil
}

func TestWorktreeTransitionUsesReviewerFollowUpStepAtToolBoundary(t *testing.T) {
	toolCall := llm.ToolCall{
		ID:    "reviewer-follow-up-tool",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	mainClient := &fakeClient{responses: []llm.Response{
		finalTextResponse("initial"),
		commentaryResponse("applying review", toolCall),
		finalTextResponse("review applied"),
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value(`{"suggestions":["apply correction"]}`),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	toolStarted := make(chan struct{})
	releaseTool := make(chan struct{})
	stepLifecycle := &nonOverlappingStepLifecycleSink{}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		mainClient,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID: toolspec.ToolExecCommand,
			Handler: blockingTool{
				name:    toolspec.ToolExecCommand,
				started: toolStarted,
				release: releaseTool,
			},
		}),
		Config{
			Model:         "gpt-5",
			StepLifecycle: stepLifecycle,
			Reviewer: ReviewerConfig{
				Frequency:     "all",
				Model:         "gpt-5",
				ThinkingLevel: "low",
				Client:        reviewerClient,
			},
		},
	)
	var releaseToolOnce sync.Once
	releaseHeldTool := func() { releaseToolOnce.Do(func() { close(releaseTool) }) }
	t.Cleanup(func() {
		releaseHeldTool()
		waitEngineLifecycleTasks(t, engine)
	})

	if _, err := engine.SubmitUserMessage(t.Context(), "start Reviewer follow-up"); err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	select {
	case <-toolStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Reviewer follow-up tool")
	}
	if !engine.ReviewerRunning() {
		t.Fatal("Reviewer activity completed before its follow-up tool boundary")
	}

	transitionRan := false
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- engine.RunWorktreeTransition(t.Context(), func() error {
			transitionRan = true
			return nil
		})
	}()
	waitForAcceptedRuntimeOperationCount(t, engine, 1)
	select {
	case err := <-transitionDone:
		t.Fatalf("Worktree transition completed before the Reviewer follow-up tool boundary: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseHeldTool()
	select {
	case err := <-transitionDone:
		if err != nil {
			t.Fatalf("Worktree transition failed at Reviewer follow-up boundary: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Worktree transition did not run at Reviewer follow-up boundary")
	}
	if !transitionRan {
		t.Fatal("Worktree transition callback did not run")
	}
	waitEngineLifecycleTasks(t, engine)
	if engine.ReviewerRunning() {
		t.Fatal("Reviewer activity remained active after its follow-up completed")
	}
}

func TestWorktreeDeleteTransitionRejectsReviewerStartedBeforeGrantedBoundary(t *testing.T) {
	mainClient, mainStarted, releaseMain := newGatedHookClient(
		finalTextResponse("done"),
		llm.Response{},
	)
	engine, reviewerStarted := newHeldReviewerWorktreeEngine(t, mainClient)
	t.Cleanup(releaseMain)

	submitDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(t.Context(), "start Reviewer at boundary")
		submitDone <- err
	}()
	select {
	case <-mainStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for active Agent provider request")
	}

	transitionRan := false
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- engine.RunWorktreeDeleteTransition(t.Context(), func() error {
			transitionRan = true
			return nil
		})
	}()
	waitForAcceptedRuntimeOperationCount(t, engine, 1)
	releaseMain()

	select {
	case <-reviewerStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Reviewer provider request")
	}
	select {
	case err := <-transitionDone:
		if !errors.Is(err, ErrReviewerRunning) {
			t.Fatalf("RunWorktreeDeleteTransition error = %v, want ErrReviewerRunning", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Worktree deletion did not reject Reviewer activity at the granted Step Boundary")
	}
	if transitionRan {
		t.Fatal("Worktree deletion ran after Reviewer activity started")
	}
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
}

func TestRuntimeOperationFIFOPropagatesTypedFailure(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)
	wantErr := errors.New("typed failure")

	result, err := submitRuntimeOperation(fifo, func(context.Context) (int, error) {
		return 73, wantErr
	}).Await(t.Context())
	if result != 73 || !errors.Is(err, wantErr) {
		t.Fatalf("typed Runtime result = %d, %v; want 73, %v", result, err, wantErr)
	}
}

func TestRuntimeOperationFIFOIdleDrainWaitsForAcceptedWork(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)
	started := make(chan struct{})
	release := make(chan struct{})
	submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
		close(started)
		<-release
		return struct{}{}, nil
	})
	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for accepted Runtime operation")
	}

	drained := make(chan error, 1)
	go func() {
		drained <- fifo.Drain(t.Context())
	}()
	select {
	case err := <-drained:
		t.Fatalf("Idle drain completed before accepted work: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-drained; err != nil {
		t.Fatalf("Idle drain: %v", err)
	}
}

func TestRuntimeOperationFIFOPauseCutoffDetectsAcceptanceAfterIdleDrain(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)

	drainedThrough, err := fifo.drainThrough(t.Context())
	if err != nil {
		t.Fatalf("idle drain: %v", err)
	}
	applied := submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
		return struct{}{}, nil
	})
	if _, err := applied.Await(t.Context()); err != nil {
		t.Fatalf("accepted Runtime operation: %v", err)
	}
	pausedThrough, err := fifo.pauseThrough(t.Context())
	if err != nil {
		t.Fatalf("pause after acceptance: %v", err)
	}
	if pausedThrough <= drainedThrough {
		t.Fatalf(
			"pause cutoff = %d, want newer than preceding drain cutoff %d",
			pausedThrough,
			drainedThrough,
		)
	}
}

func TestRuntimeOperationFIFOCanceledPauseDoesNotSuspendLaterAcceptedWork(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
		close(firstStarted)
		<-releaseFirst
		return struct{}{}, nil
	})
	<-firstStarted

	secondStarted := make(chan struct{})
	submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
		close(secondStarted)
		return struct{}{}, nil
	})

	pauseCtx, cancelPause := context.WithCancel(t.Context())
	pauseDone := make(chan error, 1)
	go func() { pauseDone <- fifo.Pause(pauseCtx) }()
	testsetup.RequireUntil(t, time.Now().Add(runtimeTestSynchronizationTimeout), time.Millisecond, func() bool {
		fifo.mu.Lock()
		defer fifo.mu.Unlock()
		return fifo.pauseDone != nil
	}, "timed out waiting for Runtime FIFO pause request")
	cancelPause()
	if err := <-pauseDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pause error = %v, want context canceled", err)
	}

	drained := make(chan error, 1)
	go func() { drained <- fifo.Drain(t.Context()) }()
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("accepted Runtime operation remained suspended after pause cancellation")
	}
	if err := <-drained; err != nil {
		t.Fatalf("drain after pause cancellation: %v", err)
	}
}

func TestRuntimeOperationFIFOCloseCancelsCurrentAndRejectsQueuedAndNewWork(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	currentStarted := make(chan struct{})
	current := submitRuntimeOperation(fifo, func(ctx context.Context) (string, error) {
		close(currentStarted)
		<-ctx.Done()
		return "current", context.Cause(ctx)
	})
	select {
	case <-currentStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for current Runtime operation")
	}
	queuedStarted := make(chan struct{})
	queued := submitRuntimeOperation(fifo, func(context.Context) (string, error) {
		close(queuedStarted)
		return "queued", nil
	})

	closed := make(chan struct{})
	go func() {
		fifo.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Runtime FIFO close did not join current work")
	}
	currentResult, currentErr := current.Await(t.Context())
	if currentResult != "current" || !errors.Is(currentErr, context.Canceled) {
		t.Fatalf("current Runtime result = %q, %v; want current, canceled", currentResult, currentErr)
	}
	queuedResult, queuedErr := queued.Await(t.Context())
	if queuedResult != "" || !errors.Is(queuedErr, ErrEngineClosed) {
		t.Fatalf("queued Runtime result = %q, %v; want zero, Runtime closed", queuedResult, queuedErr)
	}
	select {
	case <-queuedStarted:
		t.Fatal("queued Runtime operation started during close")
	default:
	}
	newResult, newErr := submitRuntimeOperation(fifo, func(context.Context) (string, error) {
		return "new", nil
	}).Await(t.Context())
	if newResult != "" || !errors.Is(newErr, ErrEngineClosed) {
		t.Fatalf("new Runtime result = %q, %v; want zero, Runtime closed", newResult, newErr)
	}
}

func TestRuntimeOperationFIFOCloseWhileIdle(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	fifo.Close()
}

func TestRuntimeDeferredCallerCancellationDoesNotCancelAcceptedWork(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)
	started := make(chan struct{})
	release := make(chan struct{})
	deferred := submitRuntimeOperation(fifo, func(context.Context) (string, error) {
		close(started)
		<-release
		return "applied", nil
	})
	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for accepted Runtime operation")
	}

	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if result, err := deferred.Await(canceled); result != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait = %q, %v; want zero, canceled", result, err)
	}
	close(release)
	if result, err := deferred.Await(t.Context()); result != "applied" || err != nil {
		t.Fatalf("accepted Runtime operation = %q, %v; want applied, nil", result, err)
	}
}

func TestRuntimeOperationFIFORejectsTheTenThousandthPendingOperation(t *testing.T) {
	fifo := newRuntimeOperationFIFO()
	t.Cleanup(fifo.Close)
	if err := fifo.Pause(t.Context()); err != nil {
		t.Fatalf("pause Runtime FIFO: %v", err)
	}
	for range maxPendingRuntimeOperations {
		submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
			return struct{}{}, nil
		})
	}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("10,000th pending Runtime operation did not panic")
		}
	}()
	submitRuntimeOperation(fifo, func(context.Context) (struct{}, error) {
		return struct{}{}, nil
	})
}

func TestEngineDefersOrdinaryRuntimeMutationUntilProtectedStepFinishes(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	stepStarted := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- engine.RunWhenIdle(t.Context(), ActiveKindUserTurn, func() error {
			close(stepStarted)
			<-releaseStep
			return nil
		})
	}()
	select {
	case <-stepStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for protected Agent Step")
	}

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- engine.AppendCommittedEntry(t.Context(), "system", "after boundary")
	}()
	select {
	case err := <-mutationDone:
		t.Fatalf("ordinary Runtime mutation completed during protected Step: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("finish protected Agent Step: %v", err)
	}
	if err := <-mutationDone; err != nil {
		t.Fatalf("apply Runtime mutation at boundary: %v", err)
	}
	entries := engine.ChatSnapshot().Entries
	if len(entries) != 1 || entries[0].Text != "after boundary" {
		t.Fatalf("post-boundary transcript = %+v", entries)
	}
}

func TestActiveSessionRuntimeFIFOsAreIndependent(t *testing.T) {
	first := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	second := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5", SupportedThinkingValues: []string{"low"}},
	)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	blocked := submitEngineRuntimeOperation(first, func(context.Context) (struct{}, error) {
		close(firstStarted)
		<-releaseFirst
		return struct{}{}, nil
	})
	select {
	case <-firstStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for first Session Runtime operation")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- second.SetThinkingLevel(t.Context(), "low")
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second Session Runtime mutation: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("first Session Runtime blocked the second Session FIFO")
	}
	close(releaseFirst)
	if _, err := blocked.Await(t.Context()); err != nil {
		t.Fatalf("release first Session Runtime operation: %v", err)
	}
}

func TestEngineAppliesStreamingStateAndItsEventAsOneOrderedMutation(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	stepStarted := make(chan struct{})
	releaseStep := make(chan struct{})
	stepDone := make(chan error, 1)
	go func() {
		stepDone <- engine.RunWhenIdle(t.Context(), ActiveKindUserTurn, func() error {
			close(stepStarted)
			<-releaseStep
			return nil
		})
	}()
	select {
	case <-stepStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for protected Agent Step")
	}

	mutationDone := make(chan struct{})
	go func() {
		engine.SetStreamingError("ordered error")
		close(mutationDone)
	}()
	select {
	case <-mutationDone:
		t.Fatal("streaming mutation completed during protected Step")
	case <-time.After(25 * time.Millisecond):
	}
	if got := engine.ChatSnapshot().StreamingError; got != "" {
		t.Fatalf("streaming error applied before its ordered event: %q", got)
	}

	close(releaseStep)
	if err := <-stepDone; err != nil {
		t.Fatalf("finish protected Agent Step: %v", err)
	}
	select {
	case <-mutationDone:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for streaming mutation")
	}
	if got := engine.ChatSnapshot().StreamingError; got != "ordered error" {
		t.Fatalf("streaming error = %q, want ordered error", got)
	}
}

func TestForegroundShellReleasesRuntimeFIFOAfterScheduling(t *testing.T) {
	handler := &heldRuntimeShell{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: handler,
		}),
		Config{Model: "gpt-5", SupportedThinkingValues: []string{"low"}},
	)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := submitEngineRuntimeOperation(engine, func(context.Context) (struct{}, error) {
		close(firstStarted)
		<-releaseFirst
		return struct{}{}, nil
	})
	select {
	case <-firstStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for preceding Runtime mutation")
	}

	shellDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserShellCommand(t.Context(), "pwd")
		shellDone <- err
	}()
	select {
	case <-handler.started:
		t.Fatal("foreground shell started before the preceding Runtime mutation")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if _, err := first.Await(t.Context()); err != nil {
		t.Fatalf("complete preceding Runtime mutation: %v", err)
	}
	select {
	case <-handler.started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for foreground shell")
	}

	settingDone := make(chan error, 1)
	go func() {
		settingDone <- engine.SetThinkingLevel(t.Context(), "low")
	}()
	select {
	case err := <-settingDone:
		if err != nil {
			t.Fatalf("apply setting while shell is held: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("foreground shell retained the Runtime FIFO head")
	}

	close(handler.release)
	if err := <-shellDone; err != nil {
		t.Fatalf("foreground shell: %v", err)
	}
}

func TestForegroundShellTerminalEffectReentersAtTheCurrentRuntimeTail(t *testing.T) {
	handler := &heldRuntimeShell{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: handler,
		}),
		Config{Model: "gpt-5"},
	)
	shellDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserShellCommand(t.Context(), "pwd")
		shellDone <- err
	}()
	select {
	case <-handler.started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for foreground shell")
	}

	laterStarted := make(chan struct{})
	releaseLater := make(chan struct{})
	later := submitEngineRuntimeOperation(engine, func(context.Context) (struct{}, error) {
		close(laterStarted)
		<-releaseLater
		return struct{}{}, nil
	})
	select {
	case <-laterStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for later Runtime mutation")
	}
	close(handler.release)
	select {
	case <-handler.returned:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("foreground shell process did not exit")
	}
	select {
	case err := <-shellDone:
		t.Fatalf("foreground shell completed before its terminal FIFO effect: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseLater)
	if _, err := later.Await(t.Context()); err != nil {
		t.Fatalf("later Runtime mutation: %v", err)
	}
	select {
	case err := <-shellDone:
		if err != nil {
			t.Fatalf("foreground shell: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("foreground shell terminal FIFO effect did not apply")
	}
}

func TestWorktreeTransitionReleasesRuntimeFIFOAfterScheduling(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{finalTextResponse("done")}}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5", SupportedThinkingValues: []string{"low"}},
	)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	first := submitEngineRuntimeOperation(engine, func(context.Context) (struct{}, error) {
		close(firstStarted)
		<-releaseFirst
		return struct{}{}, nil
	})
	select {
	case <-firstStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for preceding Runtime mutation")
	}

	transitionStarted := make(chan struct{})
	releaseTransition := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- engine.RunWorktreeTransition(t.Context(), func() error {
			close(transitionStarted)
			<-releaseTransition
			return nil
		})
	}()
	select {
	case <-transitionStarted:
		t.Fatal("Worktree transition started before the preceding Runtime mutation")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if _, err := first.Await(t.Context()); err != nil {
		t.Fatalf("complete preceding Runtime mutation: %v", err)
	}
	select {
	case <-transitionStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Worktree transition")
	}

	if _, err := engine.QueueUserMessageForAutoDrain(t.Context(), "accepted while Worktree holds eligibility"); err != nil {
		t.Fatalf("accept Human input while Worktree transition is held: %v", err)
	}
	if err := engine.SetThinkingLevel(t.Context(), "low"); err != nil {
		t.Fatalf("apply setting while Worktree transition is held: %v", err)
	}
	if got := fakeClientCallCount(client); got != 0 {
		t.Fatalf("Human provider work started while Worktree held eligibility: calls=%d", got)
	}
	close(releaseTransition)
	if err := <-transitionDone; err != nil {
		t.Fatalf("Worktree transition: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if got := fakeClientCallCount(client); got != 1 {
		t.Fatalf("Human provider work after Worktree transition: calls=%d, want 1", got)
	}
}

func TestWorktreeTerminalEffectReentersAtTheCurrentRuntimeTail(t *testing.T) {
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	transitionStarted := make(chan struct{})
	releaseTransition := make(chan struct{})
	terminalApplied := make(chan struct{})
	transitionDone := make(chan error, 1)
	go func() {
		transitionDone <- engine.RunWorktreeTransition(t.Context(), func() error {
			close(transitionStarted)
			<-releaseTransition
			return engine.ApplyWorktreeTransitionTerminal(t.Context(), func(context.Context) error {
				close(terminalApplied)
				return nil
			})
		})
	}()
	select {
	case <-transitionStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Worktree transition")
	}

	laterStarted := make(chan struct{})
	releaseLater := make(chan struct{})
	later := submitEngineRuntimeOperation(engine, func(context.Context) (struct{}, error) {
		close(laterStarted)
		<-releaseLater
		return struct{}{}, nil
	})
	select {
	case <-laterStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for later Runtime mutation")
	}
	close(releaseTransition)
	select {
	case <-terminalApplied:
		t.Fatal("Worktree terminal effect bypassed the current Runtime tail")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseLater)
	if _, err := later.Await(t.Context()); err != nil {
		t.Fatalf("later Runtime mutation: %v", err)
	}
	select {
	case err := <-transitionDone:
		if err != nil {
			t.Fatalf("Worktree transition: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("Worktree terminal FIFO effect did not apply")
	}
}

func TestManualCompactionReleasesRuntimeFIFOAfterScheduling(t *testing.T) {
	client := &heldRuntimeCompactionClient{
		fakeCompactionClient: &fakeCompactionClient{
			compactionResponses: []llm.CompactionResponse{{
				OutputItems: []llm.ResponseItem{
					{
						Type:        llm.ResponseItemTypeMessage,
						Role:        textutil.Value(llm.RoleUser),
						MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
						Content:     textutil.Value("summary"),
					},
					{
						Type:             llm.ResponseItemTypeCompaction,
						EncryptedContent: textutil.Value("checkpoint"),
					},
				},
				Usage: llm.Usage{WindowTokens: 200000},
			}},
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(),
		Config{Model: "gpt-5", SupportedThinkingValues: []string{"low"}},
	)
	engine.compactionRuntimeState().SetManualCompactionEligible(true)

	compactionDone := make(chan error, 1)
	go func() {
		compactionDone <- engine.CompactContext(t.Context(), "")
	}()
	select {
	case err := <-compactionDone:
		if err != nil {
			t.Fatalf("schedule manual compaction: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("manual compaction request did not return after scheduling")
	}
	select {
	case <-client.started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for manual compaction")
	}
	if err := engine.SetThinkingLevel(t.Context(), "low"); err != nil {
		t.Fatalf("apply setting while manual compaction is held: %v", err)
	}
	close(client.release)
}

type heldRuntimeShell struct {
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (h *heldRuntimeShell) Call(ctx context.Context, call tools.Call) (tools.Result, error) {
	close(h.started)
	select {
	case <-h.release:
		if h.returned != nil {
			close(h.returned)
		}
		return tools.Result{
			CallID: call.ID,
			Name:   toolspec.ToolExecCommand,
			Output: []byte(`{"output":"done"}`),
		}, nil
	case <-ctx.Done():
		return tools.Result{}, context.Cause(ctx)
	}
}

type heldRuntimeCompactionClient struct {
	*fakeCompactionClient
	started chan struct{}
	release chan struct{}
}

func (c *heldRuntimeCompactionClient) Compact(
	ctx context.Context,
	request llm.CompactionRequest,
) (llm.CompactionResponse, error) {
	close(c.started)
	select {
	case <-c.release:
		return c.fakeCompactionClient.Compact(ctx, request)
	case <-ctx.Done():
		return llm.CompactionResponse{}, context.Cause(ctx)
	}
}
