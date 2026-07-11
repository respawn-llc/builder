package workflowstore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"core/server/workflow"
)

func TestTaskExecutionTargetSchemaEnforcesMaterializedTargetInvariants(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	t.Run("none locks without Git or provisioning facts", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "No worktree",
			Body:       "Body",
		})
		insertTaskExecutionTarget(t, ctx, store, task.ID, map[string]any{
			"policy":               "none",
			"state":                "locked",
			"setup_state":          "not_applicable",
			"recovery_disposition": "available",
		})
		assertTaskExecutionTargetInsertRejected(t, ctx, store, map[string]any{
			"task_id":              string(task.ID),
			"policy":               "none",
			"state":                "locked",
			"setup_state":          "not_applicable",
			"recovery_disposition": "available",
		})

		invalidTask := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Invalid no worktree",
			Body:       "Body",
		})
		assertTaskExecutionTargetInsertRejected(t, ctx, store, map[string]any{
			"task_id":              string(invalidTask.ID),
			"policy":               "none",
			"resolved_commit":      "01cafe",
			"state":                "locked",
			"setup_state":          "not_applicable",
			"recovery_disposition": "available",
		})

		var metadataJSON string
		if err := store.db.QueryRowContext(ctx, `SELECT metadata_json FROM tasks WHERE id = ?`, task.ID).Scan(&metadataJSON); err != nil {
			t.Fatalf("load task metadata: %v", err)
		}
		var metadata map[string]json.RawMessage
		if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
			t.Fatalf("decode task metadata: %v", err)
		}
		if _, hasExecutionTarget := metadata["execution_target"]; hasExecutionTarget {
			t.Fatal("task metadata must not contain execution target facts")
		}
	})

	t.Run("git targets require a resolved source, matching setup generation, and nonempty custom ref", func(t *testing.T) {
		valid := func(taskID workflow.TaskID) map[string]any {
			return map[string]any{
				"task_id":                       string(taskID),
				"policy":                        "custom_ref",
				"requested_custom_ref":          "release/2026.07",
				"resolved_source_kind":          "named_ref",
				"resolved_source_ref":           "refs/heads/release/2026.07",
				"resolved_commit":               "01cafe",
				"state":                         "initial_provisioning",
				"provisioning_generation":       "target-provision-1",
				"setup_provisioning_generation": "target-provision-1",
				"setup_state":                   "pending",
				"recovery_disposition":          "available",
			}
		}

		t.Run("accepts valid target", func(t *testing.T) {
			task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Custom target", Body: "Body"})
			insertTaskExecutionTarget(t, ctx, store, task.ID, valid(task.ID))
		})

		t.Run("accepts named default branch target after setup", func(t *testing.T) {
			task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Default target", Body: "Body"})
			insertTaskExecutionTarget(t, ctx, store, task.ID, map[string]any{
				"policy":                        "default_branch",
				"resolved_source_kind":          "named_ref",
				"resolved_source_ref":           "refs/remotes/origin/HEAD",
				"resolved_commit":               "03cafe",
				"state":                         "locked",
				"provisioning_generation":       "target-provision-3",
				"setup_provisioning_generation": "target-provision-3",
				"setup_state":                   "succeeded",
				"recovery_disposition":          "available",
			})
		})

		for name, mutate := range map[string]func(map[string]any){
			"rejects ask": func(values map[string]any) {
				values["policy"] = "ask"
			},
			"rejects missing resolved commit": func(values map[string]any) {
				values["resolved_commit"] = nil
			},
			"rejects detached source with named ref": func(values map[string]any) {
				values["resolved_source_kind"] = "detached_commit"
			},
			"rejects mismatched setup generation": func(values map[string]any) {
				values["setup_provisioning_generation"] = "target-provision-2"
			},
			"rejects empty custom ref": func(values map[string]any) {
				values["requested_custom_ref"] = " "
			},
		} {
			t.Run(name, func(t *testing.T) {
				task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: name, Body: "Body"})
				values := valid(task.ID)
				mutate(values)
				assertTaskExecutionTargetInsertRejected(t, ctx, store, values)
			})
		}
	})

	t.Run("claim and recovery facts are paired and typed", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Recovery target", Body: "Body"})
		values := map[string]any{
			"task_id":                       string(task.ID),
			"policy":                        "head",
			"resolved_source_kind":          "detached_commit",
			"resolved_commit":               "02cafe",
			"state":                         "locked_reprovisioning",
			"provisioning_generation":       "target-provision-2",
			"setup_provisioning_generation": "target-provision-2",
			"setup_state":                   "failed",
			"active_claim_generation":       "target-claim-1",
			"active_claim_phase":            "recovery_queued",
			"recovery_disposition":          "manual_recovery",
			"recovery_cause":                "missing_administrative_ownership",
			"exact_branch_observation":      "02cafe",
			"linked_worktree_common_dir":    "/repo/.git",
			"linked_worktree_admin_entry":   "worktrees/WOR-1",
			"linked_worktree_gitdir":        "/tmp/worktree/.git",
			"linked_worktree_head_ref":      "refs/heads/WOR-1",
			"expected_detachment_commit":    "02cafe",
		}
		insertTaskExecutionTarget(t, ctx, store, task.ID, values)

		for _, phase := range []string{"materializing", "recovering"} {
			t.Run("accepts "+phase+" claim", func(t *testing.T) {
				claimedTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: phase + " target", Body: "Body"})
				claimed := cloneTaskExecutionTargetValues(values)
				claimed["task_id"] = string(claimedTask.ID)
				claimed["active_claim_phase"] = phase
				insertTaskExecutionTarget(t, ctx, store, claimedTask.ID, claimed)
			})
		}

		for name, mutate := range map[string]func(map[string]any){
			"rejects unpaired claim phase": func(values map[string]any) {
				values["active_claim_generation"] = nil
			},
			"rejects unknown claim phase": func(values map[string]any) {
				values["active_claim_phase"] = "unknown"
			},
			"rejects available recovery cause": func(values map[string]any) {
				values["recovery_disposition"] = "available"
			},
			"rejects manual recovery without cause": func(values map[string]any) {
				values["recovery_cause"] = nil
			},
			"rejects partial linked worktree ownership": func(values map[string]any) {
				values["linked_worktree_gitdir"] = nil
			},
		} {
			t.Run(name, func(t *testing.T) {
				task := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: name, Body: "Body"})
				candidate := cloneTaskExecutionTargetValues(values)
				candidate["task_id"] = string(task.ID)
				mutate(candidate)
				assertTaskExecutionTargetInsertRejected(t, ctx, store, candidate)
			})
		}
	})
}

