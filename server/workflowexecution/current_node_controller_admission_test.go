package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

func TestCurrentNodeControllerRegistersGateBeforeRunnerAndReleasesLeaseAfterLiveSwap(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-gate", "node-agent")
	outputPath := t.TempDir() + "/started"
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &controlledScriptRunner{
		authority:   authority,
		command:     sessionruntime.ScriptCommand{Path: shellPath, Args: []string{"-c", `printf started > "$1"`, "sh", outputPath}},
		entered:     make(chan struct{}),
		startRunner: make(chan struct{}),
		registered:  make(chan struct{}),
		returnStart: make(chan struct{}),
		handles:     make(chan sessionruntime.ExecutionHandle, 1),
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

	started := make(chan error, 1)
	go func() {
		started <- startCurrentNodeForControllerTest(context.Background(), controller, store, reference)
	}()
	<-runner.entered
	activation, err := controller.joinAgentRunActivation(reference, runtimeids.NewSessionID())
	if err != nil {
		t.Fatalf("join Agent Run activation: %v", err)
	}

	snapshot := controller.Snapshot()
	if len(snapshot.Gates) != 1 || !snapshot.Gates[0].CurrentNode.Equal(reference) {
		t.Fatalf("gate snapshot = %+v, want admitted gate for %v", snapshot.Gates, reference)
	}
	if store.admitCount() != 1 {
		t.Fatalf("admitted current nodes = %d, want 1", store.admitCount())
	}
	close(runner.startRunner)
	<-runner.registered
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("script started before gate-to-live swap: stat error = %v", err)
	}
	if _, live := authority.ExecutionByScope(snapshot.Gates[0].ScopeID); !live {
		t.Fatal("registered script scope is not live while runner is still preparing")
	}

	close(runner.returnStart)
	if err := <-started; err != nil {
		t.Fatalf("start current node: %v", err)
	}
	activationCtx, cancelActivation := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelActivation()
	if _, err := activation.await(activationCtx); err == nil {
		t.Fatal("Agent activation without a Session resource remained pending or succeeded")
	}
	handle := <-runner.handles
	if _, err := handle.Wait(context.Background()); err != nil {
		t.Fatalf("wait script: %v", err)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("script did not start after controller released lease: %v", err)
	}
	snapshot = controller.Snapshot()
	if len(snapshot.Gates) != 0 || len(snapshot.LiveScopes) != 0 {
		t.Fatalf("post-retirement snapshot = %+v, want no gate or live scope", snapshot)
	}
}

func TestCurrentNodeControllerRunnerFailureInterruptsAdmittedCurrentNode(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-failure", "node-agent")
	store := &currentNodeControllerStore{}
	attention := &currentNodeAttentionRecorder{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerWithAttentionForTest(t, store, failingCurrentNodeRunner{cause: errors.New("provider unavailable")}, authority, 1, attention)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
		t.Fatalf("queue current node start: %v", err)
	}
	var interruption currentNodeInterruptionRecord
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		var interrupted bool
		interruption, interrupted = store.interruption(reference)
		return interrupted
	}, "runner failure did not interrupt the admitted current node")
	if interruption.reason != "workflow_runtime_start_failed" {
		t.Fatalf("interruption reason = %q, want workflow_runtime_start_failed", interruption.reason)
	}
	if calls := store.interruptionCount(reference); calls != 1 {
		t.Fatalf("runner failure interruption writes = %d, want 1", calls)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return attention.pendingCount() == 1
	}, "runner failure did not publish interrupted Current Node attention")
}

func TestCurrentNodeControllerRunnerFailureWaitsForTaskLifecycleWriterBeforeStoppingRun(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-runner-failure-writer", "node-agent")
	store := &currentNodeControllerStore{}
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
		t.Fatalf("queue current node start: %v", err)
	}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("runner did not begin")
	}
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("reference key: %v", err)
	}
	controller.mu.Lock()
	run, owned := controller.runs.get(key)
	_, gated := controller.gates[key]
	controller.mu.Unlock()
	if !owned || !gated {
		t.Fatal("runner did not retain its accepted gated Run")
	}

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), reference.TaskID, func(context.Context) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	<-writerEntered
	close(runner.release)

	time.Sleep(50 * time.Millisecond)
	controller.mu.Lock()
	current, stillOwned := controller.runs.get(key)
	_, stillGated := controller.gates[key]
	disposition := run.disposition
	controller.mu.Unlock()
	if !stillOwned || current != run || !stillGated || disposition != currentNodeRunDispositionQueued {
		t.Fatalf(
			"runner failure mutated accepted Run before Task writer: owned=%t same=%t gated=%t disposition=%v",
			stillOwned,
			current == run,
			stillGated,
			disposition,
		)
	}

	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("Task lifecycle writer: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		return run.disposition == currentNodeRunDispositionStopped &&
			run.stop != nil &&
			run.stop.reason == currentNodeRunStopAdmissionFailed
	}, "runner failure did not stop the accepted Run with an admission-failed disposition")
}

