package workflowstore

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"core/server/workflow"
)

func TestPendingApprovalReloadDefaultsMissingCommentaryToEmpty(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	reviewEdgeID := edgeByKey(t, definition, "review").ID
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveEdgeRecord(t, req.Edges, reviewEdgeID).RequiresApproval = true
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "ready"},
		Commentary:   "New snapshot commentary",
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if completed.PendingApproval == nil {
		t.Fatal("CompleteCurrentNode did not create a pending Approval")
	}

	row, err := store.queries.GetTaskPendingApproval(ctx, completed.PendingApproval.ID.String())
	if err != nil {
		t.Fatalf("GetTaskPendingApproval: %v", err)
	}
	var legacySnapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(row.TransitionSnapshotJson), &legacySnapshot); err != nil {
		t.Fatalf("decode transition snapshot: %v", err)
	}
	delete(legacySnapshot, "commentary")
	legacyJSON, err := json.Marshal(legacySnapshot)
	if err != nil {
		t.Fatalf("encode legacy transition snapshot: %v", err)
	}
	if _, err := store.db.ExecContext(
		ctx,
		`UPDATE task_pending_approvals SET transition_snapshot_json = ? WHERE id = ?`,
		string(legacyJSON),
		completed.PendingApproval.ID.String(),
	); err != nil {
		t.Fatalf("write legacy transition snapshot: %v", err)
	}

	reloaded, err := store.PendingApproval(ctx, completed.PendingApproval.ID)
	if err != nil {
		t.Fatalf("PendingApproval: %v", err)
	}
	if reloaded.Commentary != "" {
		t.Fatalf("legacy pending Approval commentary = %q, want empty", reloaded.Commentary)
	}
	if len(reloaded.Branches) != 1 {
		t.Fatalf("reloaded pending Approval branches = %d, want one", len(reloaded.Branches))
	}
	target := reloaded.Branches[0].Target
	if target.NodeKind != workflow.NodeKindAgent {
		t.Fatalf("pending Approval target kind = %q, want agent", target.NodeKind)
	}
	if target.CurrentNode.AgentExecutionSelection == nil {
		t.Fatal("pending Approval Agent target omitted frozen execution selection")
	}
	branchRow, err := store.queries.ListTaskPendingApprovalBranches(ctx, completed.PendingApproval.ID.String())
	if err != nil {
		t.Fatalf("ListTaskPendingApprovalBranches: %v", err)
	}
	var targetSnapshot map[string]json.RawMessage
	if err := json.Unmarshal([]byte(branchRow[0].TargetSnapshotJson), &targetSnapshot); err != nil {
		t.Fatalf("decode target snapshot: %v", err)
	}
	delete(targetSnapshot, "agent_execution_selection")
	malformedTargetSnapshot, err := json.Marshal(targetSnapshot)
	if err != nil {
		t.Fatalf("encode malformed target snapshot: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE task_pending_approval_branches
SET target_snapshot_json = ?
WHERE approval_id = ?`, string(malformedTargetSnapshot), completed.PendingApproval.ID.String()); err != nil {
		t.Fatalf("write malformed target snapshot: %v", err)
	}
	if _, err := store.PendingApproval(ctx, completed.PendingApproval.ID); err == nil {
		t.Fatal("PendingApproval accepted Agent target without execution selection")
	}
}

func TestPendingApprovalFreezesSelectedAgentExecutionWithoutCatalogResolution(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	saveWorkflowGraphFixture(t, ctx, store, workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		edge := workflowGraphSaveEdgeRecord(t, req.Edges, testEdgeID("edge-audit-"+workflowID.String()))
		edge.RequiresApproval = true
		edge.AssigneeSelection = workflow.AssigneeSelectionPreviousNode
		edge.Parameters = []workflow.Parameter{{
			Key:     "role",
			Purpose: workflow.ParameterPurposeTargetAssignee,
		}}
	})
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	plan := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	review, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       plan.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("complete plan: %v", err)
	}
	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       review.Mutation.Created[0].Reference,
		TransitionID: "audit",
		OutputValues: map[string]string{"role": "reviewer"},
	})
	if err != nil {
		t.Fatalf("complete review: %v", err)
	}
	if completed.PendingApproval == nil || len(completed.PendingApproval.Branches) != 1 {
		t.Fatalf("pending approval = %+v, want one frozen branch", completed.PendingApproval)
	}
	frozen := completed.PendingApproval.Branches[0].Target.CurrentNode.AgentExecutionSelection
	if frozen == nil || frozen.Assignee != "reviewer" || frozen.Origin != workflow.AssigneeOriginTransitionSelected {
		t.Fatalf("frozen target selection = %+v, want selected reviewer", frozen)
	}

	store.roleResolver = emptyTargetAgentCatalog{}
	applied, err := store.ApplyPendingApproval(ctx, completed.PendingApproval.ID)
	if err != nil {
		t.Fatalf("ApplyPendingApproval without catalog: %v", err)
	}
	if len(applied.Mutation.Created) != 1 {
		t.Fatalf("applied mutation = %+v, want one frozen target", applied.Mutation)
	}
	appliedSelection := applied.Mutation.Created[0].AgentExecutionSelection
	if appliedSelection == nil || appliedSelection.Assignee != "reviewer" ||
		appliedSelection.Origin != workflow.AssigneeOriginTransitionSelected {
		t.Fatalf("applied target selection = %+v, want frozen reviewer", appliedSelection)
	}
}

type emptyTargetAgentCatalog struct{}

func (emptyTargetAgentCatalog) ResolveConfiguredRole(string) (workflow.TargetAgentRole, bool) {
	return workflow.TargetAgentRole{}, false
}

func (emptyTargetAgentCatalog) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return nil
}

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
		"Branch A is ready.",
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
		"Branch B is ready.",
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
  AND transition_branch_key IS NULL`, string(serialSource.TaskID), testGraphEntityBlob(t, string(serialSource.NodeID))); err != nil {
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
		var subagentRole string
		if err := tx.QueryRowContext(ctx, `SELECT subagent_role FROM workflow_nodes WHERE id = ?`, testGraphEntityBlob(t, string(branch.Reference.NodeID))).Scan(&subagentRole); err != nil {
			t.Fatalf("resolve branch Agent fallback %q: %v", branchKey, err)
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
    interrupted_at_unix_ms,
    effective_assignee,
    assignee_origin
) VALUES (?, ?, ?, '{}', '{"transition_parameters":{}}', NULL, 'ready', NULL, NULL, NULL, ?, 'configured_fallback')`,
			string(branch.Reference.TaskID),
			testGraphEntityBlob(t, string(branch.Reference.NodeID)),
			string(branchKey),
			subagentRole,
		); err != nil {
			t.Fatalf("insert branch current node %q: %v", branchKey, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit parallel source seed: %v", err)
	}
}
