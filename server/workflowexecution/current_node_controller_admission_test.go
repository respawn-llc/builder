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
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})

	resumed, err := controller.ResumeTask(context.Background(), reference.TaskID)
	if err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	if len(resumed) != 1 || !resumed[0].Reference.Equal(reference) {
		t.Fatalf("resumed Current Nodes = %+v, want %v", resumed, reference)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return len(runner.promptDeliveries()) == 1
	}, "resumed Current Node did not reach runner")
	if deliveries := runner.promptDeliveries(); len(deliveries) != 1 ||
		deliveries[0] != workflowruntime.TaskPromptDeliveryResume {
		t.Fatalf("runner prompt deliveries = %+v, want Resume", deliveries)
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
	permit := NewMutationPermit()
	controller, err := NewCurrentNodeController(store, runner, authority, permit, CurrentNodeControllerConfig{
		AgentConcurrency:  1,
		AssignmentSteerer: noOpCurrentNodeAssignmentSteerer{},
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
		_, err := controller.StartTaskWithPreparation(context.Background(), taskID, LaunchPreparation{
			Kind: LaunchPreparationEstablishedRoot,
		})
		startDone <- err
	}()
	select {
	case <-store.startTaskStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("task start did not enter durable mutation")
	}
	deleteCheck := make(chan error, 1)
	go func() {
		deleteCheck <- permit.Run(context.Background(), func(context.Context) error {
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
			t.Fatalf("StartTaskWithPreparation: %v", err)
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
	controller.automaticReservations[key] = currentNodeQueuedStart{
		reference:         reference,
		launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
		policy:            currentNodeAdmissionAutomaticAgent,
	}
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
				controller.automaticQueue.append(currentNodeQueuedStart{
					reference:         reference,
					launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
					policy:            currentNodeAdmissionAutomaticAgent,
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
				controller.automaticReservations[key] = currentNodeQueuedStart{
					reference:         reference,
					launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
					policy:            currentNodeAdmissionAutomaticAgent,
				}
			},
		},
		{
			name: "retirement held intent",
			apply: func(controller *CurrentNodeController) {
				controller.heldStarts[runtimeids.NewExecutionScopeID()] = []currentNodeQueuedStart{{
					reference:         reference,
					launchPreparation: LaunchPreparation{Kind: LaunchPreparationEstablishedRoot},
					policy:            currentNodeAdmissionAutomaticAgent,
				}}
			},
		},
		{
			name: "admission gate",
			apply: func(controller *CurrentNodeController) {
				key, err := reference.Key()
				if err != nil {
					t.Fatalf("reference key: %v", err)
				}
				controller.gates[key] = currentNodeAdmissionGate{reference: reference}
			},
		},
		{
			name: "live scope",
			apply: func(controller *CurrentNodeController) {
				scopeID := runtimeids.NewExecutionScopeID()
				key, err := reference.Key()
				if err != nil {
					t.Fatalf("reference key: %v", err)
				}
				controller.live[scopeID] = currentNodeLiveScope{reference: reference}
				controller.liveByNode[key] = scopeID
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
