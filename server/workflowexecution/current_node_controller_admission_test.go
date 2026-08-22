package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"

	"github.com/google/uuid"
)

func TestCurrentNodeControllerAdmitsScriptBeforeDetachedPublication(t *testing.T) {
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
		command:     sessionruntime.ScriptCommand{Path: shellPath, Args: []string{"-c", `printf started > "$1"; trap 'exit 0' TERM; while :; do sleep 1; done`, "sh", outputPath}},
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
	if store.admitCount() != 0 {
		t.Fatalf("admitted current nodes before publication = %d, want 0", store.admitCount())
	}
	close(runner.startRunner)
	<-runner.registered
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("script started before publication validation: stat error = %v", err)
	}
	if store.admitCount() != 0 {
		t.Fatalf("admitted current nodes before publication validation = %d, want 0", store.admitCount())
	}
	close(runner.returnStart)
	handle := <-runner.handles
	if store.admitCount() != 1 {
		t.Fatalf("admitted current nodes after publication validation = %d, want 1", store.admitCount())
	}
	if err := <-started; err != nil {
		t.Fatalf("start current node: %v", err)
	}
	workflowRef, workflowScoped := handle.Scope().Workflow()
	if !workflowScoped || !workflowRef.CurrentNode.Equal(reference) {
		t.Fatalf("published Workflow metadata = %+v, want Current Node %v", workflowRef, reference)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, err := os.Stat(outputPath)
		return err == nil
	}, "script did not start after controller released lease")
	if err := handle.Stop(context.Background()); err != nil {
		t.Fatalf("stop Script: %v", err)
	}
	if hasLiveCurrentNode(authority, reference) {
		t.Fatal("script execution remained live after retirement")
	}
}

func TestCurrentNodeControllerCloseDoesNotCancelStartedDurableAdmission(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-close-admission", "node-script")
	store := &currentNodeControllerStore{
		admitStarted: make(chan struct{}),
		admitRelease: make(chan struct{}),
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &recordingScriptRunner{
		authority: authority,
		command:   sessionruntime.ScriptCommand{Path: shellPath, Args: []string{"-c", "exit 0"}},
		started:   make(chan workflow.CurrentNodeReference, 1),
	}
	controller = newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	started := make(chan error, 1)
	go func() {
		started <- startCurrentNodeForControllerTest(context.Background(), controller, store, reference)
	}()
	<-store.admitStarted
	closed := make(chan error, 1)
	go func() { closed <- controller.Close() }()
	close(store.admitRelease)
	if err := <-started; err != nil && !errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
		t.Fatalf("start Current Node during close: %v", err)
	}
	if err := <-closed; err != nil {
		t.Fatalf("close controller: %v", err)
	}
	store.mu.Lock()
	sawCancellation := store.admitSawCancellation
	store.mu.Unlock()
	if sawCancellation || store.admitCount() != 1 {
		t.Fatalf("started admission = canceled:%t commits:%d, want false/1", sawCancellation, store.admitCount())
	}
	if err := authority.Close(context.Background()); err != nil {
		t.Fatalf("close authority: %v", err)
	}
}

func TestCurrentNodeAdmissionCommitCertainty(t *testing.T) {
	observerErr := errors.New("observer unavailable")
	if err := classifyCurrentNodeAdmission(session.CommitReceipt{Committed: true}, observerErr); err != nil {
		t.Fatalf("committed admission observer error = %v, want publication to continue", err)
	}
	definite := session.DefinitelyUncommittedMutation(errors.New("write rejected"))
	if err := classifyCurrentNodeAdmission(session.CommitReceipt{}, definite); !errors.Is(err, session.ErrMutationDefinitelyUncommitted) {
		t.Fatalf("definitely-uncommitted admission = %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("indeterminate admission did not fail fast")
		}
	}()
	_ = classifyCurrentNodeAdmission(session.CommitReceipt{}, errors.New("commit result unavailable"))
}

