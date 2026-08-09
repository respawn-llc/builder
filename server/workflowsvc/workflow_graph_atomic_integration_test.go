package workflowsvc

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestServiceWorkflowGraphSaveAtomicallyDeletesFanOutTransitionBranch(t *testing.T) {
	ctx, service, workflowID := newWorkflowGraphAtomicFanOutSourceFixture(t)
	before := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftFromDefinition(before)
	removedEdgeID := "edge-split-b-" + workflowID.String()
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == removedEdgeID
	})

	assertInvalidWorkflowGraphSavePreservesDefinition(t, ctx, service, before)
	saved := saveWorkflowGraphAtomicIntegrationDraft(t, ctx, service, workflowID, before.Workflow.Version, graph)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("SaveWorkflowGraph delete Fan-Out Transition Branch = %+v, want changed save", saved)
	}

	after := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	assertWorkflowGraphAtomicIntegrationResult(t, before, after, graph)
	assertWorkflowGraphAtomicIntegrationExecutionValid(t, ctx, service, workflowID)
	assertStaleWorkflowGraphSavePreservesDefinition(t, ctx, service, workflowID, before.Workflow.Version, workflowGraphDraftFromDefinition(before), after)
}

func TestServiceWorkflowGraphSaveAtomicallyDeletesNodeAndTransitionGroup(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	before := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftFromDefinition(before)
	removedNodeID := workflowServiceNodeIDByKey(t, before, "implement")
	terminalNodeID := workflowServiceNodeIDByKind(t, before, "terminal")
	removedTransitionGroupID := "group-done-" + workflowID.String()
	removedEdgeID := "edge-done-" + workflowID.String()
	graph.Nodes = slices.DeleteFunc(graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) bool {
		return node.ID == removedNodeID
	})
	graph.TransitionGroups = slices.DeleteFunc(graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) bool {
		return group.ID == removedTransitionGroupID
	})
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == removedEdgeID
	})
	for index := range graph.Edges {
		if graph.Edges[index].ID == "edge-next-"+workflowID.String() {
			graph.Edges[index].TargetNodeID = terminalNodeID
			graph.Edges[index].PromptTemplate = ""
		}
	}

	assertInvalidWorkflowGraphSavePreservesDefinition(t, ctx, service, before)
	saved := saveWorkflowGraphAtomicIntegrationDraft(t, ctx, service, workflowID, before.Workflow.Version, graph)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("SaveWorkflowGraph delete Node plus Transition Group = %+v, want changed save", saved)
	}

	after := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	assertWorkflowGraphAtomicIntegrationResult(t, before, after, graph)
	assertWorkflowGraphAtomicIntegrationExecutionValid(t, ctx, service, workflowID)
	assertStaleWorkflowGraphSavePreservesDefinition(t, ctx, service, workflowID, before.Workflow.Version, workflowGraphDraftFromDefinition(before), after)
}

func TestServiceWorkflowGraphSaveAtomicallyChangesFanOutSource(t *testing.T) {
	ctx, service, workflowID := newWorkflowGraphAtomicFanOutSourceFixture(t)
	before := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	graph := workflowGraphDraftFromDefinition(before)
	prepNodeID := "node-prep-" + workflowID.String()
	splitGroupID := "group-split-" + workflowID.String()
	for index := range graph.TransitionGroups {
		if graph.TransitionGroups[index].ID == splitGroupID {
			graph.TransitionGroups[index].SourceNodeID = prepNodeID
		}
	}

	assertInvalidWorkflowGraphSavePreservesDefinition(t, ctx, service, before)
	saved := saveWorkflowGraphAtomicIntegrationDraft(t, ctx, service, workflowID, before.Workflow.Version, graph)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("SaveWorkflowGraph change Fan-Out source = %+v, want changed save", saved)
	}

	after := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	assertWorkflowGraphAtomicIntegrationResult(t, before, after, graph)
	if got := workflowServiceTransitionGroupByID(t, after, splitGroupID).SourceNodeID; got != prepNodeID {
		t.Fatalf("reloaded Fan-Out source = %q, want %q", got, prepNodeID)
	}
	assertWorkflowGraphAtomicIntegrationExecutionValid(t, ctx, service, workflowID)
	assertStaleWorkflowGraphSavePreservesDefinition(t, ctx, service, workflowID, before.Workflow.Version, workflowGraphDraftFromDefinition(before), after)
}

