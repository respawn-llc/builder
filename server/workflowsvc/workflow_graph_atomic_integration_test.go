package workflowsvc

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestServiceWorkflowGraphSaveAtomicallyDeletesFanOutTransitionBranch(t *testing.T) {
	ctx, service, workflowID := newWorkflowGraphAtomicFanOutFixture(t)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftFromDefinition(before)
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == "edge-split-b-"+workflowID.String()
	})
	assertWorkflowGraphAtomicSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveAtomicallyDeletesNodeAndTransitionGroup(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftFromDefinition(before)
	removedNodeID := workflowServiceNodeIDByKey(t, before, "implement")
	terminalNodeID := workflowServiceNodeIDByKind(t, before, "terminal")
	graph.Nodes = slices.DeleteFunc(graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) bool {
		return node.ID == removedNodeID
	})
	graph.TransitionGroups = slices.DeleteFunc(graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) bool {
		return group.ID == "group-done-"+workflowID.String()
	})
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == "edge-done-"+workflowID.String()
	})
	for index := range graph.Edges {
		if graph.Edges[index].ID == "edge-next-"+workflowID.String() {
			graph.Edges[index].TargetNodeID = terminalNodeID
			graph.Edges[index].PromptTemplate = ""
		}
	}
	assertWorkflowGraphAtomicSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveAtomicallyChangesFanOutSource(t *testing.T) {
	ctx, service, workflowID := newWorkflowGraphAtomicFanOutFixture(t)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftFromDefinition(before)
	for index := range graph.TransitionGroups {
		if graph.TransitionGroups[index].ID == "group-split-"+workflowID.String() {
			graph.TransitionGroups[index].SourceNodeID = "node-prep-" + workflowID.String()
		}
	}
	assertWorkflowGraphAtomicSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveAtomicallyRepairsSavedInvalidWorkflow(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Invalid Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, created.Workflow.ID)
	startID := workflowServiceNodeIDByKind(t, before, "start")
	terminalID := workflowServiceNodeIDByKind(t, before, "terminal")
	agentID := "node-agent-" + created.Workflow.ID.String()
	graph := workflowGraphDraftFromDefinition(before)
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID: agentID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder",
	})
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-start", SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-done", SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		workflowGraphAtomicEdge("edge-start", "group-start", "start", agentID, "Do work."),
		workflowGraphAtomicEdge("edge-done", "group-done", "done", terminalID, ""),
	)
	assertWorkflowGraphAtomicSave(t, ctx, service, before, graph)
}

func newWorkflowGraphAtomicFanOutFixture(t *testing.T) (context.Context, *Service, runtimeids.WorkflowID) {
	t.Helper()
	ctx, service, _ := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Fan-Out Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.Workflow.ID
	current := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	startID := workflowServiceNodeIDByKind(t, current, "start")
	terminalID := workflowServiceNodeIDByKind(t, current, "terminal")
	planID := "node-plan-" + workflowID.String()
	prepID := "node-prep-" + workflowID.String()
	branchAID := "node-a-" + workflowID.String()
	branchBID := "node-b-" + workflowID.String()
	joinID := "node-join-" + workflowID.String()
	graph := workflowGraphDraftFromDefinition(current)
	graph.Nodes = append(graph.Nodes,
		serverapi.WorkflowGraphDraftNode{ID: planID, Key: "plan", Kind: "agent", DisplayName: "Plan", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: prepID, Key: "prep", Kind: "agent", DisplayName: "Prep", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: branchAID, Key: "a", Kind: "agent", DisplayName: "A", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: branchBID, Key: "b", Kind: "agent", DisplayName: "B", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: joinID, Key: "join", Kind: "join", DisplayName: "Join"},
	)
	startGroup := "group-start-" + workflowID.String()
	prepGroup := "group-prep-" + workflowID.String()
	splitGroup := "group-split-" + workflowID.String()
	alternateGroup := "group-alternate-" + workflowID.String()
	prepDoneGroup := "group-prep-done-" + workflowID.String()
	joinAGroup := "group-join-a-" + workflowID.String()
	joinBGroup := "group-join-b-" + workflowID.String()
	joinDoneGroup := "group-join-done-" + workflowID.String()
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroup, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: prepGroup, SourceNodeID: planID, TransitionID: "prepare", DisplayName: "Prepare"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: splitGroup, SourceNodeID: planID, TransitionID: "split", DisplayName: "Split"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: alternateGroup, SourceNodeID: planID, TransitionID: "alternate", DisplayName: "Alternate"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: prepDoneGroup, SourceNodeID: prepID, TransitionID: "prep_done", DisplayName: "Done"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: joinAGroup, SourceNodeID: branchAID, TransitionID: "join_a", DisplayName: "Join"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: joinBGroup, SourceNodeID: branchBID, TransitionID: "join_b", DisplayName: "Join"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: joinDoneGroup, SourceNodeID: joinID, TransitionID: "join_done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		workflowGraphAtomicEdge("edge-start-"+workflowID.String(), startGroup, "start", planID, "Plan."),
		workflowGraphAtomicEdge("edge-prep-"+workflowID.String(), prepGroup, "prepare", prepID, "Prepare."),
		workflowGraphAtomicEdge("edge-split-a-"+workflowID.String(), splitGroup, "a", branchAID, "A."),
		workflowGraphAtomicEdge("edge-split-b-"+workflowID.String(), splitGroup, "b", branchBID, "B."),
		workflowGraphAtomicEdge("edge-alternate-"+workflowID.String(), alternateGroup, "alternate", branchBID, "B."),
		workflowGraphAtomicEdge("edge-prep-done-"+workflowID.String(), prepDoneGroup, "prep_done", terminalID, ""),
		workflowGraphAtomicEdge("edge-join-a-"+workflowID.String(), joinAGroup, "join_a", joinID, ""),
		workflowGraphAtomicEdge("edge-join-b-"+workflowID.String(), joinBGroup, "join_b", joinID, ""),
		workflowGraphAtomicEdge("edge-join-done-"+workflowID.String(), joinDoneGroup, "join_done", terminalID, ""),
	)
	saved := saveWorkflowGraphAtomicDraft(t, ctx, service, workflowID, current.Workflow.Version, graph)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("seed graph = %+v", saved)
	}
	return ctx, service, workflowID
}