func TestAutomaticCurrentNodeStartFailureIsProcessFatalWhenInterruptionCannotPersist(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-automatic-fatal", "node-successor")
	startFailure := errors.New("automatic successor assignment failed")
	interruptionFailure := errors.New("current node interruption persistence failed")
	controller := &CurrentNodeController{
		store:     &currentNodeControllerStore{interruptionErr: interruptionFailure},
		mutations: NewTaskMutationCoordinator(),
	}

	defer func() {
		recovered := recover()
		fatal, ok := recovered.(*CurrentNodeAutomaticInterruptionPersistencePanic)
		if !ok {
			t.Fatalf("recovered panic = %#v, want automatic interruption persistence panic", recovered)
		}
		if fatal.Operation != "ready_start" ||
			!fatal.Reference.Equal(reference) ||
			fatal.ExpectedScheduling != workflow.CurrentNodeSchedulingReady ||
			!errors.Is(fatal.OriginalFailure, startFailure) ||
			!errors.Is(fatal.InterruptionFailure, interruptionFailure) {
			t.Fatalf("fatal panic = %+v, want exact automatic successor failure", fatal)
		}
		var processFatal interface{ ProcessFatalPanic() } = fatal
		processFatal.ProcessFatalPanic()
	}()

	controller.handleCurrentNodeStartFailures([]currentNodeQueuedStart{{
		reference: reference,
		policy:    currentNodeAdmissionAutomaticAgent,
	}}, false, startFailure)
}

func TestCurrentNodeControllerRunnerFailuresInterruptAdmittedCurrentNode(t *testing.T) {
	for name, cause := range map[string]error{
		"ordinary failure":         errors.New("provider unavailable"),
		"execution no longer live": sessionruntime.ErrExecutionNoLongerLive,
	} {
		t.Run(name, func(t *testing.T) {
			reference := currentNodeReferenceForControllerTest(t, "task-failure", "node-agent")
			store := &currentNodeControllerStore{}
			attention := &currentNodeAttentionRecorder{}
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
			controller := newCurrentNodeControllerWithAttentionForTest(t, store, failingCurrentNodeRunner{cause: cause}, authority, 1, attention)
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
		})
	}
}

