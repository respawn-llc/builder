package sessionruntime

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	askquestion "core/server/tools"
	"core/server/workflow"

	"github.com/google/uuid"
)

func TestAuthorityManualMoveSelectionCancelsPendingQuestionsAndClosesPromptAdmission(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	taskID := workflow.TaskID("task-manual-move-prompt-close")
	ref := WorkflowExecutionRef{
		ProjectID:   "project-manual-move",
		WorkflowID:  testsetup.WorkflowID(t, "workflow-manual-move"),
		CurrentNode: mustWorkflowCurrentNodeReference(t, taskID, "node-running"),
	}
	handle, err := startDetachedScriptExecutionForTest(t, authority, DetachedScriptExecutionRequest{
		Workflow: ref,
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
		snapshot, snapshotErr := currentScopedTaskExecutionSnapshot(authority, ref.ProjectID, ref.WorkflowID, taskID)
		if snapshotErr != nil {
			t.Fatalf("CurrentScopedTaskExecutionSnapshot: %v", snapshotErr)
		}
		if len(snapshot.Executions) == 1 && !snapshot.Executions[0].Queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("workflow execution snapshot = %+v, want one running scope", snapshot)
		}
		time.Sleep(10 * time.Millisecond)
	}

	exact := handle.(executionHandle)
	pendingErr := make(chan error, 1)
	go func() {
		_, promptErr := exact.execution.prompts.Await(context.Background(), askquestion.AskQuestionRequest{
			ToolCallID: "manual-move-pending-question",
			StepID:     uuid.NewString(),
			Question:   "Cancel me",
		})
		pendingErr <- promptErr
	}()
	deadline = time.Now().Add(3 * time.Second)
	for !exact.execution.prompts.hasPending() {
		if time.Now().After(deadline) {
			t.Fatal("manual-move Question did not become pending")
		}
		time.Sleep(10 * time.Millisecond)
	}

	admissionErr := make(chan error, 1)
	err = authority.WithWorkflowManualMoveSelection(taskID, func(selection WorkflowInterruptSelection) error {
		if len(selection.Interruptible) != 1 {
			return errors.New("want one interruptible workflow scope")
		}
		go func() {
			_, promptErr := exact.execution.prompts.Await(context.Background(), askquestion.AskQuestionRequest{
				ToolCallID: "manual-move-admission-race",
				StepID:     uuid.NewString(),
				Question:   "Must be rejected",
			})
			admissionErr <- promptErr
		}()
		return nil
	})
	if err != nil {
		t.Fatalf("WithWorkflowManualMoveSelection: %v", err)
	}
	select {
	case err := <-pendingErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending Question error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending Question was not canceled by manual-move selection")
	}
	select {
	case err := <-admissionErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("prompt admission error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("prompt admission remained blocked after manual-move selection")
	}
}

func TestAuthorityManualMoveSelectionClassifiesPendingApproval(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	authority := NewAuthority(AuthorityOptions{})
	taskID := workflow.TaskID("task-manual-move-approval")
	ref := WorkflowExecutionRef{
		ProjectID:   "project-manual-move",
		WorkflowID:  testsetup.WorkflowID(t, "workflow-manual-move-approval"),
		CurrentNode: mustWorkflowCurrentNodeReference(t, taskID, "node-running"),
	}
	handle, err := startDetachedScriptExecutionForTest(t, authority, DetachedScriptExecutionRequest{
		Workflow: ref,
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
	exact := handle.(executionHandle)
	awaitErr := make(chan error, 1)
	go func() {
		_, promptErr := exact.execution.prompts.Await(context.Background(), askquestion.AskQuestionRequest{
			ToolCallID: "manual-move-approval-race",
			StepID:     uuid.NewString(),
			Approval:   true,
			Question:   "Approve",
		})
		awaitErr <- promptErr
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !exact.execution.prompts.hasPending() {
		if time.Now().After(deadline) {
			t.Fatal("session approval did not become pending")
		}
		time.Sleep(10 * time.Millisecond)
	}

	err = authority.WithWorkflowManualMoveSelection(taskID, func(WorkflowInterruptSelection) error {
		return nil
	})
	if err != nil {
		t.Fatalf("WithWorkflowManualMoveSelection: %v", err)
	}
	select {
	case promptErr := <-awaitErr:
		if !errors.Is(promptErr, context.Canceled) {
			t.Fatalf("pending approval resolution error = %v, want context canceled", promptErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending approval did not resolve during Manual Move")
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