func cloneTaskExecutionTargetValues(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func insertTaskExecutionTarget(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID, values map[string]any) {
	t.Helper()
	values["task_id"] = string(taskID)
	if err := executeTaskExecutionTargetInsert(ctx, store, values); err != nil {
		t.Fatalf("insert task execution target %q: %v", taskID, err)
	}
}

func assertTaskExecutionTargetInsertRejected(t *testing.T, ctx context.Context, store *Store, values map[string]any) {
	t.Helper()
	if err := executeTaskExecutionTargetInsert(ctx, store, values); err == nil {
		t.Fatalf("insert task execution target succeeded with invalid values: %s", fmt.Sprint(values))
	}
}

func executeTaskExecutionTargetInsert(ctx context.Context, store *Store, values map[string]any) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO task_execution_targets (
			task_id, policy, requested_custom_ref, resolved_source_kind,
			resolved_source_ref, resolved_commit, state, provisioning_generation,
			setup_provisioning_generation, setup_state, active_claim_generation,
			active_claim_phase, recovery_disposition, recovery_cause,
			exact_branch_observation, linked_worktree_common_dir,
			linked_worktree_admin_entry, linked_worktree_gitdir,
			linked_worktree_head_ref, expected_detachment_commit
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		values["task_id"],
		values["policy"],
		values["requested_custom_ref"],
		values["resolved_source_kind"],
		values["resolved_source_ref"],
		values["resolved_commit"],
		values["state"],
		values["provisioning_generation"],
		values["setup_provisioning_generation"],
		values["setup_state"],
		values["active_claim_generation"],
		values["active_claim_phase"],
		values["recovery_disposition"],
		values["recovery_cause"],
		values["exact_branch_observation"],
		values["linked_worktree_common_dir"],
		values["linked_worktree_admin_entry"],
		values["linked_worktree_gitdir"],
		values["linked_worktree_head_ref"],
		values["expected_detachment_commit"],
	)
	return err
}
