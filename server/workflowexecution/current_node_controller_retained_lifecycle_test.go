package workflowexecution

import (
	"context"
	"errors"
	"testing"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func TestReactivateWorkflowSessionClassifiesDetachedSessionLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		current   []workflow.CurrentNode
		wantState TaskResumeConflictState
	}{
		{
			name: "moved current node",
			current: []workflow.CurrentNode{{
				Reference: currentNodeReferenceForControllerTest(t, "task-detached-moved", "new-node"),
			}},
			wantState: TaskResumeConflictMovedCurrentNode,
		},
		{
			name:      "finished task",
			wantState: TaskResumeConflictFinished,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			taskID := workflow.TaskID("task-detached-" + test.name)
			oldReference := currentNodeReferenceForControllerTest(t, string(taskID), "old-node")
			sessionID := runtimeids.NewSessionID()
			store := &currentNodeControllerStore{
				currentNodes:       test.current,
				sessionTaskID:      &taskID,
				sessionAssociation: &workflowstore.TaskSessionAssociation{SessionID: sessionID, CurrentNode: oldReference},
			}
			authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
			controller := newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
			t.Cleanup(func() {
				_ = controller.Close()
				_ = authority.Close(context.Background())
			})

			_, err := controller.ReactivateWorkflowSession(context.Background(), sessionID)
			var conflict *TaskResumeConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("ReactivateWorkflowSession error = %T %v, want typed conflict", err, err)
			}
			if conflict.TaskID != string(taskID) || conflict.State != test.wantState {
				t.Fatalf("conflict = %+v, want task %q state %q", conflict, taskID, test.wantState)
			}
			if len(store.resumed) != 0 {
				t.Fatalf("ResumeCurrentNode mutations = %+v, want none", store.resumed)
			}
		})
	}
}
