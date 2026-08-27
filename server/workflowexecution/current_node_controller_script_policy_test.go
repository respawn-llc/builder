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
	"core/server/workflowruntime"
)

func TestCurrentNodeControllerFailedScriptDoesNotReleaseAgentCapacity(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	occupyingAgent := currentNodeReferenceForControllerTest(t, "task-failed-script-agent", "node-occupying-agent")
	failedScript := currentNodeReferenceForControllerTest(t, "task-failed-script", "node-failed-script")
	queuedAgent := currentNodeReferenceForControllerTest(t, "task-failed-script-queued-agent", "node-queued-agent")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &selectiveScriptFailureRunner{
		authority: authority,
		failed:    failedScript,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 2),
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
		CurrentNode: occupyingAgent,
		NodeKind:    workflow.NodeKindAgent,
	}}))
	select {
	case started := <-runner.started:
		if !started.Equal(occupyingAgent) {
			t.Fatalf("occupying Agent start = %v, want %v", started, occupyingAgent)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("occupying Agent did not start")
	}
	waitForRunningCurrentNode(t, authority, occupyingAgent)
	controller.enqueueStarts(automaticQueuedStarts([]CurrentNodeAutomaticIntent{
		{CurrentNode: failedScript, NodeKind: workflow.NodeKindScript},
		{CurrentNode: queuedAgent, NodeKind: workflow.NodeKindAgent},
	}))
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(failedScript)
		return interrupted
	}, "failed Script was not interrupted")
	occupyingHandle, live := authority.ExecutionByScope(singleLiveScope(t, authority, occupyingAgent))
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

func TestCurrentNodeControllerFailedReservationReleasesAgentCapacity(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	failed := currentNodeReferenceForControllerTest(t, "task-failed-reservation", "node-failed")
	next := currentNodeReferenceForControllerTest(t, "task-next-after-failed-reservation", "node-next")
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	runner := &recordingScriptRunner{
		authority: authority,
		command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
		started: make(chan workflow.CurrentNodeReference, 1),
		agents: map[workflow.CurrentNodeReference]struct{}{
			failed: {},
			next:   {},
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

	controller.enqueueStarts([]currentNodeQueuedStart{{
		reference: failed,
		policy:    currentNodeAdmissionAutomaticAgent,
		assignmentSteer: completedCurrentNodeAssignmentSteer{
			err: errors.New("assignment preparation failed"),
		},
	}})
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, interrupted := store.interruption(failed)
		return interrupted
	}, "failed reservation was not interrupted")

	controller.enqueueStarts(automaticQueuedStarts([]CurrentNodeAutomaticIntent{{
		CurrentNode: next,
		NodeKind:    workflow.NodeKindAgent,
	}}))
	select {
	case started := <-runner.started:
		if !started.Equal(next) {
			t.Fatalf("next Agent start = %v, want %v", started, next)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("next Agent did not start after failed reservation released capacity")
	}
}

type selectiveScriptFailureRunner struct {
	authority *sessionruntime.Authority
	command   sessionruntime.ScriptCommand
	failed    workflow.CurrentNodeReference
	started   chan workflow.CurrentNodeReference
}

func (*selectiveScriptFailureRunner) UsesScriptPublication(workflow.CurrentNodeReference) bool {
	return true
}

func (r *selectiveScriptFailureRunner) PublishCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentSteer, lease workflowExecutionStart, _ workflowruntime.Controller) error {
	_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

		Command: r.command,
	})
	if err == nil {
		r.started <- reference
	}
	return err
}

func (r *selectiveScriptFailureRunner) PrepareCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery) error {
	if reference.Equal(r.failed) {
		return errors.New("script start failed")
	}
	return nil
}

type finalizingBeforeLiveRunner struct {
	authority *sessionruntime.Authority
	fast      workflow.CurrentNodeReference
	shellPath string
	started   chan workflow.CurrentNodeReference
}

func (*finalizingBeforeLiveRunner) UsesScriptPublication(workflow.CurrentNodeReference) bool {
	return false
}

func (r *finalizingBeforeLiveRunner) PublishCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentSteer,
	lease workflowExecutionStart,
	_ workflowruntime.Controller,
) error {
	command := sessionruntime.ScriptCommand{
		Path: r.shellPath,
		Args: []string{"-c", "while :; do sleep 1; done"},
	}
	if reference.Equal(r.fast) {
		command.Args = []string{"-c", "exit 0"}
		_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{
			Command: command,
		})
		return err
	}
	_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

		Command: command,
	})
	if err == nil {
		r.started <- reference
	}
	return err
}
