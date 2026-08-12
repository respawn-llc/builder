package workflow_test

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestSessionReuseAssociationReferencesIncludesLaterFanoutBranches(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_later_fanout")
	sessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-later-fanout", "node_source", nil)
	if err != nil {
		t.Fatalf("completed reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &sessionID, nil)
	if err != nil {
		t.Fatalf("completed node: %v", err)
	}
	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	next := testAgentNode(workflowID, "node_next", "next", "Next", workflow.NodeFields{SubagentRole: "reviewer"})
	branchA := testAgentNode(workflowID, "node_branch_a", "branch_a", "Branch A", workflow.NodeFields{SubagentRole: "reviewer"})
	branchB := testAgentNode(workflowID, "node_branch_b", "branch_b", "Branch B", workflow.NodeFields{SubagentRole: "reviewer"})
	accepted := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_source_next", Key: "next",
		TransitionGroupID: "group_source", TargetNodeID: "node_next",
		ContextMode: workflow.ContextModeNewSession,
	}
	laterA := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_next_a", Key: "branch_a",
		TransitionGroupID: "group_next", TargetNodeID: "node_branch_a",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
	}
	laterB := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_next_b", Key: "branch_b",
		TransitionGroupID: "group_next", TargetNodeID: "node_branch_b",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
	}

	references := workflow.SessionReuseAssociationReferences(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID: workflowID, DisplayName: "Later Fanout",
			Nodes: []workflow.Node{source, next, branchA, branchB},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
				{WorkflowID: workflowID, ID: "group_next", SourceNodeID: "node_next"},
			},
			Edges: []workflow.Edge{accepted, laterA, laterB},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completedNode,
	})

	wants := []workflow.CurrentNodeReference{}
	for _, want := range []struct {
		nodeID workflow.NodeID
		branch workflow.TransitionBranchKey
	}{
		{nodeID: "node_branch_a", branch: "branch_a"},
		{nodeID: "node_branch_b", branch: "branch_b"},
	} {
		reference, err := workflow.NewCurrentNodeReference("task-later-fanout", want.nodeID, &want.branch)
		if err != nil {
			t.Fatalf("branch %q reference: %v", want.branch, err)
		}
		wants = append(wants, reference)
	}
	for _, want := range wants {
		found := false
		for _, reference := range references {
			if reference.Equal(want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("references omitted later fanout scope %v: %+v", want, references)
		}
	}
	serialBranch := workflow.TransitionBranchKey("next")
	for _, reference := range references {
		if branch, scoped := reference.TransitionBranchKey(); scoped && branch == serialBranch {
			t.Fatalf("references included serial edge key as a branch scope: %+v", reference)
		}
	}
	for _, unwanted := range []struct {
		nodeID workflow.NodeID
		branch workflow.TransitionBranchKey
	}{
		{nodeID: "node_branch_a", branch: "branch_b"},
		{nodeID: "node_branch_b", branch: "branch_a"},
	} {
		reference, err := workflow.NewCurrentNodeReference("task-later-fanout", unwanted.nodeID, &unwanted.branch)
		if err != nil {
			t.Fatalf("unwanted branch %q reference: %v", unwanted.branch, err)
		}
		for _, candidate := range references {
			if candidate.Equal(reference) {
				t.Fatalf("references included unrelated branch scope %v: %+v", reference, references)
			}
		}
	}
}

func TestSessionReuseAssociationReferencesFollowsCycleBackToCompletedNode(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_reference_cycle")
	sessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-reference-cycle", "node_a", nil)
	if err != nil {
		t.Fatalf("completed reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &sessionID, nil)
	if err != nil {
		t.Fatalf("completed node: %v", err)
	}

	nodeA := testAgentNode(workflowID, "node_a", "a", "A", workflow.NodeFields{SubagentRole: "coder"})
	nodeB := testAgentNode(workflowID, "node_b", "b", "B", workflow.NodeFields{SubagentRole: "reviewer"})
	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "reviewer"})
	nodeC := testAgentNode(workflowID, "node_c", "c", "C", workflow.NodeFields{SubagentRole: "reviewer"})
	toB := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_a_b", Key: "b",
		TransitionGroupID: "group_a", TargetNodeID: "node_b",
		ContextMode: workflow.ContextModeNewSession,
	}
	toA := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_b_a", Key: "a",
		TransitionGroupID: "group_b", TargetNodeID: "node_a",
		ContextMode: workflow.ContextModeNewSession,
	}
	afterCycle := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_a_c", Key: "c",
		TransitionGroupID: "group_a_after_cycle", TargetNodeID: "node_c",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "worker"},
	}

	references := workflow.SessionReuseAssociationReferences(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Reference Cycle",
			Nodes:       []workflow.Node{nodeA, nodeB, worker, nodeC},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_a", SourceNodeID: "node_a"},
				{WorkflowID: workflowID, ID: "group_b", SourceNodeID: "node_b"},
				{WorkflowID: workflowID, ID: "group_a_after_cycle", SourceNodeID: "node_a"},
			},
			Edges: []workflow.Edge{toB, toA, afterCycle},
		},
		AcceptedBranches:     []workflow.Edge{toB},
		CompletedCurrentNode: completedNode,
	})

	workerReference, err := workflow.NewCurrentNodeReference("task-reference-cycle", "node_worker", nil)
	if err != nil {
		t.Fatalf("worker reference: %v", err)
	}
	for _, reference := range references {
		if reference.Equal(workerReference) {
			return
		}
	}
	t.Fatalf("references omitted context source reachable after cycle: %+v", references)
}