func workflowGraphAtomicEdge(id, groupID, key, targetID, prompt string) serverapi.WorkflowGraphDraftEdge {
	return serverapi.WorkflowGraphDraftEdge{
		ID: id, TransitionGroupID: groupID, Key: key, TargetNodeID: targetID,
		AssigneeSelection: "configured", ThinkingSelection: "configured",
		ContextMode: "new_session", ContextSource: serverapi.WorkflowContextSource{Kind: "immediate_source"},
		PromptTemplate: prompt,
	}
}

func getWorkflowGraphAtomicDefinition(t *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID) serverapi.WorkflowDefinition {
	t.Helper()
	response, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	return response.Definition
}

func saveWorkflowGraphAtomicDraft(t *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID, version int64, graph serverapi.WorkflowGraphDraft) serverapi.WorkflowGraphSaveResponse {
	t.Helper()
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID: workflowID, ExpectedVersion: version, Graph: graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	var confirmation *serverapi.WorkflowGraphSaveConfirmation
	if preview.ConfirmationRequired {
		confirmation = &serverapi.WorkflowGraphSaveConfirmation{
			ExpectedRemovedNodeGroupCount:       preview.Impact.RemovedNodeGroupCount,
			ExpectedRemovedNodeCount:            preview.Impact.RemovedNodeCount,
			ExpectedRemovedTransitionGroupCount: preview.Impact.RemovedTransitionGroupCount,
			ExpectedRemovedEdgeCount:            preview.Impact.RemovedEdgeCount,
			ExpectedNodeTaskReferenceCount:      preview.Impact.NodeTaskReferenceCount,
			ExpectedEdgeTaskReferenceCount:      preview.Impact.EdgeTaskReferenceCount,
		}
	}
	response, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: workflowID, ExpectedVersion: version, Graph: graph, Confirmation: confirmation,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	return response
}

func assertWorkflowGraphAtomicSave(t *testing.T, ctx context.Context, service *Service, before serverapi.WorkflowDefinition, graph serverapi.WorkflowGraphDraft) {
	t.Helper()
	saved := saveWorkflowGraphAtomicDraft(t, ctx, service, before.Workflow.ID, before.Workflow.Version, graph)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("save = %+v", saved)
	}
	after := getWorkflowGraphAtomicDefinition(t, ctx, service, before.Workflow.ID)
	if after.Workflow.Version != before.Workflow.Version+1 {
		t.Fatalf("Workflow Version = %d, want %d", after.Workflow.Version, before.Workflow.Version+1)
	}
	got, _ := json.Marshal(workflowGraphDraftFromDefinition(after))
	want, _ := json.Marshal(graph)
	if string(got) != string(want) {
		t.Fatalf("reloaded graph = %s, want %s", got, want)
	}
}
