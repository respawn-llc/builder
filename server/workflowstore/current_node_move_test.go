package workflowstore

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
)

func TestManualMoveTaskReplacesParallelCurrentNodesWithTerminal(t *testing.T) {
	fixture := startCurrentFanoutJoinTask(t)
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, definition, workflow.NodeKindTerminal)

	if _, err := fixture.store.ManualMoveTask(fixture.ctx, ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: workflow.NodeIDOf(done),
	}); err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}

	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Reference.NodeID != workflow.NodeIDOf(done) ||
		currentNodes[0].Reference.IsBranchScoped() ||
		currentNodes[0].Scheduling != nil {
		t.Fatalf("current nodes after terminal move = %+v, want one serial terminal node", currentNodes)
	}
	if _, err := fixture.store.queries.GetTaskActiveFanout(fixture.ctx, string(fixture.task.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetTaskActiveFanout after terminal move = %v, want no active fanout", err)
	}
}

func TestManualMoveTaskReplacesParallelCurrentNodesWithBacklog(t *testing.T) {
	fixture := startCurrentFanoutJoinTask(t)
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, definition, workflow.NodeKindStart)

	moved, err := fixture.store.ManualMoveTask(fixture.ctx, ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: workflow.NodeIDOf(start),
	})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if len(moved.Removed) != 2 ||
		len(moved.Created) != 1 ||
		moved.Created[0].Reference.NodeID != workflow.NodeIDOf(start) ||
		moved.Created[0].Reference.IsBranchScoped() {
		t.Fatalf("ManualMoveTask mutation = %+v, want two parallel sources replaced by serial backlog", moved.CurrentNodeMutationResult)
	}

	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Reference.NodeID != workflow.NodeIDOf(start) ||
		currentNodes[0].Reference.IsBranchScoped() ||
		currentNodes[0].Scheduling != nil {
		t.Fatalf("current nodes after backlog move = %+v, want one serial backlog node", currentNodes)
	}
}

func TestManualMoveTaskRejectsExecutableTargetWithoutReplacingParallelCurrentNodes(t *testing.T) {
	fixture := startCurrentFanoutJoinTask(t)

	_, err := fixture.store.ManualMoveTask(fixture.ctx, ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: workflow.NodeIDOf(fixture.synth),
	})
	if !errors.Is(err, ErrManualMoveExecutableTargetNeedsEdge) {
		t.Fatalf("ManualMoveTask executable target error = %v, want executable target rejection", err)
	}

	currentNodes, err := fixture.store.ListCurrentNodes(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 2 {
		t.Fatalf("current nodes after rejected executable move = %+v, want both branches unchanged", currentNodes)
	}
	if _, err := fixture.store.queries.GetTaskActiveFanout(fixture.ctx, string(fixture.task.ID)); err != nil {
		t.Fatalf("GetTaskActiveFanout after rejected executable move: %v", err)
	}
}

func TestManualMoveTaskClearsPendingApprovalsForAllReplacedParallelCurrentNodes(t *testing.T) {
	fixture := startCurrentFanoutJoinTask(t)
	seedCurrentFanoutPendingApprovals(t, fixture)
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	done := nodeByKind(t, definition, workflow.NodeKindTerminal)

	moved, err := fixture.store.ManualMoveTask(fixture.ctx, ManualMoveRequest{
		TaskID:       fixture.task.ID,
		TargetNodeID: workflow.NodeIDOf(done),
	})
	if err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}
	if len(moved.Removed) != 2 ||
		len(moved.Created) != 1 ||
		moved.Created[0].Reference.IsBranchScoped() ||
		moved.Created[0].Reference.NodeID != workflow.NodeIDOf(done) {
		t.Fatalf("ManualMoveTask mutation = %+v, want all parallel approval sources replaced by one terminal node", moved.CurrentNodeMutationResult)
	}
	approvals, err := fixture.store.ListPendingApprovals(fixture.ctx, fixture.task.ID)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending approvals after task-wide move = %+v, want none", approvals)
	}
}

func seedCurrentFanoutPendingApprovals(t *testing.T, fixture currentFanoutJoinTask) {
	t.Helper()
	definition, record, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	joinAEdge := edgeByKey(t, definition, "join_a")
	joinBEdge := edgeByKey(t, definition, "join_b")
	join := nodeByKey(t, definition, "join")
	implementationA := nodeByKey(t, definition, "impl_a")
	implementationB := nodeByKey(t, definition, "impl_b")
	groups := map[workflow.TransitionGroupID]workflow.TransitionGroup{}
	for _, group := range definition.TransitionGroups {
		groups[group.ID] = group
	}

	tx, err := fixture.store.db.BeginTx(fixture.ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	q := fixture.store.queries.WithTx(tx)
	for _, approvalInput := range []struct {
		source workflow.CurrentNode
		node   workflow.Node
		edge   workflow.Edge
		value  string
	}{
		{source: fixture.branchA, node: implementationA, edge: joinAEdge, value: "A"},
		{source: fixture.branchB, node: implementationB, edge: joinBEdge, value: "B"},
	} {
		branchKey, present := approvalInput.source.Reference.TransitionBranchKey()
		if !present {
			t.Fatalf("parallel source %v has no branch key", approvalInput.source.Reference)
		}
		targetReference, err := workflow.NewCurrentNodeReference(fixture.task.ID, workflow.NodeIDOf(join), &branchKey)
		if err != nil {
			t.Fatalf("NewCurrentNodeReference: %v", err)
		}
		target, err := workflow.NewCurrentNode(targetReference, nil, nil)
		if err != nil {
			t.Fatalf("NewCurrentNode: %v", err)
		}
		approval, err := newPendingApproval(
			approvalInput.source,
			record.Version,
			groups[approvalInput.edge.TransitionGroupID],
			workflow.NodeDisplayName(approvalInput.node),
			approvalInput.edge,
			join,
			target,
			map[string]string{"joined": approvalInput.value},
			time.UnixMilli(1_700_000_000_000).UTC(),
		)
		if err != nil {
			t.Fatalf("newPendingApproval: %v", err)
		}
		if err := insertPendingApproval(fixture.ctx, q, approval); err != nil {
			t.Fatalf("insertPendingApproval: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
}