func TestClassifyWorkflowSessionReuseFindsAReachableSerialLoop(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_serial_loop")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-serial-loop", "node_worker", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}
	worker := testNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeKindAgent, workflow.NodeFields{SubagentRole: "coder"})
	review := testNode(workflowID, "node_review", "review", "Review", workflow.NodeKindAgent, workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	accepted := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_review",
		Key:               "review",
		TransitionGroupID: "group_worker",
		TargetNodeID:      "node_review",
		ContextMode:       workflow.ContextModeNewSession,
	}
	reuse := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_worker",
		Key:               "worker",
		TransitionGroupID: "group_review",
		TargetNodeID:      "node_worker",
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{
			Kind:    workflow.ContextSourceSelectedNode,
			NodeKey: "worker",
		},
	}
	terminal := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_done",
		Key:               "done",
		TransitionGroupID: "group_review_done",
		TargetNodeID:      "node_done",
		ContextMode:       workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Serial Loop",
			Nodes:       []workflow.Node{worker, review, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
				{WorkflowID: workflowID, ID: "group_review", SourceNodeID: "node_review"},
				{WorkflowID: workflowID, ID: "group_review_done", SourceNodeID: "node_review"},
			},
			Edges: []workflow.Edge{accepted, reuse, terminal},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseThresholdPossibleReuse {
		t.Fatalf("classification = %q, want threshold_possible_reuse", classification)
	}
}

func TestClassifyWorkflowSessionReuseExcludesImmediateNonDormantContinuation(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_direct")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-direct", "node_worker", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	accepted := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_worker",
		Key:               "worker",
		TransitionGroupID: "group_worker",
		TargetNodeID:      "node_worker",
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
	}
	terminal := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_done",
		Key:               "done",
		TransitionGroupID: "group_done",
		TargetNodeID:      "node_done",
		ContextMode:       workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Direct Continuation",
			Nodes:       []workflow.Node{worker, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
				{WorkflowID: workflowID, ID: "group_done", SourceNodeID: "node_worker"},
			},
			Edges: []workflow.Edge{accepted, terminal},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseNone {
		t.Fatalf("classification = %q, want none", classification)
	}
}

func TestClassifyWorkflowSessionReuseDoesNotEagerlyCompactAfterImmediateContinuation(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_immediate_then_cac")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-immediate-then-cac", "node_a", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	nodeA := testAgentNode(workflowID, "node_a", "a", "A", workflow.NodeFields{SubagentRole: "coder"})
	nodeB := testAgentNode(workflowID, "node_b", "b", "B", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	toB := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_a_b", Key: "b",
		TransitionGroupID: "group_a", TargetNodeID: "node_b",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
	}
	toA := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_b_a", Key: "a",
		TransitionGroupID: "group_b", TargetNodeID: "node_a",
		ContextMode:   workflow.ContextModeCompactAndContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "a"},
	}
	toDone := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_b_done", Key: "done",
		TransitionGroupID: "group_b_done", TargetNodeID: "node_done",
		ContextMode: workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID: workflowID, DisplayName: "Immediate Then CAC",
			Nodes: []workflow.Node{nodeA, nodeB, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_a", SourceNodeID: "node_a"},
				{WorkflowID: workflowID, ID: "group_b", SourceNodeID: "node_b"},
				{WorkflowID: workflowID, ID: "group_b_done", SourceNodeID: "node_b"},
			},
			Edges: []workflow.Edge{toB, toA, toDone},
		},
		AcceptedBranches:     []workflow.Edge{toB},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseNone {
		t.Fatalf("classification = %q, want none for non-dormant immediate continuation", classification)
	}
}

func TestClassifyWorkflowSessionReuseDoesNotRetroactivelyDormantAfterDirectContinuation(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_direct_then_new")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-direct-then-new", "node_a", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	nodeA := testAgentNode(workflowID, "node_a", "a", "A", workflow.NodeFields{SubagentRole: "coder"})
	nodeB := testAgentNode(workflowID, "node_b", "b", "B", workflow.NodeFields{SubagentRole: "reviewer"})
	nodeC := testAgentNode(workflowID, "node_c", "c", "C", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	toB := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_a_b", Key: "b",
		TransitionGroupID: "group_a", TargetNodeID: "node_b",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
	}
	toC := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_b_c", Key: "c",
		TransitionGroupID: "group_b", TargetNodeID: "node_c",
		ContextMode: workflow.ContextModeNewSession,
	}
	toA := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_c_a", Key: "a",
		TransitionGroupID: "group_c", TargetNodeID: "node_a",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "a"},
	}
	toDone := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_c_done", Key: "done",
		TransitionGroupID: "group_c_done", TargetNodeID: "node_done",
		ContextMode: workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID: workflowID, DisplayName: "Direct Then New Session",
			Nodes: []workflow.Node{nodeA, nodeB, nodeC, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_a", SourceNodeID: "node_a"},
				{WorkflowID: workflowID, ID: "group_b", SourceNodeID: "node_b"},
				{WorkflowID: workflowID, ID: "group_c", SourceNodeID: "node_c"},
				{WorkflowID: workflowID, ID: "group_c_done", SourceNodeID: "node_c"},
			},
			Edges: []workflow.Edge{toB, toC, toA, toDone},
		},
		AcceptedBranches:     []workflow.Edge{toB},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseNone {
		t.Fatalf("classification = %q, want none after direct same-session continuation", classification)
	}
}