func TestServiceWorkflowGraphSaveAtomicallyRepairsSavedInvalidWorkflow(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Invalid Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.Workflow.ID
	before := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	initialValidation, err := service.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{
		WorkflowID: workflowID,
		Mode:       serverapi.WorkflowValidationModeExecution,
	})
	if err != nil {
		t.Fatalf("ValidateWorkflow initial execution: %v", err)
	}
	if initialValidation.Valid {
		t.Fatalf("initial Workflow validation = %+v, want saved invalid Workflow", initialValidation)
	}

	startNodeID := workflowServiceNodeIDByKind(t, before, "start")
	terminalNodeID := workflowServiceNodeIDByKind(t, before, "terminal")
	agentNodeID := "node-agent-" + workflowID.String()
	startGroupID := "group-start-" + workflowID.String()
	doneGroupID := "group-done-" + workflowID.String()
	graph := workflowGraphDraftFromDefinition(before)
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID:           agentNodeID,
		Key:          "agent",
		Kind:         "agent",
		DisplayName:  "Agent",
		SubagentRole: "coder",
	})
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroupID, SourceNodeID: startNodeID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: doneGroupID, SourceNodeID: agentNodeID, TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		workflowGraphAtomicIntegrationEdge("edge-start-"+workflowID.String(), startGroupID, "start", agentNodeID, "Do work."),
		workflowGraphAtomicIntegrationEdge("edge-done-"+workflowID.String(), doneGroupID, "done", terminalNodeID, ""),
	)

	assertInvalidWorkflowGraphSavePreservesDefinition(t, ctx, service, before)
	saved := saveWorkflowGraphAtomicIntegrationDraft(t, ctx, service, workflowID, before.Workflow.Version, graph)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("SaveWorkflowGraph repair saved invalid Workflow = %+v, want changed save", saved)
	}

	after := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	assertWorkflowGraphAtomicIntegrationResult(t, before, after, graph)
	assertWorkflowGraphAtomicIntegrationExecutionValid(t, ctx, service, workflowID)
	assertStaleWorkflowGraphSavePreservesDefinition(t, ctx, service, workflowID, before.Workflow.Version, workflowGraphDraftFromDefinition(before), after)
}

func TestServiceWorkflowGraphSaveCurrentNodeReferencesBlockDeletion(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      "Active graph reference",
		LabelIDs:   []string{},
	})
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)

	before := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	agentNodeID := workflowServiceNodeIDByKey(t, before, "agent")
	if len(started.CurrentNodes) != 1 || started.CurrentNodes[0].NodeID != agentNodeID {
		t.Fatalf("started Current Nodes = %+v, want active Node %q", started.CurrentNodes, agentNodeID)
	}
	terminalNodeID := workflowServiceNodeIDByKind(t, before, "terminal")
	doneGroupID := "group-done-" + workflowID.String()
	doneEdgeID := "edge-done-" + workflowID.String()
	graph := workflowGraphDraftFromDefinition(before)
	graph.Nodes = slices.DeleteFunc(graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) bool {
		return node.ID == agentNodeID
	})
	graph.TransitionGroups = slices.DeleteFunc(graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) bool {
		return group.ID == doneGroupID
	})
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == doneEdgeID
	})
	for index := range graph.Edges {
		if graph.Edges[index].ID == "edge-start-"+workflowID.String() {
			graph.Edges[index].TargetNodeID = terminalNodeID
			graph.Edges[index].PromptTemplate = ""
		}
	}

	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: before.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave remove active Current Node: %v", err)
	}
	wantRemoved := []serverapi.WorkflowGraphEntityReference{
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: doneEdgeID},
		{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: agentNodeID},
		{EntityType: serverapi.WorkflowGraphEntityTypeTransitionGroup, EntityID: doneGroupID},
	}
	if !preview.Changed ||
		!preview.ConfirmationRequired ||
		preview.Impact.NodeTaskReferenceCount != 1 ||
		preview.Impact.EdgeTaskReferenceCount != 0 ||
		preview.Impact.ActiveCurrentNodeCount != 1 ||
		!slices.Equal(preview.Impact.RemovedEntities, wantRemoved) {
		t.Fatalf("active Current Node removal preview = %+v, want aggregate Task references and exact impact %+v", preview, wantRemoved)
	}
	wantNodeReference := []serverapi.WorkflowGraphEntityReference{{
		EntityType: serverapi.WorkflowGraphEntityTypeNode,
		EntityID:   agentNodeID,
	}}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "node_task_references"); !slices.Equal(got, wantNodeReference) {
		t.Fatalf("node_task_references affected entities = %+v, want %+v", got, wantNodeReference)
	}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "confirmation_required"); !slices.Equal(got, wantRemoved) {
		t.Fatalf("confirmation_required affected entities = %+v, want %+v", got, wantRemoved)
	}

	blocked, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: before.Workflow.Version,
		Graph:           graph,
		Confirmation:    workflowGraphAtomicIntegrationConfirmation(preview.Impact),
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph remove active Current Node: %v", err)
	}
	if blocked.Saved {
		t.Fatalf("active Current Node save = %+v, want blocked", blocked)
	}
	if got := workflowServiceGraphSaveBlockerEntities(blocked.Blockers, "node_task_references"); !slices.Equal(got, wantNodeReference) {
		t.Fatalf("save node_task_references affected entities = %+v, want %+v", got, wantNodeReference)
	}
	assertWorkflowGraphAtomicIntegrationDefinitionUnchanged(t, ctx, service, before)
}