func TestCurrentNodeControllerReservationCancellationWaitsForTaskLifecycleWriter(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-reservation-cancellation-writer", "node-agent")
	preparationEntered := make(chan struct{})
	releasePreparation := make(chan struct{})
	cause := errors.New("automatic preparation failed")
	run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
	run.preparation = func(context.Context) error {
		close(preparationEntered)
		<-releasePreparation
		return cause
	}
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("reference key: %v", err)
	}
	var accepted *currentNodeRun
	if err := controller.lifecycle.Run(context.Background(), reference.TaskID, func(context.Context) error {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		if err := controller.queueAutomaticStartLocked(*run); err != nil {
			return err
		}
		accepted, _ = controller.runs.get(key)
		return nil
	}); err != nil {
		t.Fatalf("queue automatic Run: %v", err)
	}
	if accepted == nil {
		t.Fatal("automatic queue did not accept Run")
	}
	select {
	case <-preparationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic admission did not begin preparation")
	}

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), reference.TaskID, func(context.Context) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	<-writerEntered
	close(releasePreparation)

	time.Sleep(50 * time.Millisecond)
	controller.mu.Lock()
	current, owned := controller.runs.get(key)
	_, reserved := controller.automaticReservations[key]
	capacity := controller.agentCapacityActive
	disposition := accepted.disposition
	controller.mu.Unlock()
	if !owned || current != accepted || !reserved || capacity != 1 || disposition != currentNodeRunDispositionQueued {
		t.Fatalf(
			"reservation cancellation crossed Task writer: owned=%t same=%t reserved=%t capacity=%d disposition=%v",
			owned,
			current == accepted,
			reserved,
			capacity,
			disposition,
		)
	}

	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("Task lifecycle writer: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		controller.mu.Lock()
		defer controller.mu.Unlock()
		_, reserved := controller.automaticReservations[key]
		return !reserved &&
			controller.agentCapacityActive == 0 &&
			accepted.disposition == currentNodeRunDispositionStopped &&
			accepted.stop != nil &&
			accepted.stop.reason == currentNodeRunStopAdmissionFailed &&
			errors.Is(accepted.stop.cause, cause)
	}, "reservation cancellation did not end in one typed admission-failed disposition")
}

func TestCurrentNodeControllerWorkerFailureEndsAcceptedRunInOneTypedDisposition(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-worker-failure-disposition", "node-agent")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
	controller.mu.Lock()
	installCurrentNodeRunLockedForTest(controller, run)
	controller.mu.Unlock()
	cause := errors.New("assignment worker failed")

	controller.handleCurrentNodeStartFailures([]currentNodeQueuedStart{*run}, false, cause)

	if run.disposition != currentNodeRunDispositionStopped ||
		run.stop == nil ||
		run.stop.reason != currentNodeRunStopWorkerFailed ||
		!errors.Is(run.stop.cause, cause) {
		t.Fatalf("worker failure Run disposition = %+v, want one typed worker-failed stop", run)
	}
	if calls := store.interruptionCount(reference); calls != 1 {
		t.Fatalf("worker failure interruption writes = %d, want 1", calls)
	}
}

func TestCurrentNodeControllerWorkerFailureWaitsForTaskLifecycleWriterBeforeCleanup(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-worker-failure-writer", "node-agent")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
	controller.mu.Lock()
	key := installCurrentNodeRunLockedForTest(controller, run)
	controller.mu.Unlock()
	cause := errors.New("assignment worker failed")

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), reference.TaskID, func(context.Context) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	<-writerEntered
	cleanupDone := make(chan struct{})
	go func() {
		controller.handleCurrentNodeStartFailures([]currentNodeQueuedStart{*run}, false, cause)
		close(cleanupDone)
	}()

	time.Sleep(50 * time.Millisecond)
	controller.mu.Lock()
	current, owned := controller.runs.get(key)
	disposition := run.disposition
	controller.mu.Unlock()
	if !owned || current != run || disposition != currentNodeRunDispositionQueued {
		t.Fatalf(
			"worker cleanup crossed Task writer: owned=%t same=%t disposition=%v",
			owned,
			current == run,
			disposition,
		)
	}
	if calls := store.interruptionCount(reference); calls != 0 {
		t.Fatalf("worker cleanup wrote interruption before Task writer release: calls=%d", calls)
	}
	select {
	case <-cleanupDone:
		t.Fatal("worker cleanup returned while Task writer was held")
	default:
	}

	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("Task lifecycle writer: %v", err)
	}
	select {
	case <-cleanupDone:
	case <-time.After(3 * time.Second):
		t.Fatal("worker cleanup did not continue after Task writer released")
	}
	if run.disposition != currentNodeRunDispositionStopped ||
		run.stop == nil ||
		run.stop.reason != currentNodeRunStopWorkerFailed ||
		!errors.Is(run.stop.cause, cause) {
		t.Fatalf("worker cleanup Run disposition = %+v, want one typed worker-failed stop", run)
	}
	if calls := store.interruptionCount(reference); calls != 1 {
		t.Fatalf("worker cleanup interruption writes = %d, want 1", calls)
	}
}