func TestClassifyWorkflowSessionReuseDowngradesOverwrittenRetainedAssociation(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_overwritten_association")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-overwritten-association", "node_a", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	nodeA := testAgentNode(workflowID, "node_a", "a", "A", workflow.NodeFields{SubagentRole: "coder"})
	nodeB := testAgentNode(workflowID, "node_b", "b", "B", workflow.NodeFields{SubagentRole: "reviewer"})
	nodeC := testAgentNode(workflowID, "node_c", "c", "C", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	toB := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_a_b", Key: "b",
		TransitionGroupID: "group_a", TargetNodeID: "node_b",
		ContextMode: workflow.ContextModeNewSession,
	}
	toA := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_b_a", Key: "a",
		TransitionGroupID: "group_b", TargetNodeID: "node_a",
		ContextMode: workflow.ContextModeNewSession,
	}
	toC := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_a_c", Key: "c",
		TransitionGroupID: "group_a_again", TargetNodeID: "node_c",
		ContextMode:   workflow.ContextModeCompactAndContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "a"},
	}
	toDone := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_c_done", Key: "done",
		TransitionGroupID: "group_c", TargetNodeID: "node_done",
		ContextMode: workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID: workflowID, DisplayName: "Overwritten Association",
			Nodes: []workflow.Node{nodeA, nodeB, nodeC, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_a", SourceNodeID: "node_a"},
				{WorkflowID: workflowID, ID: "group_b", SourceNodeID: "node_b"},
				{WorkflowID: workflowID, ID: "group_a_again", SourceNodeID: "node_a"},
				{WorkflowID: workflowID, ID: "group_c", SourceNodeID: "node_c"},
			},
			Edges: []workflow.Edge{toB, toA, toC, toDone},
		},
		AcceptedBranches:     []workflow.Edge{toB},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseNone {
		t.Fatalf("classification = %q, want none after new-session overwrite", classification)
	}
}

func TestClassifyWorkflowSessionReuseFollowsTransitiveImmediateSourceAfterApproval(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_immediate_transitive")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-immediate-transitive", "node_worker", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
	review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	accepted := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_review",
		Key:               "review",
		TransitionGroupID: "group_worker",
		TargetNodeID:      "node_review",
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		RequiresApproval:  true,
	}
	reuse := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_worker",
		Key:               "worker",
		TransitionGroupID: "group_review",
		TargetNodeID:      "node_worker",
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
	}
	terminal := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_done",
		Key:               "done",
		TransitionGroupID: "group_review_done",
		TargetNodeID:      "node_done",
		ContextMode:       workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Transitive Immediate Source",
			Nodes:       []workflow.Node{worker, review, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
				{WorkflowID: workflowID, ID: "group_review", SourceNodeID: "node_review"},
				{WorkflowID: workflowID, ID: "group_review_done", SourceNodeID: "node_review"},
			},
			Edges: []workflow.Edge{accepted, reuse, terminal},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseThresholdPossibleReuse {
		t.Fatalf("classification = %q, want threshold_possible_reuse", classification)
	}
}

func TestClassifyWorkflowSessionReuseResolvesRetainedContextSources(t *testing.T) {
	tests := []struct {
		name        string
		source      workflow.ContextSource
		association bool
		want        workflow.SessionReuseClassification
	}{
		{
			name:        "selected node",
			source:      workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "worker"},
			association: true,
			want:        workflow.SessionReuseThresholdPossibleReuse,
		},
		{
			name:        "previous target",
			source:      workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
			association: true,
			want:        workflow.SessionReuseThresholdPossibleReuse,
		},
		{
			name:        "previous target fallback to new",
			source:      workflow.ContextSource{Kind: workflow.ContextSourcePreviousTargetOrNew},
			association: false,
			want:        workflow.SessionReuseNone,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_context_source_"+test.name)
			completedSessionID := runtimeids.NewSessionID()
			completedReference, err := workflow.NewCurrentNodeReference("task-retained-context", "node_worker", nil)
			if err != nil {
				t.Fatalf("completed current node reference: %v", err)
			}
			completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
			if err != nil {
				t.Fatalf("completed current node: %v", err)
			}

			worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
			review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
			done := testTerminalNode(workflowID, "node_done", "done", "Done")
			accepted := workflow.Edge{
				WorkflowID:        workflowID,
				ID:                "edge_worker_review",
				Key:               "review",
				TransitionGroupID: "group_worker",
				TargetNodeID:      "node_review",
				ContextMode:       workflow.ContextModeNewSession,
			}
			reuse := workflow.Edge{
				WorkflowID:        workflowID,
				ID:                "edge_review_worker",
				Key:               "worker",
				TransitionGroupID: "group_review",
				TargetNodeID:      "node_worker",
				ContextMode:       workflow.ContextModeContinueSession,
				ContextSource:     test.source,
			}
			terminal := workflow.Edge{
				WorkflowID:        workflowID,
				ID:                "edge_review_done",
				Key:               "done",
				TransitionGroupID: "group_review_done",
				TargetNodeID:      "node_done",
				ContextMode:       workflow.ContextModeNewSession,
			}
			associations := []workflow.SessionReuseAssociation(nil)
			if test.association {
				associations = append(associations, workflow.SessionReuseAssociation{
					SessionID:   completedSessionID,
					CurrentNode: completedReference,
				})
			}

			classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
				Workflow: workflow.Definition{
					ID:          workflowID,
					DisplayName: "Retained Context Source",
					Nodes:       []workflow.Node{worker, review, done},
					TransitionGroups: []workflow.TransitionGroup{
						{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
						{WorkflowID: workflowID, ID: "group_review", SourceNodeID: "node_review"},
						{WorkflowID: workflowID, ID: "group_review_done", SourceNodeID: "node_review"},
					},
					Edges: []workflow.Edge{accepted, reuse, terminal},
				},
				AcceptedBranches:     []workflow.Edge{accepted},
				CompletedCurrentNode: completedNode,
				RetainedAssociations: associations,
			})

			if classification != test.want {
				t.Fatalf("classification = %q, want %q", classification, test.want)
			}
		})
	}
}