func TestServiceWorkflowGraphSavePendingApprovalReferencesBlockDeletion(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "next")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      "Pending Approval graph reference",
		LabelIDs:   []string{},
	})
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	before := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	planNodeID := workflowServiceNodeIDByKey(t, before, "plan")
	if len(started.CurrentNodes) != 1 || started.CurrentNodes[0].NodeID != planNodeID {
		t.Fatalf("started Current Nodes = %+v, want Plan Node %q", started.CurrentNodes, planNodeID)
	}
	useWorkflowGraphAtomicCurrentNodeController(t, service)
	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
		TaskID:       task.Task.ID,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "approved plan"},
		Force:        true,
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask create Pending Approval: %v", err)
	}
	if completed.PendingApprovalID == nil || len(completed.CurrentNodes) != 0 {
		t.Fatalf("completion response = %+v, want retained source and Pending Approval", completed)
	}

	implementNodeID := workflowServiceNodeIDByKey(t, before, "implement")
	terminalNodeID := workflowServiceNodeIDByKind(t, before, "terminal")
	nextGroupID := "group-next-" + workflowID.String()
	nextEdgeID := "edge-next-" + workflowID.String()
	doneGroupID := "group-done-" + workflowID.String()
	doneEdgeID := "edge-done-" + workflowID.String()
	graph := workflowGraphDraftFromDefinition(before)
	graph.Nodes = slices.DeleteFunc(graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) bool {
		return node.ID == implementNodeID
	})
	graph.TransitionGroups = slices.DeleteFunc(graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) bool {
		return group.ID == doneGroupID
	})
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == nextEdgeID || edge.ID == doneEdgeID
	})
	graph.Edges = append(graph.Edges, workflowGraphAtomicIntegrationEdge(
		"edge-next-replacement-"+workflowID.String(),
		nextGroupID,
		"next",
		terminalNodeID,
		"",
	))

	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: before.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave remove Pending Approval target: %v", err)
	}
	wantRemoved := []serverapi.WorkflowGraphEntityReference{
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: doneEdgeID},
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: nextEdgeID},
		{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: implementNodeID},
		{EntityType: serverapi.WorkflowGraphEntityTypeTransitionGroup, EntityID: doneGroupID},
	}
	if !preview.Changed ||
		!preview.ConfirmationRequired ||
		preview.Impact.NodeTaskReferenceCount != 1 ||
		preview.Impact.EdgeTaskReferenceCount != 1 ||
		preview.Impact.PendingApprovalCount != 1 ||
		!slices.Equal(preview.Impact.RemovedEntities, wantRemoved) {
		t.Fatalf("Pending Approval target removal preview = %+v, want aggregate Task references and exact impact %+v", preview, wantRemoved)
	}
	wantNodeReference := []serverapi.WorkflowGraphEntityReference{{
		EntityType: serverapi.WorkflowGraphEntityTypeNode,
		EntityID:   implementNodeID,
	}}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "node_task_references"); !slices.Equal(got, wantNodeReference) {
		t.Fatalf("Pending Approval node_task_references affected entities = %+v, want %+v", got, wantNodeReference)
	}
	wantEdgeReference := []serverapi.WorkflowGraphEntityReference{{
		EntityType: serverapi.WorkflowGraphEntityTypeEdge,
		EntityID:   nextEdgeID,
	}}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "edge_task_references"); !slices.Equal(got, wantEdgeReference) {
		t.Fatalf("Pending Approval edge_task_references affected entities = %+v, want %+v", got, wantEdgeReference)
	}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "confirmation_required"); !slices.Equal(got, wantRemoved) {
		t.Fatalf("Pending Approval confirmation_required affected entities = %+v, want %+v", got, wantRemoved)
	}

	blocked, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: before.Workflow.Version,
		Graph:           graph,
		Confirmation:    workflowGraphAtomicIntegrationConfirmation(preview.Impact),
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph remove Pending Approval target: %v", err)
	}
	if blocked.Saved {
		t.Fatalf("Pending Approval target save = %+v, want blocked", blocked)
	}
	if got := workflowServiceGraphSaveBlockerEntities(blocked.Blockers, "node_task_references"); !slices.Equal(got, wantNodeReference) {
		t.Fatalf("save Pending Approval node_task_references affected entities = %+v, want %+v", got, wantNodeReference)
	}
	if got := workflowServiceGraphSaveBlockerEntities(blocked.Blockers, "edge_task_references"); !slices.Equal(got, wantEdgeReference) {
		t.Fatalf("save Pending Approval edge_task_references affected entities = %+v, want %+v", got, wantEdgeReference)
	}
	assertWorkflowGraphAtomicIntegrationDefinitionUnchanged(t, ctx, service, before)
}