func TestCurrentNodeControllerRunnerNoLongerLiveErrorStillStopsAcceptedRun(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-runner-no-longer-live", "node-agent")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, failingCurrentNodeRunner{
		cause: sessionruntime.ErrExecutionNoLongerLive,
	}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: reference,
		NodeKind:    workflow.NodeKindAgent,
	}})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(reference)
		return interrupted
	}, "accepted Run was not stopped after runner returned no-longer-live")
	if err := controller.EnsureTaskQuiescent(reference.TaskID); err != nil {
		t.Fatalf("accepted Run remained owned after runner failure: %v", err)
	}
}

func TestCurrentNodeControllerAdmissionWorkerKeepsStoppedRunUntilExactScopeRetires(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-stopped-exact-scope", "node-agent")
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-test",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: reference,
	})
	if err != nil {
		t.Fatalf("new workflow execution lease: %v", err)
	}
	t.Cleanup(func() {
		lease.Cancel()
		_ = authority.Close(context.Background())
	})
	controller := &CurrentNodeController{
		closed:           true,
		lifecycle:        NewTaskLifecycleCoordinator(),
		runs:             newCurrentNodeRunRegistry(),
		exactScopes:      make(map[runtimeids.ExecutionScopeID]workflow.CurrentNodeReferenceKey),
		gates:            make(map[workflow.CurrentNodeReferenceKey]struct{}),
		admissionWorkers: make(map[workflow.CurrentNodeReferenceKey]struct{}),
	}
	run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
	if err := run.transitionDisposition(currentNodeRunDispositionRunning, nil); err != nil {
		t.Fatalf("transition Run to running: %v", err)
	}
	run.stopOnce(currentNodeRunStopInterrupted, context.Canceled)
	run.executionLease = &lease
	key := installCurrentNodeRunLockedForTest(controller, run)
	controller.exactScopes[lease.ScopeID()] = key
	controller.admissionWorkers[key] = struct{}{}

	controller.finishAdmissionWorker(key, reference.TaskID)

	if current, exists := controller.runs.get(key); !exists || current != run {
		t.Fatal("admission worker removed a stopped Run before its Exact Execution Scope retired")
	}
}

func TestCurrentNodeControllerResumeReturnsBeforeSetupAndStartsParallelBranchesIndependently(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	first := currentNodeReferenceForControllerTest(t, "task-resume-parallel", "node-first")
	second := currentNodeReferenceForControllerTest(t, "task-resume-parallel", "node-second")
	store := &currentNodeControllerStore{interrupted: []workflow.CurrentNode{
		{Reference: first, Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted}},
		{Reference: second, Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted}},
	}}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &parallelExplicitRunner{
		authority:      authority,
		shellPath:      shellPath,
		blocked:        first,
		blockedEntered: make(chan struct{}),
		releaseBlocked: make(chan struct{}),
		siblingStarted: make(chan workflow.CurrentNodeReference, 1),
	}
	var releaseOnce sync.Once
	releaseBlocked := func() {
		releaseOnce.Do(func() {
			close(runner.releaseBlocked)
		})
	}
	attention := &currentNodeAttentionRecorder{}
	controller = newCurrentNodeControllerWithAttentionForTest(t, store, runner, authority, 1, attention)
	t.Cleanup(func() {
		releaseBlocked()
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	resumed, err := controller.ResumeTask(context.Background(), first.TaskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if len(resumed) != 2 {
		t.Fatalf("resumed current nodes = %+v, want both branches", resumed)
	}
	if resolved := attention.resolvedInterruptions(); len(resolved) != 2 {
		t.Fatalf("resolved interruption attention = %+v, want both resumed branches", resolved)
	}
	select {
	case <-runner.blockedEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("first resumed branch did not begin setup")
	}
	select {
	case started := <-runner.siblingStarted:
		if !started.Equal(second) {
			t.Fatalf("independent resumed branch = %v, want %v", started, second)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked first branch prevented sibling admission")
	}
	releaseBlocked()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(first)
		return interrupted
	}, "failed resumed branch was not durably interrupted")
}

func TestCurrentNodeControllerPassesResumePromptDeliveryToRunner(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-resume-prompt-delivery", "node-review")
	store := &currentNodeControllerStore{interrupted: []workflow.CurrentNode{{
		Reference: reference,
		Scheduling: &workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingInterrupted,
		},
	}}}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentEnsurer: &recordingCurrentNodeAssignmentEnsurer{},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	prepared := make(chan struct{}, 1)
	resumed, err := controller.ResumeTaskWithPreparation(context.Background(), reference.TaskID, func(context.Context) error {
		prepared <- struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if len(resumed) != 1 || !resumed[0].Reference.Equal(reference) {
		t.Fatalf("resumed Current Nodes = %+v, want %v", resumed, reference)
	}
	select {
	case <-prepared:
	case <-time.After(3 * time.Second):
		t.Fatal("resume preparation did not run")
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return len(runner.promptDeliveries()) == 1
	}, "resumed Current Node did not reach runner")
	if deliveries := runner.promptDeliveries(); len(deliveries) != 1 ||
		deliveries[0] != workflowruntime.TaskPromptDeliveryResume {
		t.Fatalf("runner prompt deliveries = %+v, want Resume", deliveries)
	}
}

func TestCurrentNodeControllerResumeInterruptsWhenAssignmentEnsureFails(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-resume-assignment-failure", "node-review")
	store := &currentNodeControllerStore{interrupted: []workflow.CurrentNode{{
		Reference: reference,
		Scheduling: &workflow.CurrentNodeScheduling{
			State: workflow.CurrentNodeSchedulingInterrupted,
		},
	}}}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	cause := errors.New("assignment append failed")
	controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AgentConcurrency: 1,
		AssignmentEnsurer: &recordingCurrentNodeAssignmentEnsurer{
			waitErr: cause,
		},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.ResumeTask(context.Background(), reference.TaskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		interruption, interrupted := store.interruption(reference)
		return interrupted &&
			interruption.reason == reasonCurrentNodeRuntimeStartFailed &&
			interruption.detail.Code == string(reasonCurrentNodeRuntimeStartFailed)
	}, "assignment ensure failure did not publish interruption")
	if starts := runner.starts(); starts != 0 {
		t.Fatalf("runner starts = %d, want none after assignment ensure failure", starts)
	}
}