func TestClassifyWorkflowSessionReuseIgnoresTerminalOnlyAndUnreachableReuse(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_terminal_unreachable")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-terminal-unreachable", "node_worker", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
	review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	unreachableReuse := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_worker",
		Key:               "worker",
		TransitionGroupID: "group_review",
		TargetNodeID:      "node_worker",
		ContextMode:       workflow.ContextModeCompactAndContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "worker"},
	}
	accepted := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_done",
		Key:               "done",
		TransitionGroupID: "group_worker",
		TargetNodeID:      "node_done",
		ContextMode:       workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Terminal And Unreachable",
			Nodes:       []workflow.Node{worker, review, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
				{WorkflowID: workflowID, ID: "group_review", SourceNodeID: "node_review"},
			},
			Edges: []workflow.Edge{accepted, unreachableReuse},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseNone {
		t.Fatalf("classification = %q, want none", classification)
	}
}

func TestClassifyWorkflowSessionReuseDistinguishesGuaranteedAndOptionalCAC(t *testing.T) {
	tests := []struct {
		name                   string
		addTerminalAlternative bool
		want                   workflow.SessionReuseClassification
	}{
		{
			name: "guaranteed",
			want: workflow.SessionReuseGuaranteedCACReuse,
		},
		{
			name:                   "optional",
			addTerminalAlternative: true,
			want:                   workflow.SessionReuseThresholdPossibleReuse,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_cac_"+test.name)
			completedSessionID := runtimeids.NewSessionID()
			completedReference, err := workflow.NewCurrentNodeReference(workflow.TaskID("task-cac-"+test.name), "node_worker", nil)
			if err != nil {
				t.Fatalf("completed current node reference: %v", err)
			}
			completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
			if err != nil {
				t.Fatalf("completed current node: %v", err)
			}

			worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
			review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
			done := testTerminalNode(workflowID, "node_done", "done", "Done")
			accepted := workflow.Edge{
				WorkflowID:        workflowID,
				ID:                "edge_worker_review",
				Key:               "review",
				TransitionGroupID: "group_worker",
				TargetNodeID:      "node_review",
				ContextMode:       workflow.ContextModeNewSession,
			}
			cac := workflow.Edge{
				WorkflowID:        workflowID,
				ID:                "edge_review_worker",
				Key:               "worker",
				TransitionGroupID: "group_review",
				TargetNodeID:      "node_worker",
				ContextMode:       workflow.ContextModeCompactAndContinueSession,
				ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "worker"},
			}
			terminal := workflow.Edge{
				WorkflowID:        workflowID,
				ID:                "edge_review_done",
				Key:               "done",
				TransitionGroupID: "group_review_done",
				TargetNodeID:      "node_done",
				ContextMode:       workflow.ContextModeNewSession,
			}
			groups := []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
				{WorkflowID: workflowID, ID: "group_review", SourceNodeID: "node_review"},
			}
			edges := []workflow.Edge{accepted, cac}
			if test.addTerminalAlternative {
				groups = append(groups, workflow.TransitionGroup{WorkflowID: workflowID, ID: "group_review_done", SourceNodeID: "node_review"})
				edges = append(edges, terminal)
			}

			classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
				Workflow: workflow.Definition{
					ID:               workflowID,
					DisplayName:      "CAC Classification",
					Nodes:            []workflow.Node{worker, review, done},
					TransitionGroups: groups,
					Edges:            edges,
				},
				AcceptedBranches:     []workflow.Edge{accepted},
				CompletedCurrentNode: completedNode,
				RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
			})

			if classification != test.want {
				t.Fatalf("classification = %q, want %q", classification, test.want)
			}
		})
	}
}

func TestClassifyWorkflowSessionReuseTreatsApprovalGatedAcceptedCACAsGuaranteed(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_approval_gated_cac")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-approval-gated-cac", "node_worker", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
	review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
	acceptedCAC := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_review",
		Key:               "review",
		TransitionGroupID: "group_worker",
		TargetNodeID:      "node_review",
		ContextMode:       workflow.ContextModeCompactAndContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		RequiresApproval:  true,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Approval-Gated Accepted CAC",
			Nodes:       []workflow.Node{worker, review},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
			},
			Edges: []workflow.Edge{acceptedCAC},
		},
		AcceptedBranches:     []workflow.Edge{acceptedCAC},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseGuaranteedCACReuse {
		t.Fatalf("classification = %q, want guaranteed_cac_reuse", classification)
	}

	acceptedCAC.RequiresApproval = false
	directClassification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Direct Accepted CAC",
			Nodes:       []workflow.Node{worker, review},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
			},
			Edges: []workflow.Edge{acceptedCAC},
		},
		AcceptedBranches:     []workflow.Edge{acceptedCAC},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})
	if directClassification != workflow.SessionReuseNone {
		t.Fatalf("direct classification = %q, want none without approval", directClassification)
	}
}

func TestClassifyWorkflowSessionReuseProvesCACThroughDecisionCycleExit(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_cycle_cac")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-cycle-cac", "node_worker", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
	review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	accepted := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_review",
		Key:               "review",
		TransitionGroupID: "group_worker",
		TargetNodeID:      "node_review",
		ContextMode:       workflow.ContextModeNewSession,
	}
	loop := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_loop",
		Key:               "loop",
		TransitionGroupID: "group_review_loop",
		TargetNodeID:      "node_review",
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
	}
	cac := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_worker",
		Key:               "worker",
		TransitionGroupID: "group_review_worker",
		TargetNodeID:      "node_worker",
		ContextMode:       workflow.ContextModeCompactAndContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "worker"},
	}
	terminal := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_done",
		Key:               "done",
		TransitionGroupID: "group_worker_done",
		TargetNodeID:      "node_done",
		ContextMode:       workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Cycle With CAC Exit",
			Nodes:       []workflow.Node{worker, review, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_worker", SourceNodeID: "node_worker"},
				{WorkflowID: workflowID, ID: "group_review_loop", SourceNodeID: "node_review"},
				{WorkflowID: workflowID, ID: "group_review_worker", SourceNodeID: "node_review"},
				{WorkflowID: workflowID, ID: "group_worker_done", SourceNodeID: "node_worker"},
			},
			Edges: []workflow.Edge{accepted, loop, cac, terminal},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseGuaranteedCACReuse {
		t.Fatalf("classification = %q, want guaranteed_cac_reuse", classification)
	}
}