func TestServiceWorkflowGraphSaveAllowsDeletingNodeWithCompletedSessionProvenance(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	current := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	agentNodeID := workflowServiceNodeIDByKey(t, current, "agent")
	terminalNodeID := workflowServiceNodeIDByKind(t, current, "terminal")
	reviewNodeID := "node-review-" + workflowID.String()
	reviewDoneGroupID := "group-review-done-" + workflowID.String()
	reviewDoneEdgeID := "edge-review-done-" + workflowID.String()
	graph := workflowGraphDraftFromDefinition(current)
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID:           reviewNodeID,
		Key:          "review",
		Kind:         "agent",
		DisplayName:  "Review",
		SubagentRole: "coder",
	})
	for index := range graph.Edges {
		if graph.Edges[index].ID == "edge-done-"+workflowID.String() {
			graph.Edges[index].TargetNodeID = reviewNodeID
			graph.Edges[index].PromptTemplate = "Review the work."
		}
	}
	graph.TransitionGroups = append(graph.TransitionGroups, serverapi.WorkflowGraphDraftTransitionGroup{
		ID:           reviewDoneGroupID,
		SourceNodeID: reviewNodeID,
		TransitionID: "finish",
		DisplayName:  "Done",
	})
	graph.Edges = append(graph.Edges, workflowGraphAtomicIntegrationEdge(
		reviewDoneEdgeID,
		reviewDoneGroupID,
		"finish",
		terminalNodeID,
		"",
	))
	added := saveWorkflowGraphAtomicIntegrationDraft(t, ctx, service, workflowID, current.Workflow.Version, graph)
	if !added.Saved || !added.Changed {
		t.Fatalf("SaveWorkflowGraph add review Node = %+v, want changed save", added)
	}

	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      "Retained Session provenance",
		LabelIDs:   []string{},
	})
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	if len(started.CurrentNodes) != 1 || started.CurrentNodes[0].NodeID != agentNodeID {
		t.Fatalf("started Current Nodes = %+v, want Agent Node %q", started.CurrentNodes, agentNodeID)
	}
	taskID := workflow.TaskID(task.Task.ID)
	agentReference := workflowServiceCurrentNodeReference(t, taskID, started.CurrentNodes[0])
	sessionID := bindWorkflowServiceSessionToTask(t, service, metadataStore, binding, taskID, started.CurrentNodes[0])
	service.currentNodeExecution = newWorkflowGraphAtomicCompletionExecution(service)

	completedAgent, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
		TaskID:       task.Task.ID,
		TransitionID: "done",
		Force:        true,
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask Agent Node: %v", err)
	}
	if completedAgent.PendingApprovalID != nil ||
		len(completedAgent.CurrentNodes) != 1 ||
		completedAgent.CurrentNodes[0].NodeID != reviewNodeID {
		t.Fatalf("Agent completion = %+v, want Review Current Node", completedAgent)
	}
	completedReview, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
		TaskID:       task.Task.ID,
		TransitionID: "finish",
		Force:        true,
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask Review Node: %v", err)
	}
	if completedReview.PendingApprovalID != nil ||
		len(completedReview.CurrentNodes) != 1 ||
		completedReview.CurrentNodes[0].NodeID != terminalNodeID {
		t.Fatalf("Review completion = %+v, want Terminal Current Node", completedReview)
	}

	before := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	doneGroupID := "group-done-" + workflowID.String()
	doneEdgeID := "edge-done-" + workflowID.String()
	graph = workflowGraphDraftFromDefinition(before)
	graph.Nodes = slices.DeleteFunc(graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) bool {
		return node.ID == agentNodeID
	})
	graph.TransitionGroups = slices.DeleteFunc(graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) bool {
		return group.ID == doneGroupID
	})
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == doneEdgeID
	})
	for index := range graph.Edges {
		if graph.Edges[index].ID == "edge-start-"+workflowID.String() {
			graph.Edges[index].TargetNodeID = reviewNodeID
			graph.Edges[index].PromptTemplate = "Review the work."
		}
	}
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: before.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave remove historical Session Node: %v", err)
	}
	wantRemoved := []serverapi.WorkflowGraphEntityReference{
		{EntityType: serverapi.WorkflowGraphEntityTypeEdge, EntityID: doneEdgeID},
		{EntityType: serverapi.WorkflowGraphEntityTypeNode, EntityID: agentNodeID},
		{EntityType: serverapi.WorkflowGraphEntityTypeTransitionGroup, EntityID: doneGroupID},
	}
	if !preview.Changed ||
		!preview.ConfirmationRequired ||
		preview.Impact.NodeTaskReferenceCount != 0 ||
		preview.Impact.EdgeTaskReferenceCount != 0 ||
		preview.Impact.PendingApprovalCount != 0 ||
		!slices.Equal(preview.Impact.RemovedEntities, wantRemoved) {
		t.Fatalf("historical Session Node removal preview = %+v, want no active Task references and exact impact %+v", preview, wantRemoved)
	}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "node_task_references"); got != nil {
		t.Fatalf("historical Session Node blocker entities = %+v, want no node_task_references blocker", got)
	}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "edge_task_references"); got != nil {
		t.Fatalf("historical Session Edge blocker entities = %+v, want no edge_task_references blocker", got)
	}

	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: before.Workflow.Version,
		Graph:           graph,
		Confirmation:    workflowGraphAtomicIntegrationConfirmation(preview.Impact),
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph remove historical Session Node: %v", err)
	}
	if !saved.Saved || !saved.Changed {
		t.Fatalf("historical Session Node save = %+v, want changed save", saved)
	}
	after := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	assertWorkflowGraphAtomicIntegrationResult(t, before, after, graph)

	taskAfter, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after graph save: %v", err)
	}
	if len(taskAfter.Task.CurrentNodes) != 1 || taskAfter.Task.CurrentNodes[0].NodeID != terminalNodeID {
		t.Fatalf("Current Nodes after graph save = %+v, want Terminal Node %q", taskAfter.Task.CurrentNodes, terminalNodeID)
	}
	taskOwner, err := service.store.TaskIDForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("TaskIDForSession after graph save: %v", err)
	}
	if taskOwner == nil || *taskOwner != taskID {
		t.Fatalf("retained Session owner = %v, want Task %q", taskOwner, taskID)
	}
	association, err := service.store.LatestTaskSessionForNode(ctx, agentReference)
	if err != nil {
		t.Fatalf("LatestTaskSessionForNode after graph save: %v", err)
	}
	if association.SessionID != sessionID || !association.CurrentNode.Equal(agentReference) {
		t.Fatalf("historical Session association = %+v, want Session %q and removed Node %q", association, sessionID, agentNodeID)
	}
}

