package workflowsvc

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestServiceWorkflowGraphSaveAtomicallyRepairsSavedInvalidWorkflow(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Invalid Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, created.Workflow.ID)
	startID := workflowServiceNodeIDByKind(t, before, "start")
	terminalID := workflowServiceNodeIDByKind(t, before, "terminal")
	agentID := workflowServiceGraphEntityID("node-agent-" + created.Workflow.ID.String())
	graph := serverapi.WorkflowGraphDraftFromDefinition(before)
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID: agentID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder",
	})
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: workflowServiceGraphEntityID("group-start"), SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: workflowServiceGraphEntityID("group-done"), SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-start"), workflowServiceGraphEntityID("group-start"), "start", agentID, "Do work."),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-done"), workflowServiceGraphEntityID("group-done"), "done", terminalID, ""),
	)
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveAtomicallyDeletesFanOutTransitionBranch(t *testing.T) {
	ctx, service, workflowID := newWorkflowGraphAtomicFanOutFixture(t)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := serverapi.WorkflowGraphDraftFromDefinition(before)
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == workflowServiceGraphEntityID("edge-split-b-"+workflowID.String())
	})
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveAtomicallyDeletesNodeAndTransitionGroup(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftWithoutNode(
		before,
		workflowServiceNodeIDByKey(t, before, "implement"),
		workflowServiceNodeIDByKind(t, before, "terminal"),
	)
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveAtomicallyChangesFanOutSource(t *testing.T) {
	ctx, service, workflowID := newWorkflowGraphAtomicFanOutFixture(t)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := serverapi.WorkflowGraphDraftFromDefinition(before)
	for index := range graph.TransitionGroups {
		if graph.TransitionGroups[index].ID == workflowServiceGraphEntityID("group-split-"+workflowID.String()) {
			graph.TransitionGroups[index].SourceNodeID = workflowServiceGraphEntityID("node-prep-" + workflowID.String())
		}
	}
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveRejectsInvalidAndStaleWithoutMutation(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	invalid := serverapi.WorkflowGraphDraftFromDefinition(before)
	invalid.Nodes = append(invalid.Nodes, invalid.Nodes[0])
	rejected, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: workflowID, ExpectedVersion: before.Workflow.Version, Graph: invalid,
	})
	if err != nil || rejected.Saved || !workflowGraphSaveResponseHasBlocker(rejected, "validation_failed") {
		t.Fatalf("invalid save = %+v, err = %v", rejected, err)
	}
	assertWorkflowGraphAtomicUnchanged(t, ctx, service, before)

	changed := serverapi.WorkflowGraphDraftFromDefinition(before)
	changed.Nodes[0].DisplayName += " edited"
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, changed)
	current := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	rejected, err = service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: workflowID, ExpectedVersion: before.Workflow.Version, Graph: serverapi.WorkflowGraphDraftFromDefinition(before),
	})
	if err != nil || rejected.Saved || !workflowGraphSaveResponseHasBlocker(rejected, "version_changed") {
		t.Fatalf("stale save = %+v, err = %v", rejected, err)
	}
	assertWorkflowGraphAtomicUnchanged(t, ctx, service, current)
}

func TestServiceWorkflowGraphSaveCurrentNodeDeletionIsBlocked(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{
		ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Active graph reference", LabelIDs: []string{},
	})
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	removedNodeID := started.CurrentNodes[0].NodeID
	graph := workflowGraphDraftWithoutNode(before, removedNodeID, workflowServiceNodeIDByKind(t, before, "terminal"))

	preview := previewWorkflowGraphAtomicDraft(t, ctx, service, before, graph)
	nodeReference := serverapi.WorkflowGraphEntityReference{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: removedNodeID}
	if preview.Impact.ActiveCurrentNodeCount != 1 || !slices.Contains(preview.Impact.RemovedEntities, nodeReference) ||
		!slices.Equal(workflowServiceGraphSaveBlockerEntities(preview.Blockers, "node_task_references"), []serverapi.WorkflowGraphEntityReference{nodeReference}) {
		t.Fatalf("active Current Node preview = %+v", preview)
	}
	blocked := saveWorkflowGraphAtomicPreview(t, ctx, service, before, graph, preview)
	if blocked.Saved {
		t.Fatalf("active Current Node save = %+v, want blocked", blocked)
	}
	assertWorkflowGraphAtomicUnchanged(t, ctx, service, before)
}