func TestClassifyWorkflowSessionReuseAcceptsOneGuaranteedFanoutBranch(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_fanout_branch")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-fanout-branch", "node_source", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}
	branchKey := workflow.TransitionBranchKey("review")
	branchReference, err := workflow.NewCurrentNodeReference("task-fanout-branch", "node_source", &branchKey)
	if err != nil {
		t.Fatalf("retained branch reference: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	acceptedReview := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_source_review",
		Key:               "review",
		TransitionGroupID: "group_source",
		TargetNodeID:      "node_review",
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
	}
	acceptedDone := acceptedReview
	acceptedDone.ID = "edge_source_done"
	acceptedDone.Key = "done"
	acceptedDone.TargetNodeID = "node_done"
	cac := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_source",
		Key:               "source",
		TransitionGroupID: "group_review",
		TargetNodeID:      "node_source",
		ContextMode:       workflow.ContextModeCompactAndContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Fanout Branch",
			Nodes:       []workflow.Node{source, review, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
				{WorkflowID: workflowID, ID: "group_review", SourceNodeID: "node_review"},
			},
			Edges: []workflow.Edge{acceptedReview, acceptedDone, cac},
		},
		AcceptedBranches:     []workflow.Edge{acceptedReview, acceptedDone},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: branchReference}},
	})

	if classification != workflow.SessionReuseGuaranteedCACReuse {
		t.Fatalf("classification = %q, want guaranteed_cac_reuse", classification)
	}
}

func TestClassifyWorkflowSessionReuseResolvesFanoutPreviousTargetInTargetBranch(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_fanout_previous_target")
	completedSessionID := runtimeids.NewSessionID()
	completedSource, err := workflow.NewCurrentNodeReference("task-fanout-previous", "node_source", nil)
	if err != nil {
		t.Fatalf("completed source reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedSource, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed source node: %v", err)
	}
	branchKey := workflow.TransitionBranchKey("branch_a")
	branchReference, err := workflow.NewCurrentNodeReference("task-fanout-previous", "node_branch_a", &branchKey)
	if err != nil {
		t.Fatalf("retained branch reference: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	branchA := testAgentNode(workflowID, "node_branch_a", "branch_a", "Branch A", workflow.NodeFields{SubagentRole: "reviewer"})
	branchB := testAgentNode(workflowID, "node_branch_b", "branch_b", "Branch B", workflow.NodeFields{SubagentRole: "reviewer"})
	acceptedA := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_source_a", Key: "branch_a",
		TransitionGroupID: "group_source", TargetNodeID: "node_branch_a",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
	}
	acceptedB := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_source_b", Key: "branch_b",
		TransitionGroupID: "group_source", TargetNodeID: "node_branch_b",
		ContextMode: workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID: workflowID, DisplayName: "Fanout Previous Target",
			Nodes: []workflow.Node{source, branchA, branchB},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
			},
			Edges: []workflow.Edge{acceptedA, acceptedB},
		},
		AcceptedBranches:     []workflow.Edge{acceptedA, acceptedB},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: branchReference}},
	})

	if classification != workflow.SessionReuseThresholdPossibleReuse {
		t.Fatalf("classification = %q, want threshold_possible_reuse", classification)
	}
}

func TestClassifyWorkflowSessionReusePreservesCompletedBranchScope(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_completed_branch")
	completedSessionID := runtimeids.NewSessionID()
	branchKey := workflow.TransitionBranchKey("branch_a")
	completedReference, err := workflow.NewCurrentNodeReference("task-completed-branch", "node_source", &branchKey)
	if err != nil {
		t.Fatalf("completed branch reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed branch node: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	accepted := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_source_review", Key: "review",
		TransitionGroupID: "group_source", TargetNodeID: "node_review",
		ContextMode: workflow.ContextModeNewSession,
	}
	reuse := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_review_source", Key: "source",
		TransitionGroupID: "group_review", TargetNodeID: "node_source",
		ContextMode:   workflow.ContextModeCompactAndContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
	}
	terminal := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_review_done", Key: "done",
		TransitionGroupID: "group_review_done", TargetNodeID: "node_done",
		ContextMode: workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID: workflowID, DisplayName: "Completed Branch Scope",
			Nodes: []workflow.Node{source, review, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
				{WorkflowID: workflowID, ID: "group_review", SourceNodeID: "node_review"},
				{WorkflowID: workflowID, ID: "group_review_done", SourceNodeID: "node_review"},
			},
			Edges: []workflow.Edge{accepted, reuse, terminal},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseThresholdPossibleReuse {
		t.Fatalf("classification = %q, want threshold_possible_reuse for completed branch scope", classification)
	}
}

