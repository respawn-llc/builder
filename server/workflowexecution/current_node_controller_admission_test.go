package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

type recordingLifecycleFatalReporter struct {
	diagnostics chan LifecycleFatalDiagnostic
	mu          sync.Mutex
	cause       error
}

func newRecordingLifecycleFatalReporter() *recordingLifecycleFatalReporter {
	return &recordingLifecycleFatalReporter{diagnostics: make(chan LifecycleFatalDiagnostic, 8)}
}

func (r *recordingLifecycleFatalReporter) ReportFatal(diagnostic LifecycleFatalDiagnostic) LifecycleFatalReportResult {
	r.mu.Lock()
	accepted := r.cause == nil
	r.cause = errors.Join(r.cause, diagnostic)
	r.mu.Unlock()
	r.diagnostics <- diagnostic
	return LifecycleFatalReportResult{ShutdownAccepted: accepted}
}

func (r *recordingLifecycleFatalReporter) Available() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cause == nil {
		return nil
	}
	return LifecycleUnavailableError{Cause: r.cause}
}

func TestCurrentNodeControllerReadyPreparationPersistenceFailureReportsTypedFatalDiagnostic(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-ready-fatal", "node-agent")
	preparationErr := errors.New("prepare execution target")
	persistenceErr := errors.New("persist ready interruption")
	store := &currentNodeControllerStore{interruptErr: persistenceErr}
	store.started = workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
		Created: []workflow.CurrentNode{{
			Reference:  reference,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		}},
	}}
	reporter := newRecordingLifecycleFatalReporter()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := NewCurrentNodeController(
		store,
		&countingCurrentNodeRunner{},
		authority,
		NewTaskMutationCoordinator(),
		CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentEnsurer: noOpCurrentNodeAssignmentEnsurer{},
			LifecycleReporter: reporter,
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil && !errors.Is(err, persistenceErr) {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return preparationErr
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	var diagnostic LifecycleFatalDiagnostic
	select {
	case diagnostic = <-reporter.diagnostics:
	case <-time.After(3 * time.Second):
		t.Fatal("ready preparation persistence failure did not report fatal lifecycle state")
	}
	if diagnostic.Operation != LifecycleFatalOperationReadyPreparationFailure ||
		diagnostic.TaskID != reference.TaskID ||
		!diagnostic.CurrentNode.Equal(reference) ||
		diagnostic.RunID == 0 ||
		diagnostic.RunPhase != LifecycleFatalRunPhaseLaunching ||
		diagnostic.ExpectedScheduling != workflow.CurrentNodeSchedulingReady ||
		diagnostic.ScopeID != nil ||
		!errors.Is(diagnostic.OriginalOutcome, preparationErr) ||
		!errors.Is(diagnostic.PersistenceFailure, persistenceErr) {
		t.Fatalf("ready fatal diagnostic = %+v", diagnostic)
	}
	if _, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID}); !errors.Is(err, persistenceErr) {
		t.Fatalf("lifecycle read after fatal report = %v, want persistence failure", err)
	}
}

