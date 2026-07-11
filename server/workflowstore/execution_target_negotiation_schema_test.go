package workflowstore

import (
	"context"
	"fmt"
	"testing"
)

func TestTaskExecutionTargetNegotiationSchemaFencesOneInitiatingAction(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	t.Run("stores one negotiation for each supported action and source snapshot", func(t *testing.T) {
		startTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Start negotiation", Body: "Body"})
		startPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(startTask.ID))
		if err != nil {
			t.Fatalf("GetActiveStartPlacementForTask: %v", err)
		}
		insertTaskExecutionTargetNegotiation(t, ctx, store, map[string]any{
			"task_id":             string(startTask.ID),
			"generation":          "target-negotiation-start",
			"workflow_id":         string(workflowID),
			"source_workspace_id": binding.WorkspaceID,
			"source_kind":         "named_ref",
			"source_named_ref":    "refs/heads/main",
			"source_commit":       "01cafe",
			"action_kind":         "start",
			"start_placement_id":  startPlacement.ID,
		})

		moveTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Move negotiation", Body: "Body"})
		movePlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(moveTask.ID))
		if err != nil {
			t.Fatalf("GetActiveStartPlacementForTask move: %v", err)
		}
		insertTaskExecutionTargetNegotiation(t, ctx, store, map[string]any{
			"task_id":                  string(moveTask.ID),
			"generation":               "target-negotiation-move",
			"workflow_id":              string(workflowID),
			"source_workspace_id":      binding.WorkspaceID,
			"source_kind":              "detached_commit",
			"source_commit":            "02cafe",
			"action_kind":              "manual_move",
			"move_source_placement_id": movePlacement.ID,
			"move_target_node_id":      "node-agent-" + string(workflowID),
		})

		approvalTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Approval negotiation", Body: "Body"})
		insertTaskExecutionTargetNegotiation(t, ctx, store, map[string]any{
			"task_id":                string(approvalTask.ID),
			"generation":             "target-negotiation-approval",
			"workflow_id":            string(workflowID),
			"source_workspace_id":    binding.WorkspaceID,
			"source_kind":            "non_git",
			"action_kind":            "approval",
			"approval_transition_id": "transition-pending",
		})
	})

	t.Run("rejects duplicate task, inconsistent task identity, and invalid source or action facts", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Invalid negotiation", Body: "Body"})
		startPlacement, err := store.queries.GetActiveStartPlacementForTask(ctx, string(task.ID))
		if err != nil {
			t.Fatalf("GetActiveStartPlacementForTask: %v", err)
		}
		valid := map[string]any{
			"task_id":             string(task.ID),
			"generation":          "target-negotiation-valid",
			"workflow_id":         string(workflowID),
			"source_workspace_id": binding.WorkspaceID,
			"source_kind":         "named_ref",
			"source_named_ref":    "refs/heads/main",
			"source_commit":       "03cafe",
			"action_kind":         "start",
			"start_placement_id":  startPlacement.ID,
		}
		insertTaskExecutionTargetNegotiation(t, ctx, store, valid)
		assertTaskExecutionTargetNegotiationRejected(t, ctx, store, valid)

		for name, mutate := range map[string]func(map[string]any){
			"mismatched workflow": func(values map[string]any) {
				values["workflow_id"] = "workflow-other"
			},
			"mismatched source workspace": func(values map[string]any) {
				values["source_workspace_id"] = "workspace-other"
			},
			"named source without ref": func(values map[string]any) {
				values["source_named_ref"] = nil
			},
			"detached source with ref": func(values map[string]any) {
				values["source_kind"] = "detached_commit"
			},
			"start action without start placement": func(values map[string]any) {
				values["start_placement_id"] = nil
			},
			"move action without both placement and target": func(values map[string]any) {
				values["action_kind"] = "manual_move"
				values["start_placement_id"] = nil
				values["move_source_placement_id"] = startPlacement.ID
			},
			"approval action with start placement": func(values map[string]any) {
				values["action_kind"] = "approval"
				values["approval_transition_id"] = "transition-pending"
			},
		} {
			t.Run(name, func(t *testing.T) {
				otherTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: name, Body: "Body"})
				values := cloneTaskExecutionTargetValues(valid)
				values["task_id"] = string(otherTask.ID)
				mutate(values)
				assertTaskExecutionTargetNegotiationRejected(t, ctx, store, values)
			})
		}
	})
}

func insertTaskExecutionTargetNegotiation(t *testing.T, ctx context.Context, store *Store, values map[string]any) {
	t.Helper()
	if err := executeTaskExecutionTargetNegotiationInsert(ctx, store, values); err != nil {
		t.Fatalf("insert task execution target negotiation: %v", err)
	}
}

func assertTaskExecutionTargetNegotiationRejected(t *testing.T, ctx context.Context, store *Store, values map[string]any) {
	t.Helper()
	if err := executeTaskExecutionTargetNegotiationInsert(ctx, store, values); err == nil {
		t.Fatalf("insert task execution target negotiation succeeded with invalid values: %s", fmt.Sprint(values))
	}
}

func executeTaskExecutionTargetNegotiationInsert(ctx context.Context, store *Store, values map[string]any) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO task_execution_target_negotiations (
			task_id, generation, workflow_id, source_workspace_id,
			source_kind, source_named_ref, source_commit, recovery_cause,
			action_kind, start_placement_id, move_source_placement_id,
			move_target_node_id, approval_transition_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		values["task_id"],
		values["generation"],
		values["workflow_id"],
		values["source_workspace_id"],
		values["source_kind"],
		values["source_named_ref"],
		values["source_commit"],
		values["recovery_cause"],
		values["action_kind"],
		values["start_placement_id"],
		values["move_source_placement_id"],
		values["move_target_node_id"],
		values["approval_transition_id"],
	)
	return err
}