type workflowGraphAtomicCompletionExecution struct {
	*currentNodeCompletionExecutionStub
}

func useWorkflowGraphAtomicCurrentNodeController(t *testing.T, service *Service) {
	t.Helper()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := workflowexecution.NewCurrentNodeController(
		service.store,
		workflowGraphAtomicCurrentNodeRunner{},
		authority,
		service.mutationPermit,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: workflowGraphAtomicAssignmentSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close Current Node controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close Session runtime authority: %v", err)
		}
	})
	service.currentNodeExecution = controller
}

type workflowGraphAtomicCurrentNodeRunner struct{}

func (workflowGraphAtomicCurrentNodeRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	workflowexecution.CurrentNodeAssignmentSteer,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	return nil
}

type workflowGraphAtomicAssignmentSteerer struct{}

func (workflowGraphAtomicAssignmentSteerer) SteerCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return workflowGraphAtomicAssignmentSteer{}, nil
}

type workflowGraphAtomicAssignmentSteer struct{}

func (workflowGraphAtomicAssignmentSteer) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
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
		Source:       source.Reference,
		TransitionID: transitionID,
		OutputValues: outputValues,
		Commentary:   commentary,
	})
}

func newWorkflowGraphAtomicFanOutSourceFixture(t *testing.T) (context.Context, *Service, runtimeids.WorkflowID) {
	t.Helper()
	ctx, service, _ := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Fan-Out Source Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	workflowID := created.Workflow.ID
	current := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	startNodeID := workflowServiceNodeIDByKind(t, current, "start")
	terminalNodeID := workflowServiceNodeIDByKind(t, current, "terminal")
	planNodeID := "node-plan-" + workflowID.String()
	prepNodeID := "node-prep-" + workflowID.String()
	implANodeID := "node-impl-a-" + workflowID.String()
	implBNodeID := "node-impl-b-" + workflowID.String()
	joinNodeID := "node-join-" + workflowID.String()
	graph := workflowGraphDraftFromDefinition(current)
	graph.Nodes = append(graph.Nodes,
		serverapi.WorkflowGraphDraftNode{ID: planNodeID, Key: "plan", Kind: "agent", DisplayName: "Plan", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: prepNodeID, Key: "prep", Kind: "agent", DisplayName: "Prepare", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: implANodeID, Key: "impl_a", Kind: "agent", DisplayName: "Implement A", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: implBNodeID, Key: "impl_b", Kind: "agent", DisplayName: "Implement B", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: joinNodeID, Key: "join", Kind: "join", DisplayName: "Join"},
	)
	startGroupID := "group-start-" + workflowID.String()
	splitGroupID := "group-split-" + workflowID.String()
	alternateBGroupID := "group-alternate-b-" + workflowID.String()
	prepGroupID := "group-prep-" + workflowID.String()
	prepDoneGroupID := "group-prep-done-" + workflowID.String()
	joinAGroupID := "group-join-a-" + workflowID.String()
	joinBGroupID := "group-join-b-" + workflowID.String()
	joinDoneGroupID := "group-join-done-" + workflowID.String()
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroupID, SourceNodeID: startNodeID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: splitGroupID, SourceNodeID: planNodeID, TransitionID: "split", DisplayName: "Split"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: alternateBGroupID, SourceNodeID: planNodeID, TransitionID: "alternate_b", DisplayName: "Alternative B"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: prepGroupID, SourceNodeID: planNodeID, TransitionID: "prepare", DisplayName: "Prepare"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: prepDoneGroupID, SourceNodeID: prepNodeID, TransitionID: "prep_done", DisplayName: "Done"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: joinAGroupID, SourceNodeID: implANodeID, TransitionID: "join_a", DisplayName: "Join"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: joinBGroupID, SourceNodeID: implBNodeID, TransitionID: "join_b", DisplayName: "Join"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: joinDoneGroupID, SourceNodeID: joinNodeID, TransitionID: "join_done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		workflowGraphAtomicIntegrationEdge("edge-start-"+workflowID.String(), startGroupID, "start", planNodeID, "Plan."),
		workflowGraphAtomicIntegrationEdge("edge-split-a-"+workflowID.String(), splitGroupID, "split_a", implANodeID, "Implement A."),
		workflowGraphAtomicIntegrationEdge("edge-split-b-"+workflowID.String(), splitGroupID, "split_b", implBNodeID, "Implement B."),
		workflowGraphAtomicIntegrationEdge("edge-alternate-b-"+workflowID.String(), alternateBGroupID, "alternate_b", implBNodeID, "Implement B."),
		workflowGraphAtomicIntegrationEdge("edge-prep-"+workflowID.String(), prepGroupID, "prepare", prepNodeID, "Prepare."),
		workflowGraphAtomicIntegrationEdge("edge-prep-done-"+workflowID.String(), prepDoneGroupID, "prep_done", terminalNodeID, ""),
		workflowGraphAtomicIntegrationEdge("edge-join-a-"+workflowID.String(), joinAGroupID, "join_a", joinNodeID, ""),
		workflowGraphAtomicIntegrationEdge("edge-join-b-"+workflowID.String(), joinBGroupID, "join_b", joinNodeID, ""),
		workflowGraphAtomicIntegrationEdge("edge-join-done-"+workflowID.String(), joinDoneGroupID, "join_done", terminalNodeID, ""),
	)
	saved := saveWorkflowGraphAtomicIntegrationDraft(t, ctx, service, workflowID, current.Workflow.Version, graph)
	if !saved.Saved || !saved.Changed {
		t.Fatalf("SaveWorkflowGraph create Fan-Out source fixture = %+v, want changed save", saved)
	}
	assertWorkflowGraphAtomicIntegrationExecutionValid(t, ctx, service, workflowID)
	return ctx, service, workflowID
}

