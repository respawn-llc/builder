package workflowstore

import (
	"context"
	"testing"
	"time"

	"core/server/workflow"
)

func TestListPendingApprovalsKeepsParallelSourcesIndependent(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_a")
	requireApprovalOnWorkflowEdge(t, ctx, store, workflowID, "join_b")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	implementationA := nodeByKey(t, definition, "impl_a")
	implementationB := nodeByKey(t, definition, "impl_b")
	join := nodeByKey(t, definition, "join")
	joinAEdge := edgeByKey(t, definition, "join_a")
	joinBEdge := edgeByKey(t, definition, "join_b")
	groups := map[workflow.TransitionGroupID]workflow.TransitionGroup{}
	for _, group := range definition.TransitionGroups {
		groups[group.ID] = group
	}

	branchA := workflow.TransitionBranchKey("split_a")
	branchB := workflow.TransitionBranchKey("split_b")
	sourceAReference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(implementationA), &branchA)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference branch A: %v", err)
	}
	sourceBReference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(implementationB), &branchB)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference branch B: %v", err)
	}
	sourceA, err := workflow.NewCurrentNode(sourceAReference, nil, &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady})
	if err != nil {
		t.Fatalf("NewCurrentNode branch A: %v", err)
	}
	sourceB, err := workflow.NewCurrentNode(sourceBReference, nil, &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady})
	if err != nil {
		t.Fatalf("NewCurrentNode branch B: %v", err)
	}
	targetAReference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(join), &branchA)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference join A: %v", err)
	}
	targetBReference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(join), &branchB)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference join B: %v", err)
	}
	targetA, err := workflow.NewCurrentNode(targetAReference, nil, nil)
	if err != nil {
		t.Fatalf("NewCurrentNode join A: %v", err)
	}
	targetB, err := workflow.NewCurrentNode(targetBReference, nil, nil)
	if err != nil {
		t.Fatalf("NewCurrentNode join B: %v", err)
	}

	seedParallelCurrentApprovalSources(t, ctx, store, task.ID, started.Mutation.Created[0].Reference, sourceA, sourceB)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx pending approvals: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := store.queries.WithTx(tx)
	approvalA, err := newPendingApproval(
		sourceA,
		record.Version,
		groups[joinAEdge.TransitionGroupID],
		workflow.NodeDisplayName(implementationA),
		joinAEdge,
		join,
		targetA,
		map[string]string{"joined": "A"},
		time.UnixMilli(1_700_000_000_001).UTC(),
	)
	if err != nil {
		t.Fatalf("newPendingApproval branch A: %v", err)
	}
	approvalB, err := newPendingApproval(
		sourceB,
		record.Version,
		groups[joinBEdge.TransitionGroupID],
		workflow.NodeDisplayName(implementationB),
		joinBEdge,
		join,
		targetB,
		map[string]string{"joined": "B"},
		time.UnixMilli(1_700_000_000_002).UTC(),
	)
	if err != nil {
		t.Fatalf("newPendingApproval branch B: %v", err)
	}
	if err := insertPendingApproval(ctx, q, approvalA); err != nil {
		t.Fatalf("insertPendingApproval branch A: %v", err)
	}
	if err := insertPendingApproval(ctx, q, approvalB); err != nil {
		t.Fatalf("insertPendingApproval branch B: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit pending approvals: %v", err)
	}

	approvals, err := store.ListPendingApprovals(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(approvals) != 2 {
		t.Fatalf("pending approvals = %+v, want two independent branch approvals", approvals)
	}
	for _, source := range []workflow.CurrentNodeReference{sourceAReference, sourceBReference} {
		var approval *workflow.PendingApproval
		for index := range approvals {
			if approvals[index].Source.Equal(source) {
				approval = &approvals[index]
				break
			}
		}
		if approval == nil || !approval.Source.IsBranchScoped() || len(approval.Branches) != 1 {
			t.Fatalf("branch approval for source %v = %+v, want one independent pending approval", source, approval)
		}
		eligible, err := store.IsCurrentNodeExecutionEligible(ctx, source)
		if err != nil {
			t.Fatalf("IsCurrentNodeExecutionEligible %v: %v", source, err)
		}
		if eligible {
			t.Fatalf("branch source %v remained eligible while pending approval exists", source)
		}
	}
}

func seedParallelCurrentApprovalSources(
	t *testing.T,
	ctx context.Context,
	store *Store,
	taskID workflow.TaskID,
	serialSource workflow.CurrentNodeReference,
	sourceA workflow.CurrentNode,
	sourceB workflow.CurrentNode,
) {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx parallel source seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
DELETE FROM task_current_nodes
WHERE task_id = ?
  AND node_id = ?
  AND transition_branch_key IS NULL`, string(serialSource.TaskID), string(serialSource.NodeID)); err != nil {
		t.Fatalf("delete serial current node: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO task_active_fanouts (task_id) VALUES (?)`, string(taskID)); err != nil {
		t.Fatalf("insert task active fanout: %v", err)
	}
	for _, branch := range []workflow.CurrentNode{sourceA, sourceB} {
		branchKey, present := branch.Reference.TransitionBranchKey()
		if !present {
			t.Fatalf("parallel source %v has no branch key", branch.Reference)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_active_fanout_branches (
    task_id,
    transition_branch_key,
    arrival_state,
    arrival_values_json
) VALUES (?, ?, 'pending', NULL)`, string(taskID), string(branchKey)); err != nil {
			t.Fatalf("insert active fanout branch %q: %v", branchKey, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_current_nodes (
    task_id,
    node_id,
    transition_branch_key,
    current_input_values_json,
    prior_node_values_json,
    session_id,
    scheduling_state,
    interruption_reason,
    interruption_detail_json,
    interrupted_at_unix_ms
) VALUES (?, ?, ?, '{}', '{}', NULL, 'ready', NULL, NULL, NULL)`,
			string(branch.Reference.TaskID),
			string(branch.Reference.NodeID),
			string(branchKey),
		); err != nil {
			t.Fatalf("insert branch current node %q: %v", branchKey, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit parallel source seed: %v", err)
	}
}