func TestCurrentNodeControllerBoundsExplicitAdmissionSetupWithoutBlockingSiblings(t *testing.T) {
	const branchCount = explicitAdmissionConcurrency + 2
	taskID := workflow.TaskID("task-explicit-admission-bound")
	interrupted := make([]workflow.CurrentNode, 0, branchCount)
	for index := 0; index < branchCount; index++ {
		reference := currentNodeReferenceForControllerTest(t, string(taskID), uuid.NewString())
		interrupted = append(interrupted, workflow.CurrentNode{
			Reference:  reference,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		})
	}
	store := &currentNodeControllerStore{interrupted: interrupted}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &boundedExplicitAdmissionRunner{
		entered: make(chan workflow.CurrentNodeReference, branchCount),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	var releaseOnce sync.Once
	releaseAll := func() {
		releaseOnce.Do(func() {
			close(runner.release)
		})
	}
	t.Cleanup(func() {
		releaseAll()
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	resumed, err := controller.ResumeTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if len(resumed) != branchCount {
		t.Fatalf("resumed Current Nodes = %d, want %d", len(resumed), branchCount)
	}
	for index := 0; index < explicitAdmissionConcurrency; index++ {
		select {
		case <-runner.entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("explicit admission %d did not begin", index+1)
		}
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshot := controller.Snapshot()
		return len(snapshot.Gates) == explicitAdmissionConcurrency &&
			len(snapshot.ExplicitStarts) == branchCount-explicitAdmissionConcurrency
	}, "explicit admission setup did not stop at the bounded capacity")

	runner.release <- struct{}{}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("releasing explicit admission capacity did not admit a queued sibling")
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshot := controller.Snapshot()
		return len(snapshot.Gates) == explicitAdmissionConcurrency &&
			len(snapshot.ExplicitStarts) == branchCount-explicitAdmissionConcurrency-1
	}, "queued explicit sibling did not replace released setup capacity")
	releaseAll()
}

func TestCurrentNodeControllerReservesAutomaticCapacityBeforeLaunchingAdmission(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	first := currentNodeReferenceForControllerTest(t, "task-automatic-a", "node-a")
	second := currentNodeReferenceForControllerTest(t, "task-automatic-b", "node-b")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &firstAdmissionBlockingScriptRunner{
		authority: authority,
		shellPath: shellPath,
		entered:   make(chan workflow.CurrentNodeReference, 2),
		release:   make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseRunner := func() {
		releaseOnce.Do(func() {
			close(runner.release)
		})
	}
	defer releaseRunner()
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{
		{CurrentNode: first, NodeKind: workflow.NodeKindAgent},
		{CurrentNode: second, NodeKind: workflow.NodeKindAgent},
	})
	select {
	case entered := <-runner.entered:
		first = entered
	case <-time.After(3 * time.Second):
		t.Fatal("first automatic admission did not begin")
	}
	select {
	case entered := <-runner.entered:
		t.Fatalf("automatic admission %v began without available capacity", entered)
	case <-time.After(100 * time.Millisecond):
	}

	releaseRunner()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return hasLiveCurrentNode(controller.Snapshot(), first)
	}, "first automatic current node did not become live")
	firstHandle, ok := authority.ExecutionByScope(singleLiveScope(t, controller, first))
	if !ok {
		t.Fatal("first automatic current node has no exact execution")
	}
	if err := firstHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop first automatic current node: %v", err)
	}
	select {
	case entered := <-runner.entered:
		if !entered.Equal(second) {
			t.Fatalf("second automatic admission = %v, want %v", entered, second)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued automatic admission did not begin after capacity released")
	}
}