func workflowGraphAtomicIntegrationEdge(id string, transitionGroupID string, key string, targetNodeID string, prompt string) serverapi.WorkflowGraphDraftEdge {
	return serverapi.WorkflowGraphDraftEdge{
		ID:                id,
		TransitionGroupID: transitionGroupID,
		Key:               key,
		TargetNodeID:      targetNodeID,
		AssigneeSelection: "configured",
		ThinkingSelection: "configured",
		ContextMode:       "new_session",
		ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
		PromptTemplate:    prompt,
	}
}

func getWorkflowGraphAtomicIntegrationDefinition(t *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID) serverapi.WorkflowDefinition {
	t.Helper()
	response, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	return response.Definition
}

func saveWorkflowGraphAtomicIntegrationDraft(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workflowID runtimeids.WorkflowID,
	expectedVersion int64,
	graph serverapi.WorkflowGraphDraft,
) serverapi.WorkflowGraphSaveResponse {
	t.Helper()
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: expectedVersion,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if !preview.CanSave && !preview.ConfirmationRequired {
		t.Fatalf("PreviewWorkflowGraphSave = %+v, want saveable draft", preview)
	}
	var confirmation *serverapi.WorkflowGraphSaveConfirmation
	if preview.ConfirmationRequired {
		confirmation = workflowGraphAtomicIntegrationConfirmation(preview.Impact)
	}
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: expectedVersion,
		Graph:           graph,
		Confirmation:    confirmation,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	return saved
}