func TestServiceWorkflowGraphSavePendingApprovalDeletionIsBlocked(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "next")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{
		ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Pending Approval reference", LabelIDs: []string{},
	})
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	source := workflowServiceCurrentNodeReference(t, workflow.TaskID(task.Task.ID), started.CurrentNodes[0])
	completed, err := service.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
		Source: source, TransitionID: "next", OutputValues: map[string]string{"prior_summary": "approved"},
	})
	if err != nil || completed.PendingApproval == nil {
		t.Fatalf("CompleteWorkflowTask = %+v, err = %v", completed, err)
	}
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	removedNodeID := workflowServiceNodeIDByKey(t, before, "implement")
	graph := workflowGraphDraftWithoutNode(before, removedNodeID, workflowServiceNodeIDByKind(t, before, "terminal"))

	preview := previewWorkflowGraphAtomicDraft(t, ctx, service, before, graph)
	nodeReference := serverapi.WorkflowGraphEntityReference{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: removedNodeID}
	if preview.Impact.PendingApprovalCount != 1 || !slices.Contains(preview.Impact.RemovedEntities, nodeReference) ||
		!slices.Equal(workflowServiceGraphSaveBlockerEntities(preview.Blockers, "node_task_references"), []serverapi.WorkflowGraphEntityReference{nodeReference}) {
		t.Fatalf("Pending Approval preview = %+v", preview)
	}
	blocked := saveWorkflowGraphAtomicPreview(t, ctx, service, before, graph, preview)
	if blocked.Saved {
		t.Fatalf("Pending Approval save = %+v, want blocked", blocked)
	}
	assertWorkflowGraphAtomicUnchanged(t, ctx, service, before)
}

func TestServiceWorkflowGraphSaveAllowsCompletedSessionProvenanceDeletion(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{
		ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Retained Session provenance", LabelIDs: []string{},
	})
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	beforeCompletion := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	removedNodeID := started.CurrentNodes[0].NodeID
	taskID := workflow.TaskID(task.Task.ID)
	reference := workflowServiceCurrentNodeReference(t, taskID, started.CurrentNodes[0])
	sessionID := bindWorkflowServiceSessionToTask(t, service, metadataStore, binding, taskID, started.CurrentNodes[0])
	service.currentNodeExecution = newManualMoveExecutionStub(service)
	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind: serverapi.WorkflowTaskCompleteActorUser, TaskID: task.Task.ID, TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "completed"}, Force: true,
	})
	implementNodeID := workflowServiceNodeIDByKey(t, beforeCompletion, "implement")
	if err != nil || completed.ForcedMove == nil || completed.ForcedMove.Outcome.Applied == nil ||
		len(completed.ForcedMove.Outcome.Applied.CurrentNodes) != 1 ||
		completed.ForcedMove.Outcome.Applied.CurrentNodes[0].NodeID != implementNodeID {
		t.Fatalf("CompleteWorkflowTask = %+v, err = %v", completed, err)
	}
	completed, err = service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind: serverapi.WorkflowTaskCompleteActorUser, TaskID: task.Task.ID, TransitionID: "done", Force: true,
	})
	if err != nil || completed.ForcedMove == nil || completed.ForcedMove.Outcome.Applied == nil ||
		len(completed.ForcedMove.Outcome.Applied.CurrentNodes) != 1 ||
		completed.ForcedMove.Outcome.Applied.CurrentNodes[0].NodeID != workflowServiceNodeIDByKind(t, beforeCompletion, "terminal") {
		t.Fatalf("complete implement Node = %+v, err = %v", completed, err)
	}
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftWithoutNode(before, removedNodeID, implementNodeID)
	preview := previewWorkflowGraphAtomicDraft(t, ctx, service, before, graph)
	if preview.Impact.ActiveCurrentNodeCount != 0 || preview.Impact.PendingApprovalCount != 0 {
		t.Fatalf("completed Session preview = %+v", preview)
	}
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, graph)
	if owner, err := service.store.TaskIDForSession(ctx, sessionID); err != nil || owner == nil || *owner != taskID {
		t.Fatalf("retained Session owner = %v, err = %v", owner, err)
	}
	if association, err := service.store.LatestTaskSessionForNode(ctx, reference); err != nil ||
		association.SessionID != sessionID || !association.CurrentNode.Equal(reference) {
		t.Fatalf("retained Session association = %+v, err = %v", association, err)
	}
}