func TestCurrentNodeControllerExecutionLossBeforeAdmissionInterruptsReadyCurrentNode(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-pre-admission-failure", "node-agent")
	store := &currentNodeControllerStore{}
	attention := &currentNodeAttentionRecorder{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	controller := newCurrentNodeControllerWithConfigForTest(t, store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency: 1,
		Attention:        attention,
		AssignmentSteerer: &recordingCurrentNodeAssignmentSteerer{
			waitErr: sessionruntime.ErrExecutionNoLongerLive,
		},
	})
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
	}, "pre-admission execution loss did not interrupt the ready current node")
	if admitted := store.admitCount(); admitted != 0 {
		t.Fatalf("admitted current nodes = %d, want 0", admitted)
	}
	if deliveries := runner.promptDeliveries(); len(deliveries) != 0 {
		t.Fatalf("runner prompt deliveries = %+v, want none", deliveries)
	}
	if interruption.reason != reasonCurrentNodeRuntimeStartFailed {
		t.Fatalf("interruption reason = %q, want %q", interruption.reason, reasonCurrentNodeRuntimeStartFailed)
	}
	if interruption.detail.Code != string(reasonCurrentNodeRuntimeStartFailed) ||
		interruption.detail.Diagnostic() == nil {
		t.Fatalf("interruption detail = %+v, want runtime-start diagnostic", interruption.detail)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return attention.pendingCount() == 1
	}, "pre-admission execution loss did not publish interrupted Current Node attention")
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
	if len(resumed.CurrentNodes) != 2 {
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

func (*finalizerFailureScriptRunner) UsesScriptPublication(workflow.CurrentNodeReference) bool {
	return true
}

func (r *finalizerFailureScriptRunner) PublishCurrentNode(
	_ context.Context,
	_ workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentSteer,
	lease workflowExecutionStart,
	_ workflowruntime.Controller,
) error {
	handle, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

		Command: sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "exit 0"}},
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
	controller := newCurrentNodeControllerWithConfigForTest(t, store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency: 1,
		AssignmentSteerer: &recordingCurrentNodeAssignmentSteerer{
			err: errors.New("Resume must not steer an assignment"),
		},
	})
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	prepared := make(chan struct{}, 1)
	resumed, err := controller.ResumeTaskWithPreparation(
		context.Background(),
		reference.TaskID,
		testTaskPreparation(func(context.Context) error {
			prepared <- struct{}{}
			return nil
		}),
		noOpTaskPreparationFinalizer,
	)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if len(resumed.CurrentNodes) != 1 || !resumed.CurrentNodes[0].Reference.Equal(reference) {
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

func TestCurrentNodeControllerSteersUnclassifiedAutomaticAgentBeforeStartingIt(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(
		t,
		"task-automatic-assignment",
		"node-automatic-agent",
	)
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	steerer := &recordingCurrentNodeAssignmentSteerer{}
	controller, err := NewCurrentNodeController(
		store,
		currentNodeTestPublicationRunner{runner: runner, authority: authority},
		authority,
		NewTaskMutationCoordinator(),
		CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: steerer,
		},
	)
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

	controller.enqueueStarts(automaticQueuedStarts([]CurrentNodeAutomaticIntent{{
		CurrentNode: reference,
		NodeKind:    workflow.NodeKindAgent,
	}}))

	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return runner.starts() == 1
	}, "automatic Agent did not reach runner")
	if got := steerer.references(); len(got) != 1 || !got[0].Equal(reference) {
		t.Fatalf("steered assignments = %+v, want %v", got, reference)
	}
	if deliveries := runner.promptDeliveries(); len(deliveries) != 1 ||
		deliveries[0] != workflowruntime.TaskPromptDeliveryResume {
		t.Fatalf("runner prompt deliveries = %+v, want Resume after assignment publication", deliveries)
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
	if len(resumed.CurrentNodes) != branchCount {
		t.Fatalf("resumed Current Nodes = %d, want %d", len(resumed.CurrentNodes), branchCount)
	}
	for index := 0; index < explicitAdmissionConcurrency; index++ {
		select {
		case <-runner.entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("explicit admission %d did not begin", index+1)
		}
	}
	runner.release <- struct{}{}
	select {
	case <-runner.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("releasing explicit admission capacity did not admit a queued sibling")
	}
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

	controller.enqueueStarts(automaticQueuedStarts([]CurrentNodeAutomaticIntent{
		{CurrentNode: first, NodeKind: workflow.NodeKindAgent},
		{CurrentNode: second, NodeKind: workflow.NodeKindAgent},
	}))
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
		return hasLiveCurrentNode(authority, first)
	}, "first automatic current node did not become live")
	firstHandle, ok := authority.ExecutionByScope(singleLiveScope(t, authority, first))
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