func TestClassifyWorkflowSessionReuseTreatsUnknownCorrelationAsOptional(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_unknown_correlation")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-unknown-correlation", "node_source", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	review := testAgentNode(workflowID, "node_review", "review", "Review", workflow.NodeFields{SubagentRole: "reviewer"})
	accepted := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_source_review",
		Key:               "review",
		TransitionGroupID: "group_source",
		TargetNodeID:      "node_review",
		ContextMode:       workflow.ContextModeNewSession,
	}
	optionalCAC := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_review_source",
		Key:               "source",
		TransitionGroupID: "group_review",
		TargetNodeID:      "node_source",
		ContextMode:       workflow.ContextModeCompactAndContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceKind("unsupported_correlation")},
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Unknown Correlation",
			Nodes:       []workflow.Node{source, review},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
				{WorkflowID: workflowID, ID: "group_review", SourceNodeID: "node_review"},
			},
			Edges: []workflow.Edge{accepted, optionalCAC},
		},
		AcceptedBranches:     []workflow.Edge{accepted},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	})

	if classification != workflow.SessionReuseThresholdPossibleReuse {
		t.Fatalf("classification = %q, want threshold_possible_reuse", classification)
	}
}

func TestClassifyWorkflowSessionReusePreservesBranchScopeAcrossJoinCycle(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_join_cycle")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-join-cycle", "node_source", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}
	branchAKey := workflow.TransitionBranchKey("branch_a")
	branchAReference, err := workflow.NewCurrentNodeReference(
		"task-join-cycle",
		"node_worker",
		&branchAKey,
	)
	if err != nil {
		t.Fatalf("retained branch current node reference: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	branchA := testAgentNode(workflowID, "node_branch_a", "branch_a", "Branch A", workflow.NodeFields{SubagentRole: "reviewer"})
	branchB := testAgentNode(workflowID, "node_branch_b", "branch_b", "Branch B", workflow.NodeFields{SubagentRole: "reviewer"})
	join := testJoinNode(workflowID, "node_join", "join", "Join")
	cycleA := testAgentNode(workflowID, "node_cycle_a", "cycle_a", "Cycle A", workflow.NodeFields{SubagentRole: "reviewer"})
	cycleB := testAgentNode(workflowID, "node_cycle_b", "cycle_b", "Cycle B", workflow.NodeFields{SubagentRole: "reviewer"})
	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")

	acceptedA := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_source_a",
		Key:               "branch_a",
		TransitionGroupID: "group_source",
		TargetNodeID:      "node_branch_a",
		ContextMode:       workflow.ContextModeNewSession,
	}
	acceptedB := acceptedA
	acceptedB.ID = "edge_source_b"
	acceptedB.Key = "branch_b"
	acceptedB.TargetNodeID = "node_branch_b"
	branchToJoinA := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_a_join",
		Key:               "join_a",
		TransitionGroupID: "group_a_join",
		TargetNodeID:      "node_join",
		ContextMode:       workflow.ContextModeNewSession,
	}
	branchToJoinB := branchToJoinA
	branchToJoinB.ID = "edge_b_join"
	branchToJoinB.Key = "join_b"
	branchToJoinB.TransitionGroupID = "group_b_join"
	cycleAEdge := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_join_cycle_a",
		Key:               "branch_a",
		TransitionGroupID: "group_join",
		TargetNodeID:      "node_cycle_a",
		ContextMode:       workflow.ContextModeNewSession,
	}
	cycleBEdge := cycleAEdge
	cycleBEdge.ID = "edge_join_cycle_b"
	cycleBEdge.Key = "branch_b"
	cycleBEdge.TargetNodeID = "node_cycle_b"
	reuse := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_cycle_worker",
		Key:               "worker",
		TransitionGroupID: "group_cycle_worker",
		TargetNodeID:      "node_worker",
		ContextMode:       workflow.ContextModeContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
	}
	terminal := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_worker_done",
		Key:               "done",
		TransitionGroupID: "group_worker_done",
		TargetNodeID:      "node_done",
		ContextMode:       workflow.ContextModeNewSession,
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Branch Scope Join Cycle",
			Nodes:       []workflow.Node{source, branchA, branchB, join, cycleA, cycleB, worker, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
				{WorkflowID: workflowID, ID: "group_a_join", SourceNodeID: "node_branch_a"},
				{WorkflowID: workflowID, ID: "group_b_join", SourceNodeID: "node_branch_b"},
				{WorkflowID: workflowID, ID: "group_join", SourceNodeID: "node_join"},
				{WorkflowID: workflowID, ID: "group_cycle_worker", SourceNodeID: "node_cycle_a"},
				{WorkflowID: workflowID, ID: "group_worker_done", SourceNodeID: "node_worker"},
			},
			Edges: []workflow.Edge{acceptedA, acceptedB, branchToJoinA, branchToJoinB, cycleAEdge, cycleBEdge, reuse, terminal},
		},
		AcceptedBranches:     []workflow.Edge{acceptedA, acceptedB},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: branchAReference}},
	})

	if classification != workflow.SessionReuseThresholdPossibleReuse {
		t.Fatalf("classification = %q, want threshold_possible_reuse", classification)
	}
}