func workflowGraphDraftWithoutNode(
	definition serverapi.WorkflowDefinition,
	nodeID string,
	replacementTargetID string,
) serverapi.WorkflowGraphDraft {
	graph := serverapi.WorkflowGraphDraftFromDefinition(definition)
	graph.Nodes = slices.DeleteFunc(graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) bool {
		return node.ID == nodeID
	})
	removedGroups := map[string]struct{}{}
	graph.TransitionGroups = slices.DeleteFunc(graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) bool {
		if group.SourceNodeID != nodeID {
			return false
		}
		removedGroups[group.ID] = struct{}{}
		return true
	})
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		if _, removed := removedGroups[edge.TransitionGroupID]; removed {
			return true
		}
		return false
	})
	for index := range graph.Edges {
		if graph.Edges[index].TargetNodeID == nodeID {
			graph.Edges[index].TargetNodeID = replacementTargetID
			if workflowServiceNodeByIDForGraphTest(definition, replacementTargetID).Kind == "terminal" {
				graph.Edges[index].PromptTemplate = ""
			}
		}
	}
	return graph
}

func workflowServiceNodeByIDForGraphTest(definition serverapi.WorkflowDefinition, nodeID string) serverapi.WorkflowNode {
	for _, node := range definition.Nodes {
		if node.ID == nodeID {
			return node
		}
	}
	panic("Workflow graph test replacement Node is missing: " + nodeID)
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
	planID := workflowServiceGraphEntityID("node-plan-" + workflowID.String())
	prepID := workflowServiceGraphEntityID("node-prep-" + workflowID.String())
	branchAID := workflowServiceGraphEntityID("node-a-" + workflowID.String())
	branchBID := workflowServiceGraphEntityID("node-b-" + workflowID.String())
	joinID := workflowServiceGraphEntityID("node-join-" + workflowID.String())
	graph := serverapi.WorkflowGraphDraftFromDefinition(current)
	graph.Nodes = append(graph.Nodes,
		serverapi.WorkflowGraphDraftNode{ID: planID, Key: "plan", Kind: "agent", DisplayName: "Plan", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: prepID, Key: "prep", Kind: "agent", DisplayName: "Prep", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: branchAID, Key: "a", Kind: "agent", DisplayName: "A", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: branchBID, Key: "b", Kind: "agent", DisplayName: "B", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: joinID, Key: "join", Kind: "join", DisplayName: "Join"},
	)
	startGroup := workflowServiceGraphEntityID("group-start-" + workflowID.String())
	prepGroup := workflowServiceGraphEntityID("group-prep-" + workflowID.String())
	splitGroup := workflowServiceGraphEntityID("group-split-" + workflowID.String())
	alternateGroup := workflowServiceGraphEntityID("group-alternate-" + workflowID.String())
	prepDoneGroup := workflowServiceGraphEntityID("group-prep-done-" + workflowID.String())
	joinAGroup := workflowServiceGraphEntityID("group-join-a-" + workflowID.String())
	joinBGroup := workflowServiceGraphEntityID("group-join-b-" + workflowID.String())
	joinDoneGroup := workflowServiceGraphEntityID("group-join-done-" + workflowID.String())
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
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-start-"+workflowID.String()), startGroup, "start", planID, "Plan."),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-prep-"+workflowID.String()), prepGroup, "prepare", prepID, "Prepare."),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-split-a-"+workflowID.String()), splitGroup, "a", branchAID, "A."),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-split-b-"+workflowID.String()), splitGroup, "b", branchBID, "B."),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-alternate-"+workflowID.String()), alternateGroup, "alternate", branchBID, "B."),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-prep-done-"+workflowID.String()), prepDoneGroup, "prep_done", terminalID, ""),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-join-a-"+workflowID.String()), joinAGroup, "join_a", joinID, ""),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-join-b-"+workflowID.String()), joinBGroup, "join_b", joinID, ""),
		workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-join-done-"+workflowID.String()), joinDoneGroup, "join_done", terminalID, ""),
	)
	saved := previewWorkflowGraphAtomicDraft(t, ctx, service, current, graph)
	response := saveWorkflowGraphAtomicPreview(t, ctx, service, current, graph, saved)
	if !response.Saved || !response.Changed {
		t.Fatalf("seed graph = %+v", response)
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

func previewWorkflowGraphAtomicDraft(
	t *testing.T,
	ctx context.Context,
	service *Service,
	before serverapi.WorkflowDefinition,
	graph serverapi.WorkflowGraphDraft,
) serverapi.WorkflowGraphSavePreviewResponse {
	t.Helper()
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID: before.Workflow.ID, ExpectedVersion: before.Workflow.Version, Graph: graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	return preview
}

func saveWorkflowGraphAtomicPreview(
	t *testing.T,
	ctx context.Context,
	service *Service,
	before serverapi.WorkflowDefinition,
	graph serverapi.WorkflowGraphDraft,
	preview serverapi.WorkflowGraphSavePreviewResponse,
) serverapi.WorkflowGraphSaveResponse {
	t.Helper()
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
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: before.Workflow.ID, ExpectedVersion: before.Workflow.Version, Graph: graph, Confirmation: confirmation,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	return saved
}

func assertWorkflowGraphAtomicChangedSave(
	t *testing.T,
	ctx context.Context,
	service *Service,
	before serverapi.WorkflowDefinition,
	graph serverapi.WorkflowGraphDraft,
) {
	t.Helper()
	saved := saveWorkflowGraphAtomicPreview(t, ctx, service, before, graph, previewWorkflowGraphAtomicDraft(t, ctx, service, before, graph))
	if !saved.Saved || !saved.Changed {
		t.Fatalf("save = %+v", saved)
	}
	after := getWorkflowGraphAtomicDefinition(t, ctx, service, before.Workflow.ID)
	if after.Workflow.Version != before.Workflow.Version+1 {
		t.Fatalf("Workflow Version = %d, want %d", after.Workflow.Version, before.Workflow.Version+1)
	}
	reloadedJSON, err := json.Marshal(serverapi.WorkflowGraphDraftFromDefinition(after))
	if err != nil {
		t.Fatalf("marshal reloaded authored graph: %v", err)
	}
	requestedJSON, err := json.Marshal(graph)
	if err != nil {
		t.Fatalf("marshal requested authored graph: %v", err)
	}
	if string(reloadedJSON) != string(requestedJSON) {
		t.Fatalf("reloaded authored graph = %s, want %s", reloadedJSON, requestedJSON)
	}
	validated, err := service.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{
		WorkflowID: before.Workflow.ID, Mode: serverapi.WorkflowValidationModeExecution,
	})
	if err != nil {
		t.Fatalf("ValidateWorkflow execution: %v", err)
	}
	if !validated.Valid {
		t.Fatalf("reloaded Workflow execution validation = %+v", validated)
	}
}

func assertWorkflowGraphAtomicUnchanged(t *testing.T, ctx context.Context, service *Service, before serverapi.WorkflowDefinition) {
	t.Helper()
	after := getWorkflowGraphAtomicDefinition(t, ctx, service, before.Workflow.ID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Workflow changed after rejected save")
	}
}