func TestCurrentNodeControllerAdmittedLaunchPersistenceFailureReportsTypedFatalDiagnostic(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-admitted-fatal", "node-agent")
	launchErr := errors.New("launch runtime")
	persistenceErr := errors.New("persist admitted interruption")
	store := &currentNodeControllerStore{
		interruptErr: persistenceErr,
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
	}
	reporter := newRecordingLifecycleFatalReporter()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := NewCurrentNodeController(
		store,
		failingCurrentNodeRunner{cause: launchErr},
		authority,
		NewTaskMutationCoordinator(),
		CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentEnsurer: noOpCurrentNodeAssignmentEnsurer{},
			LifecycleReporter: reporter,
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil && !errors.Is(err, persistenceErr) {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	var diagnostic LifecycleFatalDiagnostic
	select {
	case diagnostic = <-reporter.diagnostics:
	case <-time.After(3 * time.Second):
		t.Fatal("admitted launch persistence failure did not report fatal lifecycle state")
	}
	if diagnostic.Operation != LifecycleFatalOperationAdmittedLaunchFailure ||
		diagnostic.TaskID != reference.TaskID ||
		!diagnostic.CurrentNode.Equal(reference) ||
		diagnostic.RunID == 0 ||
		diagnostic.RunPhase != LifecycleFatalRunPhaseLaunching ||
		diagnostic.ExpectedScheduling != workflow.CurrentNodeSchedulingAdmitted ||
		diagnostic.ScopeID != nil ||
		!errors.Is(diagnostic.OriginalOutcome, launchErr) ||
		!errors.Is(diagnostic.PersistenceFailure, persistenceErr) {
		t.Fatalf("admitted fatal diagnostic = %+v", diagnostic)
	}
	if _, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID}); !errors.Is(err, persistenceErr) {
		t.Fatalf("lifecycle read after admitted fatal report = %v, want persistence failure", err)
	}
}

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
	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{reference.TaskID})
	if err != nil {
		t.Fatalf("observe registered launching scope: %v", err)
	}
	runs := observation.Runs[reference.TaskID]
	if len(runs.Queued) != 0 ||
		len(runs.InterruptibleLaunching) != 1 ||
		!runs.InterruptibleLaunching[0].Equal(reference) {
		t.Fatalf("registered launching Run observation = %+v, want %v", runs, reference)
	}

	close(runner.returnStart)
	if err := <-started; err != nil {
		t.Fatalf("start current node: %v", err)
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

func TestCurrentNodeControllerRunnerFailureAfterExactRegistrationUsesExactRuntimeDisposition(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-immediate-exact-failure", "node-agent")
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
	runnerErr := errors.New("runner returned after exact registration")
	runner := &controlledScriptRunner{
		authority:   authority,
		command:     sessionruntime.ScriptCommand{Path: shellPath, Args: []string{"-c", "while :; do sleep 1; done"}},
		entered:     make(chan struct{}),
		startRunner: make(chan struct{}),
		registered:  make(chan struct{}),
		returnStart: make(chan struct{}),
		handles:     make(chan sessionruntime.ExecutionHandle, 1),
		returnErr:   runnerErr,
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
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

	if err := startCurrentNodeForControllerTest(context.Background(), controller, store, reference); err != nil {
		t.Fatalf("start current node: %v", err)
	}
	<-runner.entered
	close(runner.startRunner)
	<-runner.registered
	close(runner.returnStart)
	select {
	case <-store.interruptStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("immediate runner failure did not begin Exact-runtime disposition")
	}
	if !hasLiveCurrentNode(controller.Snapshot(), reference) {
		t.Fatalf("runner failure removed Exact Run before durable disposition: %+v", controller.Snapshot())
	}
	close(store.interruptRelease)
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		interruption, interrupted := store.interruption(reference)
		return interrupted &&
			interruption.reason == reasonCurrentNodeRuntimeStartFailed &&
			interruption.detail.Fields["error"] == runnerErr.Error()
	}, "immediate runner failure did not durably interrupt its Exact generation")
}

