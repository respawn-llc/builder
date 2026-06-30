package workflowview

import (
	"strings"
	"testing"

	"core/server/workflow"
	"core/shared/serverapi"
)

func TestBoardColumnsOrderVisibleReworkCycleByStructuralEntry(t *testing.T) {
	def := serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{ID: "workflow-1"},
		NodeGroups: []serverapi.WorkflowNodeGroup{
			{GroupID: "group-code-review-parallel", GroupKey: "code_review_parallel", DisplayName: "Code Review Parallel"},
		},
		Nodes: []serverapi.WorkflowNode{
			{ID: "node-backlog", Key: "backlog", Kind: string(workflow.NodeKindStart), DisplayName: "Backlog"},
			{ID: "node-implementation", Key: "implementation", Kind: string(workflow.NodeKindAgent), DisplayName: "Implementation"},
			{ID: "node-approval-gate", Key: "approval_gate", Kind: string(workflow.NodeKindAgent), DisplayName: "Approval gate"},
			{ID: "node-code-review", Key: "code_review", Kind: string(workflow.NodeKindAgent), DisplayName: "Code Review", GroupID: "group-code-review-parallel"},
			{ID: "node-qa", Key: "qa", Kind: string(workflow.NodeKindAgent), DisplayName: "QA", GroupID: "group-code-review-parallel"},
			{ID: "node-code-review-join", Key: "code_review_parallel_join", Kind: string(workflow.NodeKindJoin), DisplayName: "Code Review Join", GroupID: "group-code-review-parallel"},
			{ID: "node-pr-autoreview", Key: "pr_autoreview", Kind: string(workflow.NodeKindAgent), DisplayName: "PR Autoreview"},
			{ID: "node-done", Key: "done", Kind: string(workflow.NodeKindTerminal), DisplayName: "Done"},
		},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{
			{ID: "transition-backlog", SourceNodeID: "node-backlog", TransitionID: "start"},
			{ID: "transition-implementation", SourceNodeID: "node-implementation", TransitionID: "review"},
			{ID: "transition-code-review", SourceNodeID: "node-code-review", TransitionID: "join"},
			{ID: "transition-qa", SourceNodeID: "node-qa", TransitionID: "join"},
			{ID: "transition-code-review-join", SourceNodeID: "node-code-review-join", TransitionID: "approval_gate"},
			{ID: "transition-approval-approved", SourceNodeID: "node-approval-gate", TransitionID: "approved"},
			{ID: "transition-approval-rejected", SourceNodeID: "node-approval-gate", TransitionID: "rejected"},
			{ID: "transition-pr-autoreview", SourceNodeID: "node-pr-autoreview", TransitionID: "done"},
		},
		Edges: []serverapi.WorkflowEdge{
			{ID: "edge-start", TransitionGroupID: "transition-backlog", Key: "start", TargetNodeID: "node-implementation"},
			{ID: "edge-code-review", TransitionGroupID: "transition-implementation", Key: "code_review", TargetNodeID: "node-code-review"},
			{ID: "edge-qa", TransitionGroupID: "transition-implementation", Key: "qa", TargetNodeID: "node-qa"},
			{ID: "edge-code-review-join", TransitionGroupID: "transition-code-review", Key: "join", TargetNodeID: "node-code-review-join"},
			{ID: "edge-qa-join", TransitionGroupID: "transition-qa", Key: "join", TargetNodeID: "node-code-review-join"},
			{ID: "edge-approval-gate", TransitionGroupID: "transition-code-review-join", Key: "approval_gate", TargetNodeID: "node-approval-gate"},
			{ID: "edge-approved", TransitionGroupID: "transition-approval-approved", Key: "approved", TargetNodeID: "node-pr-autoreview"},
			{ID: "edge-rejected", TransitionGroupID: "transition-approval-rejected", Key: "rejected", TargetNodeID: "node-implementation"},
			{ID: "edge-done", TransitionGroupID: "transition-pr-autoreview", Key: "done", TargetNodeID: "node-done"},
		},
	}

	keys := workflowViewBoardColumnKeys(boardColumns(def))
	wantKeys := []string{"backlog", "implementation", "code_review", "qa", "approval_gate", "pr_autoreview", "done"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("board column keys = %+v, want structural rework-cycle order %+v", keys, wantKeys)
	}

	groups := boardGroups(def)
	wantNodeIDs := []string{"node-code-review", "node-qa"}
	if len(groups) != 1 || strings.Join(groups[0].NodeIDs, ",") != strings.Join(wantNodeIDs, ",") {
		t.Fatalf("board groups = %+v, want visible group node ids %+v", groups, wantNodeIDs)
	}
}
