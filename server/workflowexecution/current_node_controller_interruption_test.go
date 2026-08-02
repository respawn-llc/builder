package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
)

func TestCurrentNodeControllerInterruptPersistsAfterCallerDeadline(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-interrupt-deadline", "node-agent")
	store := &currentNodeControllerStore{
		interruptStarted: make(chan struct{}),
		interruptRelease: make(chan struct{}),
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap '' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
		t.Fatalf("start current node: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, reference)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- controller.Interrupt(ctx, InterruptSelector{TaskID: reference.TaskID})
	}()
	select {
	case <-store.interruptStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("interrupt did not begin its durable cleanup")
	}
	<-ctx.Done()
	close(store.interruptRelease)
	if err := <-result; err != nil {
		t.Fatalf("interrupt current node after caller deadline: %v", err)
	}
	if interruption, interrupted := store.interruption(reference); !interrupted || interruption.reason != workflow.CurrentNodeInterruptionReasonUserInterrupt {
		t.Fatalf("interruption = %+v, interrupted = %t, want durable user interruption", interruption, interrupted)
	}
	if hasLiveCurrentNode(controller.Snapshot(), reference) {
		t.Fatalf("interrupted current node remains live: %+v", controller.Snapshot())
	}
}

func TestCurrentNodeControllerTaskInterruptFenceRejectsLifecycleMutationsUntilRetirement(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	source := currentNodeReferenceForControllerTest(t, "task-interrupt-fence", "node-source")
	other := currentNodeReferenceForControllerTest(t, "task-interrupt-fence", "node-other")
	approval := workflow.PendingApproval{ID: workflow.NewApprovalID(), Source: source}
	store := &currentNodeControllerStore{pendingApproval: approval}
	finalized := make(chan struct{})
	releaseFinalization := make(chan struct{})
	var finalizedOnce sync.Once
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			finalizedOnce.Do(func() {
				close(finalized)
			})
			<-releaseFinalization
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		select {
		case <-releaseFinalization:
		default:
			close(releaseFinalization)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, source); err != nil {
		t.Fatalf("start source: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, source)
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- controller.Interrupt(context.Background(), InterruptSelector{TaskID: source.TaskID})
	}()
	select {
	case <-finalized:
	case <-time.After(3 * time.Second):
		t.Fatal("Task Interrupt did not reach exact-scope retirement")
	}

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, other); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("start during Task Interrupt = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}
	if _, err := controller.ApplyPendingApproval(context.Background(), approval.ID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("approval during Task Interrupt = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}
	if err := controller.EnsureTaskQuiescent(source.TaskID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("quiescence during Task Interrupt = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}

	close(releaseFinalization)
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Task Interrupt after finalization: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Task Interrupt fence did not clear after retirement")
	}
}

func TestCurrentNodeControllerTaskInterruptFencesFinalizingSiblingBeforeReturn(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	running := currentNodeReferenceForControllerTest(t, "task-finalizing-sibling", "node-running")
	finalizing := currentNodeReferenceForControllerTest(t, "task-finalizing-sibling", "node-finalizing")
	successor := currentNodeReferenceForControllerTest(t, "task-finalizing-sibling", "node-successor")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{
			{Reference: running, Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted}},
			{Reference: finalizing, Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted}},
		},
		completion: workflowstore.CurrentNodeCompletionResult{
			AutomaticIntents: []workflowstore.CurrentNodeAutomaticIntent{{CurrentNode: successor, NodeKind: workflow.NodeKindAgent}},
		},
		interruptStarted: make(chan struct{}),
		interruptRelease: make(chan struct{}),
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &runningAndFinalizingScriptRunner{
		authority:           authority,
		shellPath:           shellPath,
		running:             running,
		finalizing:          finalizing,
		finalizerEntered:    make(chan struct{}),
		releaseFinalizer:    make(chan struct{}),
		finalizerCompletion: make(chan error, 1),
		successorStarted:    make(chan struct{}, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	var releaseFinalizerOnce sync.Once
	releaseFinalizer := func() {
		releaseFinalizerOnce.Do(func() {
			close(runner.releaseFinalizer)
		})
	}
	var releaseInterruptOnce sync.Once
	releaseInterrupt := func() {
		releaseInterruptOnce.Do(func() {
			close(store.interruptRelease)
		})
	}
	t.Cleanup(func() {
		releaseFinalizer()
		releaseInterrupt()
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if _, err := controller.ResumeTask(context.Background(), running.TaskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	select {
	case <-runner.finalizerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("sibling Script did not enter completion finalization")
	}
	waitForRunningCurrentNode(t, authority, running)

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- controller.Interrupt(context.Background(), InterruptSelector{TaskID: running.TaskID})
	}()
	select {
	case <-store.interruptStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Task Interrupt did not begin durable cleanup")
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return !hasLiveCurrentNode(controller.Snapshot(), running)
	}, "running sibling did not retire while interrupt persistence was blocked")

	releaseFinalizer()
	releaseInterrupt()
	select {
	case completionErr := <-runner.finalizerCompletion:
		if !errors.Is(completionErr, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("finalizing sibling completion error = %v, want %v", completionErr, ErrTaskExecutionNotQuiescent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("finalizing sibling completion did not resolve through the Task fence")
	}
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Task Interrupt: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Task Interrupt did not join the finalizing sibling")
	}
	if calls := store.completionCount(); calls != 0 {
		t.Fatalf("finalizing sibling durable completions = %d, want 0", calls)
	}
	select {
	case <-runner.successorStarted:
		t.Fatal("finalizing sibling released a successor after Task Interrupt")
	default:
	}
}

func TestCurrentNodeControllerTaskInterruptUserReasonWinsFinalizingScopeFailure(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	running := currentNodeReferenceForControllerTest(t, "task-finalizing-failure", "node-running")
	finalizing := currentNodeReferenceForControllerTest(t, "task-finalizing-failure", "node-finalizing")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{
			{Reference: running, Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted}},
			{Reference: finalizing, Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted}},
		},
		interruptStarted: make(chan struct{}),
		interruptRelease: make(chan struct{}),
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &runningAndFinalizingScriptRunner{
		authority:           authority,
		shellPath:           shellPath,
		running:             running,
		finalizing:          finalizing,
		finalizerEntered:    make(chan struct{}),
		releaseFinalizer:    make(chan struct{}),
		finalizerCompletion: make(chan error, 1),
		successorStarted:    make(chan struct{}, 1),
		finalize: func(ctx context.Context, scope sessionruntime.ExecutionScope, controller *CurrentNodeController) error {
			return controller.FailCurrentNodeScope(ctx, scope.ID(), "workflow_script_failed", errors.New("script failed"))
		},
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	var releaseFinalizerOnce sync.Once
	releaseFinalizer := func() {
		releaseFinalizerOnce.Do(func() {
			close(runner.releaseFinalizer)
		})
	}
	var releaseInterruptOnce sync.Once
	releaseInterrupt := func() {
		releaseInterruptOnce.Do(func() {
			close(store.interruptRelease)
		})
	}
	t.Cleanup(func() {
		releaseFinalizer()
		releaseInterrupt()
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if _, err := controller.ResumeTask(context.Background(), running.TaskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	select {
	case <-runner.finalizerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("sibling Script did not enter failure finalization")
	}
	waitForRunningCurrentNode(t, authority, running)

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- controller.Interrupt(context.Background(), InterruptSelector{TaskID: running.TaskID})
	}()
	select {
	case <-store.interruptStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("Task Interrupt did not establish its fence before persistence")
	}
	releaseFinalizer()
	releaseInterrupt()
	select {
	case finalizerErr := <-runner.finalizerCompletion:
		if !errors.Is(finalizerErr, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("finalizing scope failure error = %v, want %v", finalizerErr, ErrTaskExecutionNotQuiescent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("finalizing scope failure did not resolve through the Task fence")
	}
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Task Interrupt: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Task Interrupt did not join the failing finalizer")
	}
	interruption, interrupted := store.interruption(finalizing)
	if !interrupted || interruption.reason != workflow.CurrentNodeInterruptionReasonUserInterrupt {
		t.Fatalf("finalizing scope interruption = %+v, interrupted = %t, want user interrupt", interruption, interrupted)
	}
	if writes := store.interruptionCount(finalizing); writes != 1 {
		t.Fatalf("finalizing scope interruption writes = %d, want 1", writes)
	}
}

func TestCurrentNodeControllerScopeFailurePersistsDespiteUnrelatedWorkerError(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-scope-failure-worker-error", "node-script")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		controller.mu.Lock()
		controller.workerErr = nil
		controller.mu.Unlock()
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-test",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: reference,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	t.Cleanup(lease.Cancel)
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("Current Node key: %v", err)
	}
	controller.mu.Lock()
	controller.live[lease.ScopeID()] = currentNodeLiveScope{reference: reference, lease: lease}
	controller.liveByNode[key] = lease.ScopeID()
	controller.workerErr = errors.New("unrelated admission persistence failed")
	controller.mu.Unlock()

	if err := controller.FailCurrentNodeScope(
		context.Background(),
		lease.ScopeID(),
		"workflow_script_failed",
		errors.New("script failed"),
	); err != nil {
		t.Fatalf("FailCurrentNodeScope: %v", err)
	}
	interruption, interrupted := store.interruption(reference)
	if !interrupted || interruption.reason != "workflow_script_failed" {
		t.Fatalf("scope failure interruption = %+v, interrupted = %t", interruption, interrupted)
	}
}

func TestCurrentNodeControllerRecoveryOnlyMarksAdmittedCurrentNodesInterrupted(t *testing.T) {
	store := &currentNodeControllerStore{recovered: []workflow.CurrentNodeReference{
		currentNodeReferenceForControllerTest(t, "task-recovered-1", "node-1"),
		currentNodeReferenceForControllerTest(t, "task-recovered-2", "node-2"),
		currentNodeReferenceForControllerTest(t, "task-recovered-3", "node-3"),
	}}
	attention := &currentNodeAttentionRecorder{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	controller := newCurrentNodeControllerWithAttentionForTest(t, store, runner, authority, 1, attention)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	recovered, err := controller.Recover(context.Background())
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 3 {
		t.Fatalf("recovered markers = %d, want 3", recovered)
	}
	if runner.starts() != 0 {
		t.Fatalf("recovery started %d current nodes, want no automatic start", runner.starts())
	}
	if attention.pendingCount() != 3 {
		t.Fatalf("recovery attention notifications = %d, want 3", attention.pendingCount())
	}
}

func TestCurrentNodeControllerTaskInterruptDrainsReservationOnlyAlongsideLiveScope(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	live := currentNodeReferenceForControllerTest(t, "task-reservation-interrupt", "node-live")
	reserved := currentNodeReferenceForControllerTest(t, "task-reservation-interrupt", "node-successor")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, live); err != nil {
		t.Fatalf("start live current node: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, live)
	reservedKey, err := reserved.Key()
	if err != nil {
		t.Fatalf("reserved key: %v", err)
	}
	controller.mu.Lock()
	controller.agentCapacityActive = 1
	controller.automaticReservations[reservedKey] = currentNodeQueuedStart{
		reference:         reserved,
		launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
		policy:            currentNodeAdmissionAutomaticAgent,
		agentCapacityLease: &currentNodeAgentCapacityLease{
			owner: currentNodeAgentCapacityReservation,
		},
	}
	controller.mu.Unlock()

	if err := controller.Interrupt(context.Background(), InterruptSelector{TaskID: live.TaskID}); err != nil {
		t.Fatalf("task interrupt: %v", err)
	}
	if _, interrupted := store.interruption(reserved); !interrupted {
		t.Fatal("task interrupt did not persist the drained reservation interruption")
	}
	if err := controller.EnsureTaskQuiescent(live.TaskID); err != nil {
		t.Fatalf("task remains non-quiescent after interrupt: %v", err)
	}
	if hasAutomaticCurrentNodeIntent(controller.Snapshot(), reserved) {
		t.Fatalf("drained reservation remains in snapshot: %+v", controller.Snapshot())
	}
	controller.mu.Lock()
	if controller.agentCapacityActive != 0 {
		controller.mu.Unlock()
		t.Fatalf("Agent capacity after draining reservation = %d, want 0", controller.agentCapacityActive)
	}
	controller.mu.Unlock()
}

func TestCurrentNodeControllerInterruptingScriptDoesNotReleaseAgentCapacity(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	occupyingAgent := currentNodeReferenceForControllerTest(t, "task-interrupted-script-agent", "node-occupying-agent")
	script := currentNodeReferenceForControllerTest(t, "task-interrupted-script", "node-script")
	queuedAgent := currentNodeReferenceForControllerTest(t, "task-interrupted-script-queued-agent", "node-queued-agent")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 3),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: occupyingAgent,
		NodeKind:    workflow.NodeKindAgent,
	}})
	select {
	case started := <-runner.started:
		if !started.Equal(occupyingAgent) {
			t.Fatalf("occupying Agent start = %v, want %v", started, occupyingAgent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("occupying Agent did not start")
	}
	waitForRunningCurrentNode(t, authority, occupyingAgent)
	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{
		{CurrentNode: script, NodeKind: workflow.NodeKindScript},
		{CurrentNode: queuedAgent, NodeKind: workflow.NodeKindAgent},
	})
	select {
	case started := <-runner.started:
		if !started.Equal(script) {
			t.Fatalf("Script start = %v, want %v", started, script)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Script did not start while Agent capacity was occupied")
	}
	waitForRunningCurrentNode(t, authority, script)
	if !hasAutomaticCurrentNodeIntent(controller.Snapshot(), queuedAgent) {
		t.Fatalf("queued Agent = %+v, want queued while occupying Agent is live", controller.Snapshot().AutomaticIntents)
	}

	if err := controller.Interrupt(context.Background(), InterruptSelector{TaskID: script.TaskID}); err != nil {
		t.Fatalf("interrupt Script: %v", err)
	}
	if hasLiveCurrentNode(controller.Snapshot(), script) {
		t.Fatalf("interrupted Script remains live: %+v", controller.Snapshot().LiveScopes)
	}
	if !hasAutomaticCurrentNodeIntent(controller.Snapshot(), queuedAgent) {
		t.Fatalf("queued Agent = %+v, want queued after Script interruption", controller.Snapshot().AutomaticIntents)
	}
	occupyingHandle, live := authority.ExecutionByScope(singleLiveScope(t, controller, occupyingAgent))
	if !live {
		t.Fatal("occupying Agent is not live")
	}
	if err := occupyingHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop occupying Agent: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(queuedAgent) {
			t.Fatalf("queued Agent start = %v, want %v", started, queuedAgent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued Agent did not start after occupying Agent stopped")
	}
}

func TestCurrentNodeControllerTaskInterruptFailsWhilePreparationGateActive(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	running := currentNodeReferenceForControllerTest(t, "task-queued-gate-interrupt", "node-running")
	queued := currentNodeReferenceForControllerTest(t, "task-queued-gate-interrupt", "node-queued")
	store := &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference:  queued,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		}},
		interruptStarted: make(chan struct{}),
		interruptRelease: make(chan struct{}),
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &runningAndQueuedGateRunner{
		authority:        authority,
		shellPath:        shellPath,
		running:          running,
		runningStarted:   make(chan struct{}),
		queuedRegistered: make(chan struct{}),
		returnQueued:     make(chan struct{}),
	}
	var releaseQueuedOnce sync.Once
	releaseQueued := func() {
		releaseQueuedOnce.Do(func() {
			close(runner.returnQueued)
		})
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		releaseQueued()
		select {
		case <-store.interruptRelease:
		default:
			close(store.interruptRelease)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, running); err != nil {
		t.Fatalf("start running current node: %v", err)
	}
	select {
	case <-runner.runningStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("running scope did not start")
	}
	waitForRunningCurrentNode(t, authority, running)
	if _, err := controller.ResumeTask(context.Background(), running.TaskID); err != nil {
		t.Fatalf("ResumeTask queued sibling: %v", err)
	}
	select {
	case <-runner.queuedRegistered:
	case <-time.After(3 * time.Second):
		t.Fatal("queued sibling did not register its Authority scope")
	}

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- controller.Interrupt(context.Background(), InterruptSelector{TaskID: running.TaskID})
	}()
	select {
	case err := <-interruptDone:
		if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("Task Interrupt while preparation gate active = %v, want %v", err, ErrTaskExecutionNotQuiescent)
		}
	case <-time.After(time.Second):
		t.Fatal("Task Interrupt waited for active preparation gate")
	}
	select {
	case <-store.interruptStarted:
		t.Fatal("Task Interrupt persisted an interruption despite the active preparation gate")
	default:
	}
	if !hasLiveCurrentNode(controller.Snapshot(), running) {
		t.Fatalf("running current node changed after rejected Task Interrupt: %+v", controller.Snapshot())
	}
	releaseQueued()
	for _, reference := range []workflow.CurrentNodeReference{running, queued} {
		interruption, interrupted := store.interruption(reference)
		if interrupted {
			t.Fatalf("current node %v interruption = %+v after rejected Task Interrupt", reference, interruption)
		}
	}
}

func TestCurrentNodeControllerTaskInterruptFailsWhileBlockedPreparationContinues(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	live := currentNodeReferenceForControllerTest(t, "task-blocked-preparation-interrupt", "live")
	blocked := currentNodeReferenceForControllerTest(t, "task-blocked-preparation-interrupt", "blocked")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &liveAndBlockedPreparationRunner{
		authority:   authority,
		shellPath:   shellPath,
		live:        live,
		liveStarted: make(chan struct{}),
		entered:     make(chan struct{}),
		released:    make(chan struct{}),
		finished:    make(chan struct{}),
		canceled:    make(chan struct{}),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		select {
		case <-runner.released:
		default:
			close(runner.released)
		}
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, live); err != nil {
		t.Fatalf("start live current node: %v", err)
	}
	select {
	case <-runner.liveStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("live current node did not start")
	}
	waitForRunningCurrentNode(t, authority, live)
	controller.enqueueStarts([]currentNodeQueuedStart{{
		reference:         blocked,
		launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
		policy:            currentNodeAdmissionExplicitOverride,
	}})
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked preparation did not start")
	}

	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- controller.Interrupt(context.Background(), InterruptSelector{TaskID: live.TaskID})
	}()
	select {
	case err := <-interruptDone:
		if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("Task Interrupt while blocked preparation = %v, want %v", err, ErrTaskExecutionNotQuiescent)
		}
	case <-time.After(time.Second):
		t.Fatal("Task Interrupt waited for blocked preparation gate")
	}
	select {
	case <-runner.canceled:
		t.Fatal("Task Interrupt canceled preparation")
	default:
	}
	if !hasLiveCurrentNode(controller.Snapshot(), live) {
		t.Fatalf("live current node changed after rejected Task Interrupt: %+v", controller.Snapshot())
	}
	close(runner.released)
	select {
	case <-runner.finished:
	case <-time.After(time.Second):
		t.Fatal("blocked preparation did not continue to completion")
	}
}

func TestCurrentNodeControllerSessionInterruptIgnoresUnrelatedPreparationGate(t *testing.T) {
	fixture := newCurrentNodeQuestionFixture(t)
	live := currentNodeReferenceForControllerTest(t, "task-session-interrupt-preparation-gate", "live")
	blocked := currentNodeReferenceForControllerTest(t, "task-session-interrupt-preparation-gate", "blocked")
	releaseAgent := make(chan struct{})
	agent := fixture.startAgent(t, live, func(ctx context.Context, _ sessionruntime.ExecutionScope) error {
		select {
		case <-releaseAgent:
			return nil
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	})
	t.Cleanup(func() {
		select {
		case <-releaseAgent:
		default:
			close(releaseAgent)
		}
	})
	preparation := &interruptiblePreparationRunner{
		entered:  make(chan struct{}),
		canceled: make(chan struct{}),
		released: make(chan struct{}),
	}
	fixture.controller.runner = preparation
	fixture.controller.enqueueStarts([]currentNodeQueuedStart{{
		reference:         blocked,
		launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
		policy:            currentNodeAdmissionExplicitOverride,
	}})
	select {
	case <-preparation.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked preparation did not start")
	}
	waitForRunningCurrentNode(t, fixture.authority, live)

	interruptDone := make(chan error, 1)
	go func() {
		sessionID := agent.sessionID
		interruptDone <- fixture.controller.Interrupt(context.Background(), InterruptSelector{
			TaskID:    live.TaskID,
			SessionID: &sessionID,
		})
	}()
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Session Interrupt with unrelated preparation gate: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Session Interrupt waited for unrelated preparation gate")
	}
	select {
	case <-preparation.canceled:
		t.Fatal("Session Interrupt canceled unrelated preparation")
	default:
	}
	if interruption, interrupted := fixture.store.interruption(blocked); interrupted {
		t.Fatalf("unrelated preparing Current Node interruption = %+v", interruption)
	}
	if interruption, interrupted := fixture.store.interruption(live); !interrupted ||
		interruption.reason != workflow.CurrentNodeInterruptionReasonUserInterrupt {
		t.Fatalf("selected Session interruption = %+v, interrupted = %t, want user interruption", interruption, interrupted)
	}
	close(preparation.released)
}

func TestCurrentNodeControllerReservationDoesNotAuthorizeTaskInterrupt(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-reservation-no-live", "node-agent")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	key, err := reference.Key()
	if err != nil {
		t.Fatalf("reference key: %v", err)
	}
	controller.mu.Lock()
	controller.automaticReservations[key] = currentNodeQueuedStart{
		reference:         reference,
		launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
		policy:            currentNodeAdmissionAutomaticAgent,
	}
	controller.mu.Unlock()

	if err := controller.Interrupt(context.Background(), InterruptSelector{TaskID: reference.TaskID}); !errors.Is(err, ErrNoInterruptibleExecution) {
		t.Fatalf("reservation-only task interrupt error = %v, want %v", err, ErrNoInterruptibleExecution)
	}
	if !hasAutomaticCurrentNodeIntent(controller.Snapshot(), reference) {
		t.Fatalf("reservation was removed despite absent live-scope authorization: %+v", controller.Snapshot())
	}
}

func TestCurrentNodeControllerProtocolViolationCapStopsAndInterruptsLiveScope(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-protocol", "node-agent")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
		t.Fatalf("start current node: %v", err)
	}
	<-runner.started
	waitForRunningCurrentNode(t, authority, reference)
	scopeID := singleLiveScope(t, controller, reference)
	result, err := controller.RecordProtocolViolation(context.Background(), workflowruntime.ViolationRequest{
		ScopeID:  scopeID,
		Kind:     workflowruntime.ViolationKindInvalidCompletion,
		MaxCount: 1,
		Detail:   "invalid completion",
	})
	if err != nil {
		t.Fatalf("record protocol violation: %v", err)
	}
	if !result.Interrupted || result.Count != 1 {
		t.Fatalf("violation result = %+v, want count 1 and interrupted", result)
	}
	interruption, ok := store.interruption(reference)
	if !ok || interruption.reason != reasonProtocolViolationCap {
		t.Fatalf("protocol interruption = %+v, want reason %q", interruption, reasonProtocolViolationCap)
	}
}