func TestCurrentNodeControllerFinalizedWithoutOutcomeInterruptsAdmittedCurrentNode(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-finalization-failure", "node-script")
	store := &currentNodeControllerStore{}
	attention := &currentNodeAttentionRecorder{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &finalizerFailureScriptRunner{
		authority:        authority,
		shellPath:        shellPath,
		finalizerEntered: make(chan struct{}),
		releaseFinalizer: make(chan struct{}),
		handle:           make(chan sessionruntime.ExecutionHandle, 1),
	}
	controller = newCurrentNodeControllerWithAttentionForTest(t, store, runner, authority, 1, attention)
	var releaseOnce sync.Once
	releaseFinalizer := func() {
		releaseOnce.Do(func() {
			close(runner.releaseFinalizer)
		})
	}
	t.Cleanup(func() {
		releaseFinalizer()
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
	select {
	case <-runner.finalizerEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Script did not reach completion finalization")
	}
	if !hasLiveCurrentNode(controller.Snapshot(), reference) {
		t.Fatal("Script finalized before becoming the exact live Current Node")
	}
	releaseFinalizer()
	handle := <-runner.handle
	if _, err := handle.Wait(context.Background()); err == nil {
		t.Fatal("Script finalization unexpectedly succeeded")
	}

	var interruption currentNodeInterruptionRecord
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		var interrupted bool
		interruption, interrupted = store.interruption(reference)
		return interrupted
	}, "outcome-less finalization did not interrupt the admitted current node")
	if interruption.reason != reasonCurrentNodeRuntimeFinalizedWithoutOutcome {
		t.Fatalf(
			"interruption reason = %q, want %q",
			interruption.reason,
			reasonCurrentNodeRuntimeFinalizedWithoutOutcome,
		)
	}
	if !workflow.IsActionableCurrentNodeInterruptionReason(interruption.reason) {
		t.Fatalf("interruption reason = %q, want actionable runtime finalization failure", interruption.reason)
	}
	if interruption.detail.Code != string(reasonCurrentNodeRuntimeFinalizedWithoutOutcome) ||
		interruption.detail.Diagnostic() == nil {
		t.Fatalf("interruption detail = %+v, want typed runtime finalization diagnostic", interruption.detail)
	}
	if calls := store.interruptionCount(reference); calls != 1 {
		t.Fatalf("outcome-less finalization interruption writes = %d, want 1", calls)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return attention.pendingCount() == 1
	}, "outcome-less finalization did not publish interrupted Current Node attention")
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

type finalizerFailureScriptRunner struct {
	authority        *sessionruntime.Authority
	shellPath        string
	finalizerEntered chan struct{}
	releaseFinalizer chan struct{}
	handle           chan sessionruntime.ExecutionHandle
}

func (r *finalizerFailureScriptRunner) StartCurrentNode(
	_ context.Context,
	_ workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentEnsure,
	lease sessionruntime.WorkflowExecutionLease,
	_ workflowruntime.Controller,
) error {
	handle, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "exit 0"}},
		Finalize: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.ScriptResult, error) error {
			close(r.finalizerEntered)
			<-r.releaseFinalizer
			return errors.New("persist completion: database snapshot is busy")
		},
	})
	if err != nil {
		return err
	}
	r.handle <- handle
	return nil
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
	ensurer := &recordingCurrentNodeAssignmentEnsurer{}
	controller, err := NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		LifecycleReporter: newRecordingLifecycleFatalReporter(),
		AssignmentEnsurer: ensurer,
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
	if ensured := ensurer.references(); len(ensured) != 1 || !ensured[0].Equal(reference) {
		t.Fatalf("ensured Resume assignments = %+v, want %v", ensured, reference)
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

func TestRetainedUserActivationWaitsBehindExplicitAdmissionCapacityWithoutOwningAWaiter(t *testing.T) {
	const branchCount = explicitAdmissionConcurrency + 1
	taskID := workflow.TaskID("task-retained-activation-capacity")
	interrupted := make([]workflow.CurrentNode, 0, branchCount)
	for index := 0; index < branchCount; index++ {
		reference := currentNodeReferenceForControllerTest(t, string(taskID), uuid.NewString())
		interrupted = append(interrupted, workflow.CurrentNode{
			Reference:  reference,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		})
	}
	retained := interrupted[len(interrupted)-1].Reference
	sessionID, err := runtimeids.ParseSessionID(uuid.NewString())
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	store := &currentNodeControllerStore{
		interrupted:   interrupted,
		sessionTaskID: &taskID,
		directSessionBinding: &workflowstore.DirectSessionCurrentNodeBinding{
			TaskID:      taskID,
			CurrentNode: retained,
		},
		sessionAssociation: &workflowstore.TaskSessionAssociation{
			SessionID:   sessionID,
			CurrentNode: retained,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &boundedExplicitAdmissionRunner{
		entered: make(chan workflow.CurrentNodeReference, branchCount),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		close(runner.release)
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	if _, err := controller.ResumeTask(context.Background(), taskID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	for range explicitAdmissionConcurrency {
		select {
		case <-runner.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("explicit admission did not fill its bounded setup capacity")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	activationDone := make(chan error, 1)
	go func() {
		_, activationErr := controller.ActivateWorkflowSession(ctx, sessionruntime.WorkflowSessionActivationRequest{
			SessionID: sessionID,
			OwnerID:   "retained-capacity-owner",
			Operation: serverapi.SessionRuntimeActivationUserActivation,
		})
		activationDone <- activationErr
	}()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		observation, observeErr := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{taskID})
		if observeErr != nil {
			return false
		}
		runs := observation.Runs[taskID]
		return slices.ContainsFunc(runs.Queued, func(reference workflow.CurrentNodeReference) bool {
			return reference.Equal(retained)
		}) &&
			!slices.ContainsFunc(runs.InterruptibleLaunching, func(reference workflow.CurrentNodeReference) bool {
				return reference.Equal(retained)
			})
	}, "retained activation did not remain queued and non-interruptible behind explicit admission capacity")

	cancel()
	select {
	case activationErr := <-activationDone:
		if !errors.Is(activationErr, context.Canceled) {
			t.Fatalf("canceled capacity-waiting activation error = %v, want context canceled", activationErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled capacity-waiting activation did not return")
	}
	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{taskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	if !slices.ContainsFunc(observation.Runs[taskID].Queued, func(reference workflow.CurrentNodeReference) bool {
		return reference.Equal(retained)
	}) {
		t.Fatalf("request cancellation removed the capacity-owned Run: %+v", observation.Runs[taskID])
	}
}

func TestRetainedUserActivationReadyPreparationFailureReturnsAfterDurableInterruption(t *testing.T) {
	taskID := workflow.TaskID("task-retained-activation-ready-failure")
	reference := currentNodeReferenceForControllerTest(t, string(taskID), "node-agent")
	sessionID := runtimeids.NewSessionID()
	store := retainedActivationControllerStore(taskID, reference, sessionID)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	preparationEntered := make(chan struct{})
	releasePreparation := make(chan struct{})
	preparationErr := errors.New("retained activation ready preparation failed")
	if _, err := controller.ResumeTaskWithPreparation(
		context.Background(),
		taskID,
		func(ctx context.Context) error {
			close(preparationEntered)
			select {
			case <-releasePreparation:
				return preparationErr
			case <-ctx.Done():
				return context.Cause(ctx)
			}
		},
	); err != nil {
		t.Fatalf("ResumeTaskWithPreparation: %v", err)
	}
	select {
	case <-preparationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("Task Resume did not enter ready-scheduling preparation")
	}

	activationDone := make(chan error, 1)
	go func() {
		_, activationErr := controller.ActivateWorkflowSession(context.Background(), sessionruntime.WorkflowSessionActivationRequest{
			SessionID: sessionID,
			OwnerID:   "retained-ready-failure-owner",
			Operation: serverapi.SessionRuntimeActivationUserActivation,
		})
		activationDone <- activationErr
	}()
	select {
	case activationErr := <-activationDone:
		t.Fatalf("activation returned while ready preparation was blocked: %v", activationErr)
	case <-time.After(150 * time.Millisecond):
	}
	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{taskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	runs := observation.Runs[taskID]
	if len(runs.Queued) != 0 ||
		len(runs.InterruptibleLaunching) != 1 ||
		!runs.InterruptibleLaunching[0].Equal(reference) {
		t.Fatalf("ready preparation activation observation = %+v, want one interruptible launching Run", runs)
	}

	close(releasePreparation)
	select {
	case activationErr := <-activationDone:
		if !errors.Is(activationErr, serverapi.ErrRuntimeUnavailable) {
			t.Fatalf("ready preparation activation error = %v, want runtime unavailable", activationErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("activation did not return after ready preparation failure")
	}
	interruption, interrupted := store.interruption(reference)
	if !interrupted ||
		interruption.reason != reasonCurrentNodeRuntimeStartFailed ||
		store.interruptionCount(reference) != 1 {
		t.Fatalf("ready preparation interruption = %+v, interrupted=%t", interruption, interrupted)
	}
	if starts := runner.starts(); starts != 0 {
		t.Fatalf("ready preparation failure reached runner %d times, want 0", starts)
	}
}

func TestRetainedUserActivationAdmittedLaunchFailureReturnsAfterDurableInterruption(t *testing.T) {
	taskID := workflow.TaskID("task-retained-activation-admitted-failure")
	reference := currentNodeReferenceForControllerTest(t, string(taskID), "node-agent")
	sessionID := runtimeids.NewSessionID()
	store := retainedActivationControllerStore(taskID, reference, sessionID)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	launchErr := errors.New("retained activation admitted launch failed")
	controller := newCurrentNodeControllerForTest(t, store, failingCurrentNodeRunner{cause: launchErr}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	_, err := controller.ActivateWorkflowSession(context.Background(), sessionruntime.WorkflowSessionActivationRequest{
		SessionID: sessionID,
		OwnerID:   "retained-admitted-failure-owner",
		Operation: serverapi.SessionRuntimeActivationUserActivation,
	})
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("admitted launch activation error = %v, want runtime unavailable", err)
	}
	interruption, interrupted := store.interruption(reference)
	if !interrupted ||
		interruption.reason != reasonCurrentNodeRuntimeStartFailed ||
		store.interruptionCount(reference) != 1 ||
		store.admitCount() != 1 {
		t.Fatalf(
			"admitted launch interruption = %+v, interrupted=%t writes=%d admits=%d",
			interruption,
			interrupted,
			store.interruptionCount(reference),
			store.admitCount(),
		)
	}
}

func retainedActivationControllerStore(
	taskID workflow.TaskID,
	reference workflow.CurrentNodeReference,
	sessionID runtimeids.SessionID,
) *currentNodeControllerStore {
	return &currentNodeControllerStore{
		interrupted: []workflow.CurrentNode{{
			Reference:  reference,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		}},
		sessionTaskID: &taskID,
		directSessionBinding: &workflowstore.DirectSessionCurrentNodeBinding{
			TaskID:      taskID,
			CurrentNode: reference,
		},
		sessionAssociation: &workflowstore.TaskSessionAssociation{
			SessionID:   sessionID,
			CurrentNode: reference,
		},
	}
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
	snapshot := controller.Snapshot()
	if len(snapshot.AutomaticIntents) != 0 ||
		len(snapshot.ExplicitStarts) != 0 ||
		len(snapshot.HeldIntents) != 0 ||
		len(snapshot.Gates) != 0 ||
		len(snapshot.LiveScopes) != 0 ||
		len(snapshot.InterruptingTasks) != 0 {
		t.Fatalf("controller Close retained Workflow Run ownership: %+v", snapshot)
	}
}

func TestCurrentNodeControllerStartTaskPublishesAdmissionOwnershipBeforeDeleteCanObserveQuiescence(t *testing.T) {
	taskID := workflow.TaskID("task-start-delete-linearization")
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
	taskMutations := NewTaskMutationCoordinator()
	controller, err := NewCurrentNodeController(store, runner, authority, taskMutations, CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentEnsurer: noOpCurrentNodeAssignmentEnsurer{},
		LifecycleReporter: newRecordingLifecycleFatalReporter(),
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
	deleteCheck := make(chan error, 1)
	go func() {
		deleteCheck <- taskMutations.Run(context.Background(), taskID, func(context.Context) error {
			return controller.EnsureTaskQuiescent(taskID)
		})
	}()
	select {
	case err := <-deleteCheck:
		t.Fatalf("delete quiescence check crossed unfinished task start: %v", err)
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
	case err := <-deleteCheck:
		if !errors.Is(err, ErrTaskExecutionNotQuiescent) {
			t.Fatalf("delete quiescence after task start = %v, want %v", err, ErrTaskExecutionNotQuiescent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delete quiescence check did not resume")
	}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit admission did not begin")
	}
	releaseRunner()
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
	taskMutations := NewTaskMutationCoordinator()
	controller, err := NewCurrentNodeController(store, runner, authority, taskMutations, CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentEnsurer: noOpCurrentNodeAssignmentEnsurer{},
		LifecycleReporter: newRecordingLifecycleFatalReporter(),
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
		permitAvailable <- taskMutations.Run(context.Background(), "unrelated-task", func(context.Context) error { return nil })
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
