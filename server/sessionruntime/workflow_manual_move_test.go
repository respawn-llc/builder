package sessionruntime

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	askquestion "core/server/tools"
	"core/server/workflow"

	"github.com/google/uuid"
)

func TestAuthorityManualMoveSelectionClosesPromptAdmissionBeforeRelease(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	taskID := workflow.TaskID("task-manual-move-prompt-close")
	ref := WorkflowExecutionRef{
		ProjectID:   "project-manual-move",
		WorkflowID:  "workflow-manual-move",
		CurrentNode: mustWorkflowCurrentNodeReference(t, taskID, "node-running"),
	}
	lease, err := authority.NewWorkflowExecutionLease(ref)
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	handle, err := authority.StartScriptExecution(context.Background(), ScriptExecutionRequest{
		Workflow: &lease,
		Command: ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	t.Cleanup(func() {
		handle.RequestStop()
		_, _ = handle.Wait(context.Background())
		_ = authority.Close(context.Background())
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, stateErr := authority.CurrentWorkflowTaskExecutionState(taskID)
		if stateErr != nil {
			t.Fatalf("CurrentWorkflowTaskExecutionState: %v", stateErr)
		}
		if state.Running == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow execution state = %+v, want one running scope", state)
		}
		time.Sleep(10 * time.Millisecond)
	}

	awaitErr := make(chan error, 1)
	err = authority.WithWorkflowManualMoveSelection(taskID, func(selection WorkflowInterruptSelection) error {
		if len(selection.Interruptible) != 1 {
			return errors.New("want one interruptible workflow scope")
		}
		exact := handle.(executionHandle)
		go func() {
			_, promptErr := exact.execution.prompts.Await(context.Background(), askquestion.AskQuestionRequest{
				ID:       "manual-move-admission-race",
				StepID:   uuid.NewString(),
				Question: "Must be rejected",
			})
			awaitErr <- promptErr
		}()
		return nil
	})
	if err != nil {
		t.Fatalf("WithWorkflowManualMoveSelection: %v", err)
	}
	select {
	case err := <-awaitErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("prompt admission error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt admission remained blocked after manual-move selection")
	}
}

func mustWorkflowCurrentNodeReference(t *testing.T, taskID workflow.TaskID, nodeID workflow.NodeID) workflow.CurrentNodeReference {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(taskID, nodeID, nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	return reference
}