func workflowGraphAtomicIntegrationConfirmation(impact serverapi.WorkflowGraphSaveImpact) *serverapi.WorkflowGraphSaveConfirmation {
	return &serverapi.WorkflowGraphSaveConfirmation{
		ExpectedRemovedNodeGroupCount:       impact.RemovedNodeGroupCount,
		ExpectedRemovedNodeCount:            impact.RemovedNodeCount,
		ExpectedRemovedTransitionGroupCount: impact.RemovedTransitionGroupCount,
		ExpectedRemovedEdgeCount:            impact.RemovedEdgeCount,
		ExpectedNodeTaskReferenceCount:      impact.NodeTaskReferenceCount,
		ExpectedEdgeTaskReferenceCount:      impact.EdgeTaskReferenceCount,
	}
}

func assertWorkflowGraphAtomicIntegrationResult(
	t *testing.T,
	before serverapi.WorkflowDefinition,
	after serverapi.WorkflowDefinition,
	wantGraph serverapi.WorkflowGraphDraft,
) {
	t.Helper()
	if after.Workflow.Version != before.Workflow.Version+1 {
		t.Fatalf("Workflow Version = %d, want exactly one increment from %d", after.Workflow.Version, before.Workflow.Version)
	}
	gotJSON, err := json.Marshal(workflowGraphDraftFromDefinition(after))
	if err != nil {
		t.Fatalf("marshal reloaded authored topology: %v", err)
	}
	wantJSON, err := json.Marshal(wantGraph)
	if err != nil {
		t.Fatalf("marshal expected authored topology: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("reloaded authored topology = %s, want %s", gotJSON, wantJSON)
	}
}

func assertWorkflowGraphAtomicIntegrationExecutionValid(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workflowID runtimeids.WorkflowID,
) {
	t.Helper()
	validated, err := service.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{
		WorkflowID: workflowID,
		Mode:       serverapi.WorkflowValidationModeExecution,
	})
	if err != nil {
		t.Fatalf("ValidateWorkflow execution: %v", err)
	}
	if !validated.Valid {
		t.Fatalf("Workflow execution validation = %+v, want valid", validated)
	}
}

