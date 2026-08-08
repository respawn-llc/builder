package workflowstore

import (
	"testing"

	"core/server/workflow"
)

func TestDeriveLifecycleTaskStatusKeepsQueuedAndRunningFactsIndependent(t *testing.T) {
	taskID := workflow.TaskID("task-live")
	running, err := workflow.NewCurrentNodeReference(taskID, "node-running", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference running: %v", err)
	}
	queued, err := workflow.NewCurrentNodeReference(taskID, "node-queued", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference queued: %v", err)
	}
	status, err := DeriveLifecycleTaskStatus(
		taskID,
		[]workflow.CurrentNodeReference{running, queued},
		[]LifecycleTaskExecutionStatus{{
			CurrentNode:     running,
			WaitingQuestion: true,
		}},
	)
	if err != nil {
		t.Fatalf("DeriveLifecycleTaskStatus: %v", err)
	}
	if !status.HasRunning || !status.HasQueued || !status.WaitingQuestion || status.WaitingApproval {
		t.Fatalf("derived lifecycle Task status = %+v", status)
	}
}