func TestCurrentNodeControllerStartsScriptsWhileAgentCapacityIsSaturated(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	agent := currentNodeReferenceForControllerTest(t, "task-agent-running", "node-agent-running")
	queuedAgent := currentNodeReferenceForControllerTest(t, "task-agent-queued", "node-agent-queued")
	firstScript := currentNodeReferenceForControllerTest(t, "task-script-first", "node-script-first")
	secondScript := currentNodeReferenceForControllerTest(t, "task-script-second", "node-script-second")
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
		started: make(chan workflow.CurrentNodeReference, 4),
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
		CurrentNode: agent,
		NodeKind:    workflow.NodeKindAgent,
	}})
	select {
	case started := <-runner.started:
		if !started.Equal(agent) {
			t.Fatalf("first automatic start = %v, want %v", started, agent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first automatic Agent Node did not start")
	}
	waitForRunningCurrentNode(t, authority, agent)

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{
		{CurrentNode: queuedAgent, NodeKind: workflow.NodeKindAgent},
		{CurrentNode: firstScript, NodeKind: workflow.NodeKindScript},
		{CurrentNode: secondScript, NodeKind: workflow.NodeKindScript},
	})
	seenScripts := map[workflow.CurrentNodeReference]bool{}
	for len(seenScripts) < 2 {
		select {
		case started := <-runner.started:
			switch {
			case started.Equal(firstScript), started.Equal(secondScript):
				seenScripts[started] = true
			case started.Equal(queuedAgent):
				t.Fatalf("queued automatic Agent Node started before the occupying Agent was released")
			default:
				t.Fatalf("unexpected automatic start %v", started)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Scripts did not start concurrently while Agent capacity was saturated: %v", seenScripts)
		}
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshot := controller.Snapshot()
		return hasLiveCurrentNode(snapshot, firstScript) && hasLiveCurrentNode(snapshot, secondScript)
	}, "both Script Nodes did not become live before Agent release")
	if !hasAutomaticCurrentNodeIntent(controller.Snapshot(), queuedAgent) {
		t.Fatalf("automatic queue = %+v, want queued Agent while scripts are live", controller.Snapshot().AutomaticIntents)
	}

	agentHandle, ok := authority.ExecutionByScope(singleLiveScope(t, controller, agent))
	if !ok {
		t.Fatal("occupying Agent has no exact execution")
	}
	if err := agentHandle.Stop(context.Background()); err != nil {
		t.Fatalf("stop occupying Agent: %v", err)
	}
	select {
	case started := <-runner.started:
		if !started.Equal(queuedAgent) {
			t.Fatalf("queued Agent start = %v, want %v", started, queuedAgent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued Agent did not start after occupying Agent released")
	}
}

func TestCurrentNodeControllerAutomaticSelectionWaitsForTaskLifecycleWriter(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-selection-writer", "node-agent")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{entered: make(chan struct{}), release: make(chan struct{})}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	var releaseRunnerOnce sync.Once
	t.Cleanup(func() {
		releaseRunnerOnce.Do(func() { close(runner.release) })
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), reference.TaskID, func(context.Context) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	<-writerEntered

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{{
		CurrentNode: reference,
		NodeKind:    workflow.NodeKindAgent,
	}})
	time.Sleep(50 * time.Millisecond)
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("reference key: %v", err)
	}
	controller.mu.Lock()
	run, exists := controller.runs.get(key)
	reserved := false
	if exists {
		_, reserved = controller.automaticReservations[key]
	}
	controller.mu.Unlock()
	if !exists || run.phase != currentNodeRunQueued || reserved {
		t.Fatalf("Run crossed Task writer during selection: exists=%t phase=%d reserved=%t", exists, run.phase, reserved)
	}

	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("Task lifecycle writer: %v", err)
	}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("automatic admission did not continue after Task writer released")
	}
	releaseRunnerOnce.Do(func() { close(runner.release) })
}

