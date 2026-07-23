package workflowattention

import (
	"reflect"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
)

func TestStoreProjectionSlicesDelegateToCanonicalSingleProjection(t *testing.T) {
	detail := `{"error":"model failed"}`
	approval := workflowstore.ApprovalTransitionProjection{
		TransitionID:     "transition-1",
		ProjectID:        "project-1",
		WorkflowID:       "workflow-1",
		TaskID:           workflow.TaskID("task-1"),
		TaskShortID:      "WOR-1",
		TaskTitle:        "Approval task",
		SourceRunID:      workflow.RunID("run-approval"),
		SessionID:        "session-approval",
		OccurredAtUnixMs: 101,
	}
	interrupted := workflowstore.InterruptedRunAttentionProjection{
		ProjectID:              "project-2",
		WorkflowID:             "workflow-2",
		TaskID:                 workflow.TaskID("task-2"),
		TaskShortID:            "WOR-2",
		TaskTitle:              "Interrupted task",
		RunID:                  workflow.RunID("run-interrupted"),
		SessionID:              "session-interrupted",
		InterruptionReason:     "workflow_runtime_failed",
		InterruptionDetailJSON: &detail,
		OccurredAtUnixMs:       202,
	}

	approvalSlice := ApprovalProjections([]workflowstore.ApprovalTransitionProjection{approval})
	if len(approvalSlice) != 1 || !reflect.DeepEqual(approvalSlice[0], ApprovalProjectionFromStore(approval)) {
		t.Fatalf("approval slice = %+v, want canonical single projection", approvalSlice)
	}
	interruptedSlice := InterruptedRunProjections([]workflowstore.InterruptedRunAttentionProjection{interrupted})
	if len(interruptedSlice) != 1 || !reflect.DeepEqual(interruptedSlice[0], InterruptedRunProjectionFromStore(interrupted)) {
		t.Fatalf("interrupted slice = %+v, want canonical single projection", interruptedSlice)
	}
}
