package workflowexecution

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
)

func TestAgentTaskStartPublishesQueuedRunWithDurableCurrentNode(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-agent-start-publication", "node-agent")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		close(runner.release)
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	capture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("CaptureLifecycle: %v", err)
	}
	defer func() { _ = capture.Close() }()
	queued := capture.QueuedCurrentNodes(reference.TaskID)
	if len(queued) != 1 || !queued[0].Equal(reference) {
		t.Fatalf("queued Current Nodes = %+v, want %v", queued, reference)
	}
}

func TestAdmittedTransitionPublishesMatchingExactExecution(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-admitted-publication", "node-script")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{
			Mutation: workflow.CurrentNodeMutationResult{Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}}},
			CreatedNodeKind: workflow.NodeKindScript,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	started := make(chan workflow.CurrentNodeReference, 1)
	controller := newCurrentNodeControllerForTest(t, store, &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: started,
	}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		capture, err := controller.CaptureLifecycle(context.Background())
		if err != nil {
			return false
		}
		defer func() { _ = capture.Close() }()
		exact := capture.ExactExecutions(reference.TaskID)
		return len(exact) == 1 && exact[0].CurrentNode.Equal(reference)
	}, "admitted Current Node did not publish its Exact Execution Scope")
}

func TestQueuedInterruptionPublishesStoppedLifecycle(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-queued-interruption-publication", "node-agent")
	liveReference := currentNodeReferenceForControllerTest(t, string(reference.TaskID), "node-live-script")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	controller = newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	preparationEntered := make(chan struct{})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(ctx context.Context) error {
		close(preparationEntered)
		<-ctx.Done()
		return context.Cause(ctx)
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	select {
	case <-preparationEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("queued Run did not enter preparation")
	}
	lease, err := authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   "project-test",
		WorkflowID:  currentNodeControllerTestWorkflowID,
		CurrentNode: liveReference,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	_, err = authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	controller.mu.Lock()
	installLiveCurrentNodeRunLockedForTest(
		controller,
		liveReference,
		workflow.NodeKindScript,
		currentNodeAdmissionExplicitOverride,
		lease,
	)
	controller.mu.Unlock()
	lease.Release()
	waitForRunningCurrentNode(t, authority, liveReference)
	if err := controller.Interrupt(context.Background(), InterruptSelector{TaskID: reference.TaskID}); err != nil {
		t.Fatalf("Interrupt queued Run: %v", err)
	}
	capture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("CaptureLifecycle: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if queued := capture.QueuedCurrentNodes(reference.TaskID); len(queued) != 0 {
		t.Fatalf("queued Current Nodes after interruption = %+v, want none", queued)
	}
	if exact := capture.ExactExecutions(reference.TaskID); len(exact) != 0 {
		t.Fatalf("Exact Executions after interruption = %+v, want none", exact)
	}
}

func TestLiveInterruptionRemovesMatchingExactExecutionAndRun(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh unavailable: %v", err)
	}
	reference := currentNodeReferenceForControllerTest(t, "task-live-interruption-publication", "node-script")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{
			Mutation: workflow.CurrentNodeMutationResult{Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}}},
			CreatedNodeKind: workflow.NodeKindScript,
		},
	}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	controller = newCurrentNodeControllerForTest(t, store, &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 1),
	}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	waitForRunningCurrentNode(t, authority, reference)
	if err := controller.Interrupt(context.Background(), InterruptSelector{TaskID: reference.TaskID}); err != nil {
		t.Fatalf("Interrupt live Run: %v", err)
	}
	capture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("CaptureLifecycle: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if queued := capture.QueuedCurrentNodes(reference.TaskID); len(queued) != 0 {
		t.Fatalf("queued Current Nodes after live interruption = %+v, want none", queued)
	}
	if exact := capture.ExactExecutions(reference.TaskID); len(exact) != 0 {
		t.Fatalf("Exact Executions after live interruption = %+v, want none", exact)
	}
}

func TestAdmissionFailurePublishesStoppedLifecycle(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-admission-failure-publication", "node-agent")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
			Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}},
		}},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller := newCurrentNodeControllerForTest(t, store, failingCurrentNodeRunner{
		cause: errors.New("launch failed"),
	}, authority, 1)
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		key, err := reference.Key()
		return err == nil && store.interruptionCalls[key] == 1
	}, "admission failure was not durably interrupted")
	capture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("CaptureLifecycle: %v", err)
	}
	defer func() { _ = capture.Close() }()
	if queued := capture.QueuedCurrentNodes(reference.TaskID); len(queued) != 0 {
		t.Fatalf("queued Current Nodes after admission failure = %+v, want none", queued)
	}
	if exact := capture.ExactExecutions(reference.TaskID); len(exact) != 0 {
		t.Fatalf("Exact Executions after admission failure = %+v, want none", exact)
	}
}

func TestScriptTaskStartPublishesQueuedRunWithDurableCurrentNode(t *testing.T) {
	reference := currentNodeReferenceForControllerTest(t, "task-script-start-publication", "node-script")
	store := &currentNodeControllerStore{
		started: workflowstore.StartTaskResult{
			Mutation: workflow.CurrentNodeMutationResult{Created: []workflow.CurrentNode{{
				Reference:  reference,
				Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
			}}},
			CreatedNodeKind: workflow.NodeKindScript,
		},
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &blockingCurrentNodeRunner{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	controller := newCurrentNodeControllerForTest(t, store, runner, authority, 1)
	t.Cleanup(func() {
		close(runner.release)
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	if _, err := controller.StartTask(context.Background(), reference.TaskID, func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	capture, err := controller.CaptureLifecycle(context.Background())
	if err != nil {
		t.Fatalf("CaptureLifecycle: %v", err)
	}
	defer func() { _ = capture.Close() }()
	queued := capture.QueuedCurrentNodes(reference.TaskID)
	if len(queued) != 1 || !queued[0].Equal(reference) {
		t.Fatalf("queued Current Nodes = %+v, want %v", queued, reference)
	}
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("Current Node key: %v", err)
	}
	controller.mu.Lock()
	run, exists := controller.runs.get(key)
	controller.mu.Unlock()
	if !exists || run.nodeKind != workflow.NodeKindScript {
		t.Fatalf("published Run = %+v, want Script Run", run)
	}
}