func assertWorkflowGraphAtomicIntegrationDefinitionUnchanged(
	t *testing.T,
	ctx context.Context,
	service *Service,
	before serverapi.WorkflowDefinition,
) {
	t.Helper()
	after := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, before.Workflow.ID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Workflow after blocked save = %+v, want unchanged %+v", after, before)
	}
}

func assertInvalidWorkflowGraphSavePreservesDefinition(
	t *testing.T,
	ctx context.Context,
	service *Service,
	before serverapi.WorkflowDefinition,
) {
	t.Helper()
	invalid := workflowGraphDraftFromDefinition(before)
	if len(invalid.TransitionGroups) == 0 {
		invalid.Nodes = append(invalid.Nodes, invalid.Nodes[0])
	} else {
		invalid.Edges = append(invalid.Edges, serverapi.WorkflowGraphDraftEdge{
			ID:                "edge-invalid-" + before.Workflow.ID.String(),
			TransitionGroupID: invalid.TransitionGroups[0].ID,
			Key:               "invalid",
			TargetNodeID:      "node-missing-" + before.Workflow.ID.String(),
			AssigneeSelection: "configured",
			ThinkingSelection: "configured",
			ContextMode:       "new_session",
			ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
		})
	}
	rejected, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      before.Workflow.ID,
		ExpectedVersion: before.Workflow.Version,
		Graph:           invalid,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph invalid request: %v", err)
	}
	if rejected.Saved || !workflowGraphSaveResponseHasBlocker(rejected, "validation_failed") {
		t.Fatalf("SaveWorkflowGraph invalid request = %+v, want validation rejection", rejected)
	}
	assertWorkflowGraphAtomicIntegrationDefinitionUnchanged(t, ctx, service, before)
}

func assertStaleWorkflowGraphSavePreservesDefinition(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workflowID runtimeids.WorkflowID,
	staleVersion int64,
	staleGraph serverapi.WorkflowGraphDraft,
	before serverapi.WorkflowDefinition,
) {
	t.Helper()
	rejected, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: staleVersion,
		Graph:           staleGraph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph stale request: %v", err)
	}
	if rejected.Saved || !workflowGraphSaveResponseHasBlocker(rejected, "version_changed") {
		t.Fatalf("SaveWorkflowGraph stale request = %+v, want version rejection", rejected)
	}
	after := getWorkflowGraphAtomicIntegrationDefinition(t, ctx, service, workflowID)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("Workflow after stale request = %+v, want unchanged %+v", after, before)
	}
}