func TestClassifyWorkflowSessionReuseScopesOverwrittenProvenanceByBranch(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_branch_scoped_overwrite")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-branch-scoped-overwrite", "node_source", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}
	branchBKey := workflow.TransitionBranchKey("branch_b")
	workerBranchBReference, err := workflow.NewCurrentNodeReference(
		"task-branch-scoped-overwrite",
		"node_worker",
		&branchBKey,
	)
	if err != nil {
		t.Fatalf("retained branch current node reference: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	branchA := testAgentNode(workflowID, "node_branch_a", "branch_a", "Branch A", workflow.NodeFields{SubagentRole: "reviewer"})
	branchOther := testAgentNode(workflowID, "node_branch_other", "branch_other", "Other Branch", workflow.NodeFields{SubagentRole: "reviewer"})
	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
	join := testJoinNode(workflowID, "node_join", "join", "Join")
	cycleB := testAgentNode(workflowID, "node_cycle_b", "cycle_b", "Cycle B", workflow.NodeFields{SubagentRole: "reviewer"})
	cycleOther := testAgentNode(workflowID, "node_cycle_other", "cycle_other", "Other Cycle", workflow.NodeFields{SubagentRole: "reviewer"})

	acceptedA := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_source_a", Key: "branch_a",
		TransitionGroupID: "group_source", TargetNodeID: "node_branch_a",
		ContextMode: workflow.ContextModeNewSession,
	}
	acceptedOther := acceptedA
	acceptedOther.ID = "edge_source_other"
	acceptedOther.Key = "branch_other"
	acceptedOther.TargetNodeID = "node_branch_other"
	branchAToWorker := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_a_worker", Key: "worker",
		TransitionGroupID: "group_a_worker", TargetNodeID: "node_worker",
		ContextMode: workflow.ContextModeNewSession,
	}
	workerToJoin := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_worker_join", Key: "join",
		TransitionGroupID: "group_worker_join", TargetNodeID: "node_join",
		ContextMode: workflow.ContextModeNewSession,
	}
	joinToB := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_join_b", Key: "branch_b",
		TransitionGroupID: "group_join", TargetNodeID: "node_cycle_b",
		ContextMode: workflow.ContextModeNewSession,
	}
	joinToOther := joinToB
	joinToOther.ID = "edge_join_other"
	joinToOther.Key = "branch_other"
	joinToOther.TargetNodeID = "node_cycle_other"
	cycleBToWorker := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_cycle_b_worker", Key: "worker",
		TransitionGroupID: "group_cycle_b_worker", TargetNodeID: "node_worker",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Branch-Scoped Overwrite",
			Nodes:       []workflow.Node{source, branchA, branchOther, worker, join, cycleB, cycleOther},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
				{WorkflowID: workflowID, ID: "group_a_worker", SourceNodeID: "node_branch_a"},
				{WorkflowID: workflowID, ID: "group_worker_join", SourceNodeID: "node_worker"},
				{WorkflowID: workflowID, ID: "group_join", SourceNodeID: "node_join"},
				{WorkflowID: workflowID, ID: "group_cycle_b_worker", SourceNodeID: "node_cycle_b"},
			},
			Edges: []workflow.Edge{
				acceptedA, acceptedOther, branchAToWorker, workerToJoin,
				joinToB, joinToOther, cycleBToWorker,
			},
		},
		AcceptedBranches:     []workflow.Edge{acceptedA, acceptedOther},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: workerBranchBReference}},
	})

	if classification == workflow.SessionReuseNone {
		t.Fatalf("classification = %q, want branch-scoped reuse to remain eligible", classification)
	}
}

func TestClassifyWorkflowSessionReuseUsesOneGuaranteedFanoutBranch(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_fanout_guarantee")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-fanout-guarantee", "node_source", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}
	branchKey := workflow.TransitionBranchKey("reuse")
	branchReference, err := workflow.NewCurrentNodeReference("task-fanout-guarantee", "node_source", &branchKey)
	if err != nil {
		t.Fatalf("retained branch reference: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	reuse := testAgentNode(workflowID, "node_reuse", "reuse", "Reuse", workflow.NodeFields{SubagentRole: "reviewer"})
	terminal := testTerminalNode(workflowID, "node_done", "done", "Done")
	acceptedReuse := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_source_reuse",
		Key:               "reuse",
		TransitionGroupID: "group_source",
		TargetNodeID:      "node_reuse",
		ContextMode:       workflow.ContextModeNewSession,
	}
	acceptedDone := acceptedReuse
	acceptedDone.ID = "edge_source_done"
	acceptedDone.Key = "done"
	acceptedDone.TargetNodeID = "node_done"
	cac := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_reuse_source",
		Key:               "source",
		TransitionGroupID: "group_reuse",
		TargetNodeID:      "node_source",
		ContextMode:       workflow.ContextModeCompactAndContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourcePreviousTarget},
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Fanout Guarantee",
			Nodes:       []workflow.Node{source, reuse, terminal},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
				{WorkflowID: workflowID, ID: "group_reuse", SourceNodeID: "node_reuse"},
			},
			Edges: []workflow.Edge{acceptedReuse, acceptedDone, cac},
		},
		AcceptedBranches:     []workflow.Edge{acceptedReuse, acceptedDone},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: branchReference}},
	})

	if classification != workflow.SessionReuseGuaranteedCACReuse {
		t.Fatalf("classification = %q, want guaranteed_cac_reuse", classification)
	}
}

func TestClassifyWorkflowSessionReuseSelectedNodeUsesSourceBranchAfterFanout(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_selected_source_branch")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-selected-source-branch", "node_source", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	reuse := testAgentNode(workflowID, "node_reuse", "reuse", "Reuse", workflow.NodeFields{SubagentRole: "reviewer"})
	done := testTerminalNode(workflowID, "node_done", "done", "Done")
	acceptedReuse := workflow.Edge{
		WorkflowID:        workflowID,
		ID:                "edge_source_reuse",
		Key:               "reuse",
		TransitionGroupID: "group_source",
		TargetNodeID:      "node_reuse",
		ContextMode:       workflow.ContextModeCompactAndContinueSession,
		ContextSource:     workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "source"},
	}
	acceptedDone := acceptedReuse
	acceptedDone.ID = "edge_source_done"
	acceptedDone.Key = "done"
	acceptedDone.TargetNodeID = "node_done"
	input := workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Selected Source Branch",
			Nodes:       []workflow.Node{source, reuse, done},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
			},
			Edges: []workflow.Edge{acceptedReuse, acceptedDone},
		},
		AcceptedBranches:     []workflow.Edge{acceptedReuse, acceptedDone},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: completedReference}},
	}

	if classification := workflow.ClassifyWorkflowSessionReuse(input); classification != workflow.SessionReuseGuaranteedCACReuse {
		t.Fatalf("classification = %q, want guaranteed_cac_reuse", classification)
	}
	for _, reference := range workflow.SessionReuseAssociationReferences(input) {
		if branch, scoped := reference.TransitionBranchKey(); scoped &&
			reference.NodeID == "node_source" && branch == "reuse" {
			t.Fatalf("selected-node lookup incorrectly used target branch: %+v", reference)
		}
	}
}

