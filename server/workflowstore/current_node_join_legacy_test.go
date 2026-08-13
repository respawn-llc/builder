package workflowstore

import (
	"errors"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/invariant"
)

func TestRejectLegacyCurrentFanoutJoinSourceReturnsTypedErrorInProduction(t *testing.T) {
	definition, source, joinNodeID, joinEdgeID := legacyFanoutJoinDefinition(t)
	records := testsetup.CaptureSlogRecords(t)
	policy := invariant.NewPolicy(
		invariant.WithMode(invariant.ModeDiagnostic),
		invariant.WithSink(workflowInvariantSlogSink{}),
	)
	err := rejectLegacyCurrentFanoutJoinSource(
		policy,
		definition,
		source,
		joinNodeID,
		[]currentFanoutJoinArrival{
			{
				BranchKey:          "branch-a",
				ContinuationSource: workflow.LegacyMaterializedContinuationSource(),
			},
			{
				BranchKey:          "branch-b",
				ContinuationSource: workflow.AbsentMaterializedContinuationSource(),
			},
		},
	)
	var unresolved workflow.LegacyContinuationSourceUnresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("reject legacy Join source error = %v, want LegacyContinuationSourceUnresolvedError", err)
	}
	if unresolved.Source.NodeID != "node-branch-a" {
		t.Fatalf("unresolved legacy Join source Node = %q, want node-branch-a", unresolved.Source.NodeID)
	}
	if branchKey, branchScoped := unresolved.Source.TransitionBranchKey(); !branchScoped || branchKey != "branch-a" {
		t.Fatalf("unresolved legacy Join source = %v, want branch-a", unresolved.Source)
	}
	if unresolved.EdgeID != joinEdgeID {
		t.Fatalf("unresolved legacy Join Edge = %q, want %q", unresolved.EdgeID, joinEdgeID)
	}
	reportWorkflowInvariantError(policy, err)
	var diagnostics int
	for _, record := range records.Records() {
		if record.Fields[string(invariant.FieldOperation)] == legacyContinuationSourceOperation {
			diagnostics++
		}
	}
	if diagnostics != 1 {
		t.Fatalf("legacy Join diagnostics = %d, want exactly 1", diagnostics)
	}
}

func TestRejectLegacyCurrentFanoutJoinSourceFailsFastInDebug(t *testing.T) {
	definition, source, joinNodeID, _ := legacyFanoutJoinDefinition(t)
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("reject legacy Join source did not panic in debug")
		}
	}()
	_ = rejectLegacyCurrentFanoutJoinSource(
		invariant.NewPolicy(invariant.WithMode(invariant.ModePanic)),
		definition,
		source,
		joinNodeID,
		[]currentFanoutJoinArrival{
			{
				BranchKey:          "branch-a",
				ContinuationSource: workflow.LegacyMaterializedContinuationSource(),
			},
			{
				BranchKey:          "branch-b",
				ContinuationSource: workflow.AbsentMaterializedContinuationSource(),
			},
		},
	)
}

func legacyFanoutJoinDefinition(
	t *testing.T,
) (workflow.Definition, workflow.CurrentNodeReference, workflow.NodeID, workflow.EdgeID) {
	t.Helper()
	source, err := workflow.NewCurrentNodeReference("task-legacy-join", "node-branch-b", nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	definition := workflow.Definition{
		Nodes: []workflow.Node{
			workflow.StartNode{NodeIdentity: workflow.NodeIdentity{ID: "node-start", Key: "start", DisplayName: "Start"}},
			workflow.AgentNode{NodeIdentity: workflow.NodeIdentity{ID: "node-branch-a", Key: "branch_a", DisplayName: "Branch A"}, SubagentRole: "default", CompletionMode: "tool"},
			workflow.AgentNode{NodeIdentity: workflow.NodeIdentity{ID: "node-branch-b", Key: "branch_b", DisplayName: "Branch B"}, SubagentRole: "default", CompletionMode: "tool"},
			workflow.JoinNode{NodeIdentity: workflow.NodeIdentity{ID: "node-join", Key: "join", DisplayName: "Join"}},
			workflow.TerminalNode{NodeIdentity: workflow.NodeIdentity{ID: "node-terminal", Key: "terminal", DisplayName: "Terminal"}},
		},
		TransitionGroups: []workflow.TransitionGroup{
			{ID: "group-fanout", SourceNodeID: "node-start", TransitionID: "fanout", DisplayName: "Fan out"},
			{ID: "group-branch-a", SourceNodeID: "node-branch-a", TransitionID: "join_a", DisplayName: "Join A"},
			{ID: "group-branch-b", SourceNodeID: "node-branch-b", TransitionID: "join_b", DisplayName: "Join B"},
			{ID: "group-done", SourceNodeID: "node-join", TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{ID: "edge-fanout-a", TransitionGroupID: "group-fanout", Key: "branch-a", TargetNodeID: "node-branch-a"},
			{ID: "edge-fanout-b", TransitionGroupID: "group-fanout", Key: "branch-b", TargetNodeID: "node-branch-b"},
			{ID: "edge-join-a", TransitionGroupID: "group-branch-a", Key: "join-a", TargetNodeID: "node-join"},
			{ID: "edge-join-b", TransitionGroupID: "group-branch-b", Key: "join-b", TargetNodeID: "node-join"},
			{ID: "edge-done", TransitionGroupID: "group-done", Key: "done", TargetNodeID: "node-terminal"},
		},
	}
	return definition, source, "node-join", "edge-join-a"
}