func TestCurrentNodeControllerAutomaticSelectionSkipsBusyTaskForUnrelatedScript(t *testing.T) {
	busy := currentNodeReferenceForControllerTest(t, "task-busy-selection", "node-agent")
	script := currentNodeReferenceForControllerTest(t, "task-unrelated-selection", "node-script")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &boundedExplicitAdmissionRunner{
		entered: make(chan workflow.CurrentNodeReference, 2),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	var releaseRunnerOnce sync.Once
	t.Cleanup(func() {
		releaseRunnerOnce.Do(func() { close(runner.release) })
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), busy.TaskID, func(context.Context) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	<-writerEntered

	controller.enqueueAutomaticIntents([]CurrentNodeAutomaticIntent{
		{CurrentNode: busy, NodeKind: workflow.NodeKindAgent},
		{CurrentNode: script, NodeKind: workflow.NodeKindScript},
	})
	select {
	case started := <-runner.entered:
		if !started.Equal(script) {
			t.Fatalf("start while first Task writer was busy = %v, want unrelated Script %v", started, script)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("busy Task writer prevented unrelated Script progress")
	}

	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("busy Task lifecycle writer: %v", err)
	}
	releaseRunnerOnce.Do(func() { close(runner.release) })
}

func TestCurrentNodeControllerExplicitSelectionSkipsBusyTask(t *testing.T) {
	busy := currentNodeReferenceForControllerTest(t, "task-busy-explicit-selection", "node-agent")
	unrelated := currentNodeReferenceForControllerTest(t, "task-unrelated-explicit-selection", "node-agent")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &boundedExplicitAdmissionRunner{
		entered: make(chan workflow.CurrentNodeReference, 2),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 2)
	var releaseRunnerOnce sync.Once
	t.Cleanup(func() {
		releaseRunnerOnce.Do(func() { close(runner.release) })
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		writerDone <- controller.lifecycle.Run(context.Background(), busy.TaskID, func(context.Context) error {
			close(writerEntered)
			<-releaseWriter
			return nil
		})
	}()
	<-writerEntered

	controller.mu.Lock()
	if err := controller.queueExplicitStartLocked(*newCurrentNodeRun(
		busy,
		workflow.NodeKindAgent,
		currentNodeAdmissionExplicitOverride,
	)); err != nil {
		controller.mu.Unlock()
		t.Fatalf("queue busy explicit Run: %v", err)
	}
	if err := controller.queueExplicitStartLocked(*newCurrentNodeRun(
		unrelated,
		workflow.NodeKindAgent,
		currentNodeAdmissionExplicitOverride,
	)); err != nil {
		controller.mu.Unlock()
		t.Fatalf("queue unrelated explicit Run: %v", err)
	}
	controller.mu.Unlock()

	select {
	case started := <-runner.entered:
		if !started.Equal(unrelated) {
			t.Fatalf("explicit start while first Task writer was busy = %v, want %v", started, unrelated)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("busy Task writer prevented unrelated explicit admission")
	}

	close(releaseWriter)
	if err := <-writerDone; err != nil {
		t.Fatalf("busy Task lifecycle writer: %v", err)
	}
	releaseRunnerOnce.Do(func() { close(runner.release) })
}

func TestCurrentNodeControllerCloseBroadcastsScriptStopsBeforeJoining(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	const scriptCount = 3
	grace := 250 * time.Millisecond
	script := `trap '' TERM; while :; do sleep 1; done`
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
			Path:              shellPath,
			Args:              []string{"-c", script},
			CancellationGrace: &grace,
		},
		started: make(chan workflow.CurrentNodeReference, scriptCount),
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
	references := make([]workflow.CurrentNodeReference, 0, scriptCount)
	intents := make([]CurrentNodeAutomaticIntent, 0, scriptCount)
	for index := 0; index < scriptCount; index++ {
		reference := currentNodeReferenceForControllerTest(t, fmt.Sprintf("task-close-script-%d", index), fmt.Sprintf("node-script-%d", index))
		references = append(references, reference)
		intents = append(intents, CurrentNodeAutomaticIntent{
			CurrentNode: reference,
			NodeKind:    workflow.NodeKindScript,
		})
	}
	controller.enqueueAutomaticIntents(intents)
	started := make(map[workflow.CurrentNodeReference]struct{}, scriptCount)
	for len(started) < scriptCount {
		select {
		case reference := <-runner.started:
			started[reference] = struct{}{}
			if len(started) == scriptCount {
				break
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("Scripts did not start: %+v", started)
		}
	}
	for _, reference := range references {
		waitForRunningCurrentNode(t, authority, reference)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshot := controller.Snapshot()
		for _, reference := range references {
			if !hasLiveCurrentNode(snapshot, reference) {
				return false
			}
		}
		return true
	}, "all Script Nodes did not enter controller live state")

	closeStarted := time.Now()
	if err := controller.Close(); err != nil {
		t.Fatalf("controller Close: %v", err)
	}
	if elapsed := time.Since(closeStarted); elapsed >= 2*grace {
		t.Fatalf("controller Close took %s for %d Script grace windows, want overlapping shutdown", elapsed, scriptCount)
	}
}

func TestCurrentNodeControllerStartTaskSerializesSameTaskLifecycleWrites(t *testing.T) {
	taskID := workflow.TaskID("task-start-serialization")
	target := currentNodeReferenceForControllerTest(t, string(taskID), "node-target")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  target,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
		startTaskStarted: make(chan struct{}),
		startTaskRelease: make(chan struct{}),
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	permit := NewMutationPermit()
	controller, err := NewCurrentNodeController(store, runner, authority, permit, CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentEnsurer: noOpCurrentNodeAssignmentEnsurer{},
	})
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	var releaseRunnerOnce sync.Once
	releaseRunner := func() {
		releaseRunnerOnce.Do(func() {
			close(runner.release)
		})
	}
	t.Cleanup(func() {
		releaseRunner()
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	startDone := make(chan error, 1)
	go func() {
		_, err := controller.StartTask(context.Background(), taskID, func(context.Context) error { return nil })
		startDone <- err
	}()
	select {
	case <-store.startTaskStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("task start did not enter durable mutation")
	}
	secondStart := make(chan error, 1)
	go func() {
		_, err := controller.StartTask(context.Background(), taskID, func(context.Context) error { return nil })
		secondStart <- err
	}()
	select {
	case err := <-secondStart:
		t.Fatalf("same-Task start crossed unfinished lifecycle write: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(store.startTaskRelease)
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("StartTask: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("task start did not finish")
	}
	select {
	case err := <-secondStart:
		if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("second same-Task start = %v, want %v", err, ErrTaskExecutionNotQuiescent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second same-Task start did not resume")
	}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit admission did not begin")
	}
	releaseRunner()
}

func TestCurrentNodeControllerStartTaskDoesNotSerializeUnrelatedTasks(t *testing.T) {
	slowTaskID := workflow.TaskID("task-slow-start")
	fastTaskID := workflow.TaskID("task-fast-start")
	slowReference := currentNodeReferenceForControllerTest(t, string(slowTaskID), "node-target")
	fastReference := currentNodeReferenceForControllerTest(t, string(fastTaskID), "node-target")
	releaseSlow := make(chan struct{})
	var releaseSlowOnce sync.Once
	store := &currentNodeControllerStore{
		startedByTask: map[workflow.TaskID]workflowstore.StartTaskResult{
			slowTaskID: {Mutation: workflow.CurrentNodeMutationResult{Created: []workflow.CurrentNode{{
				Reference:  slowReference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}}}},
			fastTaskID: {Mutation: workflow.CurrentNodeMutationResult{Created: []workflow.CurrentNode{{
				Reference:  fastReference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}}}},
		},
		startTaskCalls: make(chan workflow.TaskID, 2),
		startTaskHook: func(ctx context.Context, taskID workflow.TaskID) error {
			if taskID != slowTaskID {
				return nil
			}
			select {
			case <-releaseSlow:
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseRunnerOnce sync.Once
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		releaseSlowOnce.Do(func() { close(releaseSlow) })
		releaseRunnerOnce.Do(func() { close(runner.release) })
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	slowDone := make(chan error, 1)
	go func() {
		_, err := controller.StartTask(context.Background(), slowTaskID, func(context.Context) error { return nil })
		slowDone <- err
	}()
	if taskID := <-store.startTaskCalls; taskID != slowTaskID {
		t.Fatalf("first durable Task start = %s, want %s", taskID, slowTaskID)
	}

	fastDone := make(chan error, 1)
	go func() {
		_, err := controller.StartTask(context.Background(), fastTaskID, func(context.Context) error { return nil })
		fastDone <- err
	}()
	select {
	case taskID := <-store.startTaskCalls:
		if taskID != fastTaskID {
			t.Fatalf("concurrent durable Task start = %s, want %s", taskID, fastTaskID)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("unrelated Task start waited for the slow Task lifecycle write")
	}
	releaseSlowOnce.Do(func() { close(releaseSlow) })
	if err := <-slowDone; err != nil {
		t.Fatalf("slow StartTask: %v", err)
	}
	if err := <-fastDone; err != nil {
		t.Fatalf("fast StartTask: %v", err)
	}
}

func TestCurrentNodeControllerStartTaskReturnsBeforePreparation(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-deferred-preparation", "node-agent")
	store := &currentNodeControllerStore{started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
		Created: []workflow.CurrentNode{{
			Reference:  reference,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		}},
	}}}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{entered: make(chan struct{}), release: make(chan struct{})}
	permit := NewMutationPermit()
	controller, err := NewCurrentNodeController(store, runner, authority, permit, CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentEnsurer: noOpCurrentNodeAssignmentEnsurer{},
	})
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	preparationStarted := make(chan struct{})
	preparationRelease := make(chan struct{})

	started := make(chan error, 1)
	go func() {
		_, err := controller.StartTask(context.Background(), reference.TaskID, func(ctx context.Context) error {
			close(preparationStarted)
			select {
			case <-preparationRelease:
				return nil
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		})
		started <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("StartTask: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("StartTask waited for asynchronous preparation")
	}
	<-preparationStarted
	permitAvailable := make(chan error, 1)
	go func() {
		permitAvailable <- permit.Run(context.Background(), func(context.Context) error { return nil })
	}()
	select {
	case err := <-permitAvailable:
		if err != nil {
			t.Fatalf("unrelated mutation permit: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("preparation blocked unrelated workflow mutations")
	}
	close(preparationRelease)
	<-runner.entered
	close(runner.release)
}

func TestCurrentNodeControllerPreparationFailureInterruptsPlacedNode(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-preparation-failure", "node-agent")
	store := &currentNodeControllerStore{started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
		Created: []workflow.CurrentNode{{
			Reference:  reference,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		}},
	}}}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	cause := errors.New("worktree setup failed")

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return cause
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if interruption, ok := store.interruption(reference); ok {
			if interruption.reason != reasonCurrentNodeRuntimeStartFailed ||
				interruption.detail.Fields["error"] != cause.Error() {
				t.Fatalf("interruption = %+v, want preparation failure", interruption)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("placed Current Node was not interrupted")
		}
		time.Sleep(time.Millisecond)
	}
	if runner.starts() != 0 {
		t.Fatalf("runner starts = %d, want none", runner.starts())
	}
}

func TestCurrentNodeControllerReservationBlocksTaskQuiescence(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-reservation-quiescence", "node-agent")
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
	run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
	run.phase = currentNodeRunReserved
	installCurrentNodeRunLockedForTest(controller, run)
	controller.automaticReservations[key] = struct{}{}
	controller.mu.Unlock()

	if err := controller.EnsureTaskQuiescent(reference.TaskID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		t.Fatalf("EnsureTaskQuiescent error = %v, want %v", err, ErrTaskExecutionNotQuiescent)
	}
	otherTaskID := workflow.TaskID("task-quiescent")
	quiescence, err := controller.CurrentTaskQuiescence([]workflow.TaskID{reference.TaskID, otherTaskID})
	if err != nil {
		t.Fatalf("CurrentTaskQuiescence: %v", err)
	}
	if quiescence[reference.TaskID] || !quiescence[otherTaskID] {
		t.Fatalf("task quiescence = %+v, want reserved Task false and unrelated Task true", quiescence)
	}
	if !hasAutomaticCurrentNodeIntent(controller.Snapshot(), reference) {
		t.Fatalf("reservation is absent from immutable live snapshot: %+v", controller.Snapshot())
	}
}

func TestCurrentNodeControllerTaskQuiescenceRejectsEveryControllerOwnedWorkState(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-quiescence-states", "node-agent")
	tests := []struct {
		name  string
		apply func(*CurrentNodeController)
	}{
		{
			name: "automatic queue",
			apply: func(controller *CurrentNodeController) {
				run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
				key := installCurrentNodeRunLockedForTest(controller, run)
				controller.automaticQueue.append(key, run)
			},
		},
		{
			name: "automatic reservation",
			apply: func(controller *CurrentNodeController) {
				key, err := reference.Key()
				if err != nil {
					t.Fatalf("reference key: %v", err)
				}
				run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
				run.phase = currentNodeRunReserved
				installCurrentNodeRunLockedForTest(controller, run)
				controller.automaticReservations[key] = struct{}{}
			},
		},
		{
			name: "retirement held intent",
			apply: func(controller *CurrentNodeController) {
				run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
				key := installCurrentNodeRunLockedForTest(controller, run)
				controller.heldStarts[runtimeids.NewExecutionScopeID()] = []workflow.CurrentNodeReferenceKey{key}
			},
		},
		{
			name: "admission gate",
			apply: func(controller *CurrentNodeController) {
				key, err := reference.Key()
				if err != nil {
					t.Fatalf("reference key: %v", err)
				}
				run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
				run.phase = currentNodeRunGated
				installCurrentNodeRunLockedForTest(controller, run)
				controller.gates[key] = struct{}{}
			},
		},
		{
			name: "live scope",
			apply: func(controller *CurrentNodeController) {
				run := newCurrentNodeRun(reference, workflow.NodeKindAgent, currentNodeAdmissionAutomaticAgent)
				run.phase = currentNodeRunRunning
				installCurrentNodeRunLockedForTest(controller, run)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			controller.mu.Lock()
			test.apply(controller)
			controller.mu.Unlock()
			if err := controller.EnsureTaskQuiescent(reference.TaskID); !errors.Is(err, ErrTaskExecutionNotQuiescent) {
				t.Fatalf("EnsureTaskQuiescent error = %v, want %v", err, ErrTaskExecutionNotQuiescent)
			}
		})
	}
}