func TestClassifyWorkflowSessionReusePreservesReuseAcrossConvergedOverwritePaths(t *testing.T) {
	workflowID := testsetup.WorkflowID(t, "workflow_session_reuse_converged_overwrite")
	completedSessionID := runtimeids.NewSessionID()
	completedReference, err := workflow.NewCurrentNodeReference("task-converged-overwrite", "node_source", nil)
	if err != nil {
		t.Fatalf("completed current node reference: %v", err)
	}
	completedNode, err := workflow.NewCurrentNode(completedReference, &completedSessionID, nil)
	if err != nil {
		t.Fatalf("completed current node: %v", err)
	}
	workerReference, err := workflow.NewCurrentNodeReference("task-converged-overwrite", "node_worker", nil)
	if err != nil {
		t.Fatalf("retained worker reference: %v", err)
	}

	source := testAgentNode(workflowID, "node_source", "source", "Source", workflow.NodeFields{SubagentRole: "coder"})
	decision := testAgentNode(workflowID, "node_decision", "decision", "Decision", workflow.NodeFields{SubagentRole: "reviewer"})
	pathA := testAgentNode(workflowID, "node_path_a", "path_a", "Path A", workflow.NodeFields{SubagentRole: "reviewer"})
	pathB := testAgentNode(workflowID, "node_path_b", "path_b", "Path B", workflow.NodeFields{SubagentRole: "reviewer"})
	worker := testAgentNode(workflowID, "node_worker", "worker", "Worker", workflow.NodeFields{SubagentRole: "coder"})
	join := testJoinNode(workflowID, "node_join", "join", "Join")
	start := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_source_decision", Key: "decision",
		TransitionGroupID: "group_source", TargetNodeID: "node_decision",
		ContextMode: workflow.ContextModeNewSession,
	}
	pathAEdge := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_decision_a", Key: "path_a",
		TransitionGroupID: "group_decision_a", TargetNodeID: "node_path_a",
		ContextMode: workflow.ContextModeNewSession,
	}
	pathBEdge := pathAEdge
	pathBEdge.ID = "edge_decision_b"
	pathBEdge.Key = "path_b"
	pathBEdge.TransitionGroupID = "group_decision_b"
	pathBEdge.TargetNodeID = "node_path_b"
	pathAToWorker := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_path_a_worker", Key: "worker",
		TransitionGroupID: "group_path_a_worker", TargetNodeID: "node_worker",
		ContextMode: workflow.ContextModeNewSession,
	}
	pathAToJoin := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_worker_join", Key: "join",
		TransitionGroupID: "group_worker_join", TargetNodeID: "node_join",
		ContextMode: workflow.ContextModeNewSession,
	}
	pathBToJoin := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_path_b_join", Key: "join",
		TransitionGroupID: "group_path_b_join", TargetNodeID: "node_join",
		ContextMode: workflow.ContextModeNewSession,
	}
	joinToWorker := workflow.Edge{
		WorkflowID: workflowID, ID: "edge_join_worker", Key: "worker",
		TransitionGroupID: "group_join_worker", TargetNodeID: "node_worker",
		ContextMode:   workflow.ContextModeContinueSession,
		ContextSource: workflow.ContextSource{Kind: workflow.ContextSourceSelectedNode, NodeKey: "worker"},
	}

	classification := workflow.ClassifyWorkflowSessionReuse(workflow.SessionReuseAnalysisInput{
		Workflow: workflow.Definition{
			ID:          workflowID,
			DisplayName: "Converged Overwrite Paths",
			Nodes:       []workflow.Node{source, decision, pathA, pathB, worker, join},
			TransitionGroups: []workflow.TransitionGroup{
				{WorkflowID: workflowID, ID: "group_source", SourceNodeID: "node_source"},
				{WorkflowID: workflowID, ID: "group_decision_a", SourceNodeID: "node_decision"},
				{WorkflowID: workflowID, ID: "group_decision_b", SourceNodeID: "node_decision"},
				{WorkflowID: workflowID, ID: "group_path_a_worker", SourceNodeID: "node_path_a"},
				{WorkflowID: workflowID, ID: "group_worker_join", SourceNodeID: "node_worker"},
				{WorkflowID: workflowID, ID: "group_path_b_join", SourceNodeID: "node_path_b"},
				{WorkflowID: workflowID, ID: "group_join_worker", SourceNodeID: "node_join"},
			},
			Edges: []workflow.Edge{start, pathAEdge, pathBEdge, pathAToWorker, pathAToJoin, pathBToJoin, joinToWorker},
		},
		AcceptedBranches:     []workflow.Edge{start},
		CompletedCurrentNode: completedNode,
		RetainedAssociations: []workflow.SessionReuseAssociation{{SessionID: completedSessionID, CurrentNode: workerReference}},
	})

	if classification != workflow.SessionReuseThresholdPossibleReuse {
		t.Fatalf("classification = %q, want threshold_possible_reuse", classification)
	}
}