func TestCurrentNodeControllerPromotesConcurrencyQueuedTaskToExplicitAdmission(t *testing.T) {
	first := currentNodeReferenceForControllerTest(t, "task-capacity-owner", "node-first")
	queued := currentNodeReferenceForControllerTest(t, "task-force-resume", "node-queued")
	store := &currentNodeControllerStore{}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &boundedExplicitAdmissionRunner{
		entered: make(chan workflow.CurrentNodeReference, 2),
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

	controller.enqueueStarts(automaticQueuedStarts([]CurrentNodeAutomaticIntent{
		{CurrentNode: first, NodeKind: workflow.NodeKindAgent},
		{CurrentNode: queued, NodeKind: workflow.NodeKindAgent},
	}))
	select {
	case entered := <-runner.entered:
		if !entered.Equal(first) {
			t.Fatalf("first automatic admission = %v, want %v", entered, first)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first automatic admission did not begin")
	}
	select {
	case entered := <-runner.entered:
		t.Fatalf("automatic admission %v exceeded Agent capacity", entered)
	case <-time.After(100 * time.Millisecond):
	}

	observation, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{queued.TaskID})
	if err != nil {
		t.Fatalf("ObserveWorkflowTaskExecutions: %v", err)
	}
	if references := observation.ConcurrencyQueued[queued.TaskID]; len(references) != 1 ||
		!references[0].Equal(queued) {
		t.Fatalf("concurrency-queued Current Nodes = %+v, want %v", references, queued)
	}
	promoted, handled, err := controller.PromoteConcurrencyQueuedTask(
		context.Background(),
		queued.TaskID,
	)
	if err != nil {
		t.Fatalf("PromoteConcurrencyQueuedTask: %v", err)
	}
	if !handled || len(promoted) != 1 || !promoted[0].Reference.Equal(queued) {
		t.Fatalf("promoted Current Nodes = %+v handled=%v, want %v", promoted, handled, queued)
	}
	select {
	case entered := <-runner.entered:
		if !entered.Equal(queued) {
			t.Fatalf("explicit promoted admission = %v, want %v", entered, queued)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("promoted Current Node did not bypass Agent concurrency")
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
		scripts: map[workflow.CurrentNodeReference]struct{}{
			firstScript:  {},
			secondScript: {},
		},
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

	controller.enqueueStarts(automaticQueuedStarts([]CurrentNodeAutomaticIntent{{
		CurrentNode: agent,
		NodeKind:    workflow.NodeKindAgent,
	}}))
	select {
	case started := <-runner.started:
		if !started.Equal(agent) {
			t.Fatalf("first automatic start = %v, want %v", started, agent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first automatic Agent Node did not start")
	}
	waitForRunningCurrentNode(t, authority, agent)

	controller.enqueueStarts(automaticQueuedStarts([]CurrentNodeAutomaticIntent{
		{CurrentNode: queuedAgent, NodeKind: workflow.NodeKindAgent},
		{CurrentNode: firstScript, NodeKind: workflow.NodeKindScript},
		{CurrentNode: secondScript, NodeKind: workflow.NodeKindScript},
	}))
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
		return hasLiveCurrentNode(authority, firstScript) && hasLiveCurrentNode(authority, secondScript)
	}, "both Script Nodes did not become live before Agent release")

	agentHandle, ok := authority.ExecutionByScope(singleLiveScope(t, authority, agent))
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
	controller.enqueueStarts(automaticQueuedStarts(intents))
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

	closeStarted := time.Now()
	if err := controller.Close(); err != nil {
		t.Fatalf("controller Close: %v", err)
	}
	if elapsed := time.Since(closeStarted); elapsed >= 2*grace {
		t.Fatalf("controller Close took %s for %d Script grace windows, want overlapping shutdown", elapsed, scriptCount)
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
	controller := newCurrentNodeControllerWithConfigForTest(t, store, runner, authority, taskMutations, CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: noOpCurrentNodeAssignmentSteerer{},
	})
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
		_, err := controller.StartTask(
			context.Background(),
			taskID,
			testTaskPreparation(func(context.Context) error { return nil }),
			noOpTaskPreparationFinalizer,
		)
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
	controller := newCurrentNodeControllerWithConfigForTest(t, store, runner, authority, taskMutations, CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: noOpCurrentNodeAssignmentSteerer{},
	})
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	preparationStarted := make(chan struct{})
	preparationRelease := make(chan struct{})
	preparationCommitted := make(chan struct{})

	started := make(chan error, 1)
	go func() {
		_, err := controller.StartTask(
			context.Background(),
			reference.TaskID,
			TaskStartPreparation{
				Prepare: func(ctx context.Context) error {
					close(preparationStarted)
					select {
					case <-preparationRelease:
						return nil
					case <-ctx.Done():
						return context.Cause(ctx)
					}
				},
				Commit: func(context.Context) error {
					close(preparationCommitted)
					return nil
				},
			},
			noOpTaskPreparationFinalizer,
		)
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
		permitAvailable <- taskMutations.Run(context.Background(), workflow.TaskID("unrelated-task"), func(context.Context) error { return nil })
	}()
	select {
	case err := <-permitAvailable:
		if err != nil {
			t.Fatalf("unrelated Task mutation lane: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("preparation blocked unrelated workflow mutations")
	}
	select {
	case <-runner.entered:
		t.Fatal("Current Node admission began before preparation commit")
	default:
	}
	close(preparationRelease)
	<-preparationCommitted
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

	if _, err := controller.StartTask(
		context.Background(),
		reference.TaskID,
		testTaskPreparation(func(context.Context) error { return cause }),
		noOpTaskPreparationFinalizer,
	); err != nil {
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

func TestCurrentNodeControllerResumeSharesPreparationAndRetiresBeforeRetry(t *testing.T) {
	canonical := currentNodeReferenceForControllerTest(t, "task-resume-preparation", "node-a")
	sibling := currentNodeReferenceForControllerTest(t, "task-resume-preparation", "node-b")
	store := &currentNodeControllerStore{interrupted: []workflow.CurrentNode{
		{
			Reference:  sibling,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		},
		{
			Reference:  canonical,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingInterrupted},
		},
	}}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &countingCurrentNodeRunner{}
	attention := &currentNodeAttentionRecorder{}
	controller := newCurrentNodeControllerWithAttentionForTest(t, store, runner, authority, 2, attention)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	cause := NewTaskStartPreparationError(
		errors.New("worktree setup failed"),
		workflow.NewCurrentNodeInterruptionDetail("canonical_setup_failure", errors.New("worktree setup failed")),
	)
	var prepareCalls atomic.Int32
	failed := make(chan TaskPreparationFinalization, 1)
	_, err := controller.ResumeTaskWithPreparation(
		context.Background(),
		canonical.TaskID,
		TaskStartPreparation{
			Prepare: func(context.Context) error {
				prepareCalls.Add(1)
				return cause
			},
			Commit: func(context.Context) error { return nil },
		},
		func(finalization TaskPreparationFinalization) {
			failed <- finalization
		},
	)
	if err != nil {
		t.Fatalf("ResumeTaskWithPreparation: %v", err)
	}
	select {
	case <-failed:
	case <-time.After(3 * time.Second):
		t.Fatal("shared preparation did not finalize")
	}
	if prepareCalls.Load() != 1 {
		t.Fatalf("preparation calls = %d, want one shared failed prepare", prepareCalls.Load())
	}
	if interruption, ok := store.interruption(canonical); !ok || interruption.detail.Code != "canonical_setup_failure" {
		t.Fatalf("canonical interruption = %+v, present = %t", interruption, ok)
	}
	if interruption, ok := store.interruption(sibling); !ok || interruption.detail.Code != string(reasonCurrentNodeRuntimeStartFailed) {
		t.Fatalf("sibling interruption = %+v, present = %t", interruption, ok)
	}
	retryPrepared := make(chan struct{})
	if _, err := controller.ResumeTaskWithPreparation(
		context.Background(),
		canonical.TaskID,
		TaskStartPreparation{
			Prepare: func(context.Context) error {
				close(retryPrepared)
				return nil
			},
			Commit: func(context.Context) error { return nil },
		},
		noOpTaskPreparationFinalizer,
	); err != nil {
		t.Fatalf("immediate retry ResumeTaskWithPreparation: %v", err)
	}
	select {
	case <-retryPrepared:
	case <-time.After(3 * time.Second):
		t.Fatal("immediate retry did not register a new preparation")
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
	controller.automaticReservations[key] = currentNodeQueuedStart{reference: reference, policy: currentNodeAdmissionAutomaticAgent}
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
				controller.automaticQueue.append(currentNodeQueuedStart{
					reference: reference,
					policy:    currentNodeAdmissionAutomaticAgent,
				})
			},
		},
		{
			name: "automatic reservation",
			apply: func(controller *CurrentNodeController) {
				key, err := reference.Key()
				if err != nil {
					t.Fatalf("reference key: %v", err)
				}
				controller.automaticReservations[key] = currentNodeQueuedStart{reference: reference, policy: currentNodeAdmissionAutomaticAgent}
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
