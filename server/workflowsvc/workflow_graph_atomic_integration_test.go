package workflowsvc

import (
	"context"
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
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveAtomicallyDeletesFanOutTransitionBranch(t *testing.T) {
	ctx, service, workflowID := newWorkflowGraphAtomicFanOutFixture(t)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftFromDefinition(before)
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == "edge-split-b-"+workflowID.String()
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
	graph := workflowGraphDraftFromDefinition(before)
	for index := range graph.TransitionGroups {
		if graph.TransitionGroups[index].ID == "group-split-"+workflowID.String() {
			graph.TransitionGroups[index].SourceNodeID = "node-prep-" + workflowID.String()
		}
	}
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, graph)
}

func TestServiceWorkflowGraphSaveRejectsInvalidAndStaleWithoutMutation(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	invalid := workflowGraphDraftFromDefinition(before)
	invalid.Nodes = append(invalid.Nodes, invalid.Nodes[0])
	rejected, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: workflowID, ExpectedVersion: before.Workflow.Version, Graph: invalid,
	})
	if err != nil || rejected.Saved || !workflowGraphSaveResponseHasBlocker(rejected, "validation_failed") {
		t.Fatalf("invalid save = %+v, err = %v", rejected, err)
	}
	assertWorkflowGraphAtomicUnchanged(t, ctx, service, before)

	changed := workflowGraphDraftFromDefinition(before)
	changed.Nodes[0].DisplayName += " edited"
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, before, changed)
	current := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	rejected, err = service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: workflowID, ExpectedVersion: before.Workflow.Version, Graph: workflowGraphDraftFromDefinition(before),
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
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	service.currentNodeExecution = newWorkflowGraphAtomicCompletionExecution(service)
	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind: serverapi.WorkflowTaskCompleteActorUser, TaskID: task.Task.ID,
		TransitionID: "next", OutputValues: map[string]string{"prior_summary": "approved"}, Force: true,
	})
	if err != nil || completed.PendingApprovalID == nil {
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
	service.currentNodeExecution = newWorkflowGraphAtomicCompletionExecution(service)
	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind: serverapi.WorkflowTaskCompleteActorUser, TaskID: task.Task.ID, TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "completed"}, Force: true,
	})
	implementNodeID := workflowServiceNodeIDByKey(t, beforeCompletion, "implement")
	if err != nil || len(completed.CurrentNodes) != 1 || completed.CurrentNodes[0].NodeID != implementNodeID {
		t.Fatalf("CompleteWorkflowTask = %+v, err = %v", completed, err)
	}
	completed, err = service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind: serverapi.WorkflowTaskCompleteActorUser, TaskID: task.Task.ID, TransitionID: "done", Force: true,
	})
	if err != nil || len(completed.CurrentNodes) != 1 ||
		completed.CurrentNodes[0].NodeID != workflowServiceNodeIDByKind(t, beforeCompletion, "terminal") {
		t.Fatalf("complete implement Node = %+v, err = %v", completed, err)
	}
	before := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftWithoutNode(before, removedNodeID, implementNodeID)
	preview := previewWorkflowGraphAtomicDraft(t, ctx, service, before, graph)
	if preview.Impact.ActiveCurrentNodeCount != 0 || preview.Impact.PendingApprovalCount != 0 {
		t.Fatalf("completed Session preview = %+v", preview)
	}
	saved := saveWorkflowGraphAtomicPreview(t, ctx, service, before, graph, preview)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("completed Session save = %+v", saved)
	}
	if owner, err := service.store.TaskIDForSession(ctx, sessionID); err != nil || owner == nil || *owner != taskID {
		t.Fatalf("retained Session owner = %v, err = %v", owner, err)
	}
	if association, err := service.store.LatestTaskSessionForNode(ctx, reference); err != nil ||
		association.SessionID != sessionID || !association.CurrentNode.Equal(reference) {
		t.Fatalf("retained Session association = %+v, err = %v", association, err)
	}
}

type workflowGraphAtomicCompletionExecution struct {
	*currentNodeCompletionExecutionStub
}

func newWorkflowGraphAtomicCompletionExecution(service *Service) *workflowGraphAtomicCompletionExecution {
	return &workflowGraphAtomicCompletionExecution{
		currentNodeCompletionExecutionStub: &currentNodeCompletionExecutionStub{store: service.store},
	}
}

func (e *workflowGraphAtomicCompletionExecution) CompleteIdleCurrentNode(
	ctx context.Context,
	selector workflowstore.IdleCurrentNodeSelector,
	transitionID string,
	outputValues map[string]string,
	commentary string,
) (workflowstore.CurrentNodeCompletionResult, error) {
	source, err := e.store.ResolveIdleExecutableCurrentNode(ctx, selector)
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	return e.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
		Source: source.Reference, TransitionID: transitionID, OutputValues: outputValues, Commentary: commentary,
	})
}

func workflowGraphDraftWithoutNode(
	definition serverapi.WorkflowDefinition,
	nodeID string,
	replacementTargetID string,
) serverapi.WorkflowGraphDraft {
	graph := workflowGraphDraftFromDefinition(definition)
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
	graph := workflowGraphDraftFromDefinition(current)
	graph.Nodes = append(graph.Nodes,
		serverapi.WorkflowGraphDraftNode{ID: "node-plan-" + workflowID.String(), Key: "plan", Kind: "agent", DisplayName: "Plan", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-prep-" + workflowID.String(), Key: "prep", Kind: "agent", DisplayName: "Prep", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-a-" + workflowID.String(), Key: "a", Kind: "agent", DisplayName: "A", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-b-" + workflowID.String(), Key: "b", Kind: "agent", DisplayName: "B", SubagentRole: "coder"},
	)
	startGroup := "group-start-" + workflowID.String()
	splitGroup := "group-split-" + workflowID.String()
	prepDoneGroup := "group-prep-done-" + workflowID.String()
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroup, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: splitGroup, SourceNodeID: "node-plan-" + workflowID.String(), TransitionID: "split", DisplayName: "Split"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: prepDoneGroup, SourceNodeID: "node-prep-" + workflowID.String(), TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		workflowGraphAtomicEdge("edge-start-"+workflowID.String(), startGroup, "start", "node-plan-"+workflowID.String(), "Plan."),
		workflowGraphAtomicEdge("edge-split-a-"+workflowID.String(), splitGroup, "a", "node-a-"+workflowID.String(), "A."),
		workflowGraphAtomicEdge("edge-split-b-"+workflowID.String(), splitGroup, "b", "node-b-"+workflowID.String(), "B."),
		workflowGraphAtomicEdge("edge-prep-done-"+workflowID.String(), prepDoneGroup, "done", terminalID, ""),
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
}

func assertWorkflowGraphAtomicUnchanged(t *testing.T, ctx context.Context, service *Service, before serverapi.WorkflowDefinition) {
	t.Helper()
	after := getWorkflowGraphAtomicDefinition(t, ctx, service, before.Workflow.ID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Workflow changed after rejected save")
	}
}
