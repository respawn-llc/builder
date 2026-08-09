package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"core/internal/testharness/testsetup"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/server/workflowsvc"
	"core/server/workflowview"
	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const (
	workflowGraphAddedAlphaNodeID  = "11111111-1111-4111-8111-111111111111"
	workflowGraphAddedZetaNodeID   = "22222222-2222-4222-8222-222222222222"
	workflowGraphAddedAlphaGroupID = "33333333-3333-4333-8333-333333333333"
	workflowGraphAddedZetaGroupID  = "44444444-4444-4444-8444-444444444444"
	workflowGraphAddedAlphaEdgeID  = "55555555-5555-4555-8555-555555555555"
	workflowGraphAddedZetaEdgeID   = "66666666-6666-4666-8666-666666666666"
)

type workflowGraphServiceRemote struct {
	apicontract.WorkflowService
	previewRequests []serverapi.WorkflowGraphSavePreviewRequest
	saveRequests    []serverapi.WorkflowGraphSaveRequest
}

func (r *workflowGraphServiceRemote) PreviewWorkflowGraphSave(ctx context.Context, req serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	r.previewRequests = append(r.previewRequests, req)
	return r.WorkflowService.PreviewWorkflowGraphSave(ctx, req)
}

func (r *workflowGraphServiceRemote) SaveWorkflowGraph(ctx context.Context, req serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	r.saveRequests = append(r.saveRequests, req)
	return r.WorkflowService.SaveWorkflowGraph(ctx, req)
}

func (*workflowGraphServiceRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, errors.New("project path resolution is not available")
}

func (*workflowGraphServiceRemote) Close() error { return nil }

func TestWorkflowGraphCanonicalProjectionUsesSemanticDocumentOrder(t *testing.T) {
	ctx, remote, workflowID := newWorkflowGraphServiceFixture(t)
	current := getWorkflowGraphDefinition(t, ctx, remote, workflowID)

	graph, err := canonicalWorkflowGraphDraftFromDefinition(current)
	if err != nil {
		t.Fatalf("canonicalWorkflowGraphDraftFromDefinition: %v", err)
	}

	requireWorkflowGraphIDs(t, graph.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID }, []string{"group-parallel-alpha", "group-parallel-zeta"})
	requireWorkflowGraphIDs(t, graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) string { return node.ID }, []string{
		currentNodeIDByKind(t, current, "start"),
		"node-branch-a", "node-branch-b", "node-branch-c", "node-branch-d",
		currentNodeIDByKind(t, current, "terminal"),
		"node-join-alpha", "node-join-zeta", "node-plan-alpha", "node-plan-zeta",
	})
	requireWorkflowGraphTransitionGroupsCanonical(t, graph.TransitionGroups)
	requireWorkflowGraphIDs(t, graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) string { return edge.ID }, []string{
		"edge-branch-a", "edge-branch-b", "edge-branch-c", "edge-branch-d",
		"edge-join-alpha", "edge-join-zeta", "edge-plan-alpha-a", "edge-plan-alpha-b",
		"edge-plan-zeta-a", "edge-plan-zeta-b", "edge-start",
	})
}

func TestWorkflowGraphPreviewNormalizesPermutedDocumentToUnchangedWithoutSave(t *testing.T) {
	ctx, remote, workflowID := newWorkflowGraphServiceFixture(t)
	current := getWorkflowGraphDefinition(t, ctx, remote, workflowID)
	submitted := workflowGraphDraftFromDefinition(current)
	reverseWorkflowGraphDraft(&submitted)

	preview, err := previewWorkflowGraphDraft(ctx, remote, current, submitted)
	if err != nil {
		t.Fatalf("previewWorkflowGraphDraft: %v", err)
	}

	if preview.Response.Changed {
		t.Fatalf("preview Changed = true, want unchanged")
	}
	if len(remote.previewRequests) != 1 {
		t.Fatalf("preview calls = %d, want 1", len(remote.previewRequests))
	}
	if len(remote.saveRequests) != 0 {
		t.Fatalf("save calls = %d, want no CLI save for unchanged preview", len(remote.saveRequests))
	}
	requireWorkflowGraphDraftMatchesDefinitionOrder(t, preview.Graph, current)
	requireWorkflowGraphDraftMatchesDefinitionOrder(t, remote.previewRequests[0].Graph, current)
}

func TestWorkflowGraphChangedPermutedDocumentPreservesIdentityBoundOrder(t *testing.T) {
	ctx, remote, workflowID := newWorkflowGraphServiceFixture(t)
	current := getWorkflowGraphDefinition(t, ctx, remote, workflowID)
	submitted := workflowGraphDraftFromDefinition(current)
	reverseWorkflowGraphDraft(&submitted)

	submitted.NodeGroups = deleteWorkflowGraphEntity(submitted.NodeGroups, "group-parallel-alpha", func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID })
	for index := range submitted.Nodes {
		switch submitted.Nodes[index].ID {
		case "node-branch-c", "node-branch-d", "node-join-alpha":
			submitted.Nodes[index].GroupID = ""
			submitted.Nodes[index].GroupKey = ""
		case "node-plan-zeta":
			submitted.Nodes[index].DisplayName = "Edited Plan"
		}
	}
	submitted.Nodes = append(submitted.Nodes,
		serverapi.WorkflowGraphDraftNode{ID: workflowGraphAddedZetaNodeID, Key: "z_added", Kind: "agent", DisplayName: "Added Zeta", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: workflowGraphAddedAlphaNodeID, Key: "a_added", Kind: "agent", DisplayName: "Added Alpha", SubagentRole: "coder"},
	)
	for index := range submitted.Edges {
		if submitted.Edges[index].ID == "edge-join-alpha" {
			submitted.Edges[index].TargetNodeID = workflowGraphAddedAlphaNodeID
		}
	}
	submitted.TransitionGroups = append(submitted.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: workflowGraphAddedZetaGroupID, SourceNodeID: workflowGraphAddedZetaNodeID, TransitionID: "added_done", DisplayName: "Done"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: workflowGraphAddedAlphaGroupID, SourceNodeID: workflowGraphAddedAlphaNodeID, TransitionID: "added_next", DisplayName: "Next"},
	)
	submitted.Edges = append(submitted.Edges,
		serverapi.WorkflowGraphDraftEdge{ID: workflowGraphAddedZetaEdgeID, TransitionGroupID: workflowGraphAddedZetaGroupID, Key: "done", TargetNodeID: currentNodeIDByKind(t, current, "terminal"), AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
		serverapi.WorkflowGraphDraftEdge{ID: workflowGraphAddedAlphaEdgeID, TransitionGroupID: workflowGraphAddedAlphaGroupID, Key: "next", TargetNodeID: workflowGraphAddedZetaNodeID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
	)

	preview, err := previewWorkflowGraphDraft(ctx, remote, current, submitted)
	if err != nil {
		t.Fatalf("previewWorkflowGraphDraft: %v", err)
	}
	if !preview.Response.Changed || !preview.Response.CanSave || len(preview.Response.Blockers) != 0 {
		t.Fatalf("preview = %+v, want changed savable graph", preview.Response)
	}

	saved, err := remote.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           preview.Graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || !saved.Changed {
		t.Fatalf("save = %+v, want changed save", saved)
	}
	reloaded := getWorkflowGraphDefinition(t, ctx, remote, workflowID)

	requireWorkflowGraphIDs(t, reloaded.NodeGroups, func(group serverapi.WorkflowNodeGroup) string { return group.GroupID }, []string{"group-parallel-zeta"})
	requireWorkflowGraphIDs(t, reloaded.Nodes, func(node serverapi.WorkflowNode) string { return node.ID }, append(workflowDefinitionNodeIDs(current), workflowGraphAddedAlphaNodeID, workflowGraphAddedZetaNodeID))
	requireWorkflowGraphIDs(t, reloaded.TransitionGroups, func(group serverapi.WorkflowTransitionGroup) string { return group.ID }, append(workflowDefinitionTransitionGroupIDs(current), workflowGraphAddedAlphaGroupID, workflowGraphAddedZetaGroupID))
	requireWorkflowGraphIDs(t, reloaded.Edges, func(edge serverapi.WorkflowEdge) string { return edge.ID }, append(workflowDefinitionEdgeIDs(current), workflowGraphAddedAlphaEdgeID, workflowGraphAddedZetaEdgeID))
	if got := workflowDefinitionNodeByID(t, reloaded, "node-plan-zeta").DisplayName; got != "Edited Plan" {
		t.Fatalf("edited Node display name = %q, want %q", got, "Edited Plan")
	}
	if reloaded.Workflow.Version != current.Workflow.Version+1 {
		t.Fatalf("reloaded version = %d, want %d", reloaded.Workflow.Version, current.Workflow.Version+1)
	}
}

func newWorkflowGraphServiceFixture(t *testing.T) (context.Context, *workflowGraphServiceRemote, runtimeids.WorkflowID) {
	t.Helper()
	ctx := context.Background()
	resolver := testsetup.QuestionsEnabled("coder")
	metadataStore := testsetup.OpenStore(t, t.TempDir())
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(resolver))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	definitions, err := workflowview.NewDefinitionProjection(store)
	if err != nil {
		t.Fatalf("workflowview.NewDefinitionProjection: %v", err)
	}
	service, err := workflowsvc.New(store, workflowsvc.ReadModels{
		Definitions:      definitions,
		Board:            workflowGraphUnusedBoardReadModel{},
		TaskList:         workflowGraphUnusedTaskListReadModel{},
		TaskSearch:       workflowGraphUnusedTaskSearchReadModel{},
		TaskDetail:       workflowGraphUnusedTaskDetailReadModel{},
		TaskDependencies: workflowGraphUnusedTaskDependencyReadModel{},
		Activity:         workflowGraphUnusedActivityReadModel{},
		Attention:        workflowGraphUnusedAttentionReadModel{},
		Approvals:        workflowGraphUnusedApprovalReadModel{},
	}, resolver, workflowexecution.NewMutationPermit(), workflowsvc.WithCurrentNodeExecution(workflowGraphUnavailableExecution{}))
	if err != nil {
		t.Fatalf("workflowsvc.New: %v", err)
	}
	remote := &workflowGraphServiceRemote{WorkflowService: service}
	created, err := remote.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "CLI graph order"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	seedWorkflowGraphOrderFixture(t, ctx, remote, created.Workflow.ID)
	remote.previewRequests = nil
	remote.saveRequests = nil
	return ctx, remote, created.Workflow.ID
}

func seedWorkflowGraphOrderFixture(t *testing.T, ctx context.Context, remote apicontract.WorkflowService, workflowID runtimeids.WorkflowID) {
	t.Helper()
	current := getWorkflowGraphDefinition(t, ctx, remote, workflowID)
	startID := currentNodeIDByKind(t, current, "start")
	doneID := currentNodeIDByKind(t, current, "terminal")
	graph := workflowGraphDraftFromDefinition(current)
	graph.NodeGroups = append(graph.NodeGroups,
		serverapi.WorkflowGraphDraftNodeGroup{ID: "group-parallel-zeta", Key: "parallel_zeta", DisplayName: "Parallel Zeta"},
		serverapi.WorkflowGraphDraftNodeGroup{ID: "group-parallel-alpha", Key: "parallel_alpha", DisplayName: "Parallel Alpha"},
	)
	graph.Nodes = append(graph.Nodes,
		serverapi.WorkflowGraphDraftNode{ID: "node-plan-zeta", Key: "plan_zeta", Kind: "agent", DisplayName: "Plan Zeta", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-branch-a", Key: "branch_a", Kind: "agent", DisplayName: "Branch A", GroupID: "group-parallel-zeta", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-branch-b", Key: "branch_b", Kind: "agent", DisplayName: "Branch B", GroupID: "group-parallel-zeta", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-join-zeta", Key: "join_zeta", Kind: "join", DisplayName: "Join Zeta", GroupID: "group-parallel-zeta", JoinInputProviders: []serverapi.WorkflowJoinInputProvider{{InputName: "joined_zeta", ProviderEdgeID: "edge-branch-a"}}},
		serverapi.WorkflowGraphDraftNode{ID: "node-plan-alpha", Key: "plan_alpha", Kind: "agent", DisplayName: "Plan Alpha", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-branch-c", Key: "branch_c", Kind: "agent", DisplayName: "Branch C", GroupID: "group-parallel-alpha", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-branch-d", Key: "branch_d", Kind: "agent", DisplayName: "Branch D", GroupID: "group-parallel-alpha", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: "node-join-alpha", Key: "join_alpha", Kind: "join", DisplayName: "Join Alpha", GroupID: "group-parallel-alpha", JoinInputProviders: []serverapi.WorkflowJoinInputProvider{{InputName: "joined_alpha", ProviderEdgeID: "edge-branch-c"}}},
	)
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-start", SourceNodeID: startID, TransitionID: "start_flow", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-plan-zeta", SourceNodeID: "node-plan-zeta", TransitionID: "split_zeta", DisplayName: "Split"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-branch-a", SourceNodeID: "node-branch-a", TransitionID: "join_a", DisplayName: "Join"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-branch-b", SourceNodeID: "node-branch-b", TransitionID: "join_b", DisplayName: "Join"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-join-zeta", SourceNodeID: "node-join-zeta", TransitionID: "next_alpha", DisplayName: "Next"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-plan-alpha", SourceNodeID: "node-plan-alpha", TransitionID: "split_alpha", DisplayName: "Split"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-branch-c", SourceNodeID: "node-branch-c", TransitionID: "join_c", DisplayName: "Join"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-branch-d", SourceNodeID: "node-branch-d", TransitionID: "join_d", DisplayName: "Join"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "group-join-alpha", SourceNodeID: "node-join-alpha", TransitionID: "done_flow", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		workflowGraphOrderEdge("edge-start", "group-start", "start", "node-plan-zeta"),
		workflowGraphOrderEdge("edge-plan-zeta-a", "group-plan-zeta", "branch_a", "node-branch-a"),
		workflowGraphOrderEdge("edge-plan-zeta-b", "group-plan-zeta", "branch_b", "node-branch-b"),
		workflowGraphOrderEdgeWithParameter("edge-branch-a", "group-branch-a", "join_a", "node-join-zeta", "joined_zeta"),
		workflowGraphOrderEdge("edge-branch-b", "group-branch-b", "join_b", "node-join-zeta"),
		workflowGraphOrderEdge("edge-join-zeta", "group-join-zeta", "next", "node-plan-alpha"),
		workflowGraphOrderEdge("edge-plan-alpha-a", "group-plan-alpha", "branch_c", "node-branch-c"),
		workflowGraphOrderEdge("edge-plan-alpha-b", "group-plan-alpha", "branch_d", "node-branch-d"),
		workflowGraphOrderEdgeWithParameter("edge-branch-c", "group-branch-c", "join_c", "node-join-alpha", "joined_alpha"),
		workflowGraphOrderEdge("edge-branch-d", "group-branch-d", "join_d", "node-join-alpha"),
		workflowGraphOrderEdge("edge-join-alpha", "group-join-alpha", "done", doneID),
	)
	saved, err := remote.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("seed SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || !saved.Changed {
		t.Fatalf("seed SaveWorkflowGraph = %+v, want changed save", saved)
	}
}

func workflowGraphOrderEdge(id, groupID, key, targetID string) serverapi.WorkflowGraphDraftEdge {
	return serverapi.WorkflowGraphDraftEdge{
		ID: id, TransitionGroupID: groupID, Key: key, TargetNodeID: targetID,
		AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session",
	}
}

func workflowGraphOrderEdgeWithParameter(id, groupID, key, targetID, parameter string) serverapi.WorkflowGraphDraftEdge {
	edge := workflowGraphOrderEdge(id, groupID, key, targetID)
	edge.Parameters = []serverapi.WorkflowParameter{{Key: parameter, Description: "Joined input", Purpose: "ordinary"}}
	return edge
}

func getWorkflowGraphDefinition(t *testing.T, ctx context.Context, remote apicontract.WorkflowService, workflowID runtimeids.WorkflowID) serverapi.WorkflowDefinition {
	t.Helper()
	response, err := remote.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	return response.Definition
}

func currentNodeIDByKind(t *testing.T, definition serverapi.WorkflowDefinition, kind string) string {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind == kind {
			return node.ID
		}
	}
	t.Fatalf("missing %s Node", kind)
	return ""
}

func workflowDefinitionNodeByID(t *testing.T, definition serverapi.WorkflowDefinition, id string) serverapi.WorkflowNode {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("missing Node %q", id)
	return serverapi.WorkflowNode{}
}

func reverseWorkflowGraphDraft(graph *serverapi.WorkflowGraphDraft) {
	slices.Reverse(graph.NodeGroups)
	slices.Reverse(graph.Nodes)
	slices.Reverse(graph.TransitionGroups)
	slices.Reverse(graph.Edges)
}

func deleteWorkflowGraphEntity[T any](entities []T, id string, entityID func(T) string) []T {
	filtered := make([]T, 0, len(entities))
	for _, entity := range entities {
		if entityID(entity) != id {
			filtered = append(filtered, entity)
		}
	}
	return filtered
}

func requireWorkflowGraphDraftMatchesDefinitionOrder(t *testing.T, graph serverapi.WorkflowGraphDraft, definition serverapi.WorkflowDefinition) {
	t.Helper()
	requireWorkflowGraphIDs(t, graph.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID }, workflowDefinitionNodeGroupIDs(definition))
	requireWorkflowGraphIDs(t, graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) string { return node.ID }, workflowDefinitionNodeIDs(definition))
	requireWorkflowGraphIDs(t, graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) string { return group.ID }, workflowDefinitionTransitionGroupIDs(definition))
	requireWorkflowGraphIDs(t, graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) string { return edge.ID }, workflowDefinitionEdgeIDs(definition))
}

func requireWorkflowGraphIDs[T any](t *testing.T, entities []T, entityID func(T) string, expected []string) {
	t.Helper()
	actual := make([]string, 0, len(entities))
	for _, entity := range entities {
		actual = append(actual, entityID(entity))
	}
	if !slices.Equal(actual, expected) {
		t.Fatalf("entity IDs = %v, want %v", actual, expected)
	}
}

func requireWorkflowGraphTransitionGroupsCanonical(t *testing.T, groups []serverapi.WorkflowGraphDraftTransitionGroup) {
	t.Helper()
	for index := 1; index < len(groups); index++ {
		previous := groups[index-1]
		current := groups[index]
		if previous.SourceNodeID > current.SourceNodeID ||
			previous.SourceNodeID == current.SourceNodeID && previous.TransitionID > current.TransitionID ||
			previous.SourceNodeID == current.SourceNodeID && previous.TransitionID == current.TransitionID && previous.ID > current.ID {
			t.Fatalf("Transition Groups are not in canonical order at %q then %q", previous.ID, current.ID)
		}
	}
}

func workflowDefinitionNodeGroupIDs(definition serverapi.WorkflowDefinition) []string {
	ids := make([]string, 0, len(definition.NodeGroups))
	for _, group := range definition.NodeGroups {
		ids = append(ids, group.GroupID)
	}
	return ids
}

func workflowDefinitionNodeIDs(definition serverapi.WorkflowDefinition) []string {
	ids := make([]string, 0, len(definition.Nodes))
	for _, node := range definition.Nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func workflowDefinitionTransitionGroupIDs(definition serverapi.WorkflowDefinition) []string {
	ids := make([]string, 0, len(definition.TransitionGroups))
	for _, group := range definition.TransitionGroups {
		ids = append(ids, group.ID)
	}
	return ids
}

func workflowDefinitionEdgeIDs(definition serverapi.WorkflowDefinition) []string {
	ids := make([]string, 0, len(definition.Edges))
	for _, edge := range definition.Edges {
		ids = append(ids, edge.ID)
	}
	return ids
}

type workflowGraphUnusedBoardReadModel struct{}

func (workflowGraphUnusedBoardReadModel) Get(context.Context, serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoard, error) {
	return serverapi.WorkflowBoard{}, errors.New("unused Workflow Board read")
}
func (workflowGraphUnusedBoardReadModel) ListNodeCards(context.Context, serverapi.WorkflowBoardNodeCardsListRequest) (serverapi.WorkflowBoardNodeCardsListResponse, error) {
	return serverapi.WorkflowBoardNodeCardsListResponse{}, errors.New("unused Workflow Board Node Cards read")
}

type workflowGraphUnusedTaskListReadModel struct{}

func (workflowGraphUnusedTaskListReadModel) List(context.Context, serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	return serverapi.WorkflowTaskListResponse{}, errors.New("unused Workflow Task List read")
}

type workflowGraphUnusedTaskSearchReadModel struct{}

func (workflowGraphUnusedTaskSearchReadModel) Search(context.Context, serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error) {
	return serverapi.TaskSearchResponse{}, errors.New("unused Workflow Task Search read")
}

type workflowGraphUnusedTaskDetailReadModel struct{}

func (workflowGraphUnusedTaskDetailReadModel) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("unused Workflow Task Detail read")
}
func (workflowGraphUnusedTaskDetailReadModel) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("unused Workflow Task Detail read")
}
func (workflowGraphUnusedTaskDetailReadModel) GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("unused Workflow Task Detail read")
}
func (workflowGraphUnusedTaskDetailReadModel) ListCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error) {
	return nil, errors.New("unused Workflow Current Nodes read")
}

type workflowGraphUnusedTaskDependencyReadModel struct{}

func (workflowGraphUnusedTaskDependencyReadModel) GetTaskDependencies(context.Context, string) (serverapi.WorkflowTaskDependencies, error) {
	return serverapi.WorkflowTaskDependencies{}, errors.New("unused Workflow Task Dependencies read")
}
func (workflowGraphUnusedTaskDependencyReadModel) CountUnsatisfiedBlockers(context.Context, string) (int, error) {
	return 0, errors.New("unused Workflow Task Dependency count")
}
func (workflowGraphUnusedTaskDependencyReadModel) ListTaskDependencies(context.Context, string, *serverapi.WorkflowTaskDependencyDirection) (serverapi.WorkflowTaskDependencyListResponse, error) {
	return serverapi.WorkflowTaskDependencyListResponse{}, errors.New("unused Workflow Task Dependencies read")
}

type workflowGraphUnusedActivityReadModel struct{}

func (workflowGraphUnusedActivityReadModel) List(context.Context, serverapi.WorkflowTaskActivityListRequest) (serverapi.WorkflowTaskActivityListResponse, error) {
	return serverapi.WorkflowTaskActivityListResponse{}, errors.New("unused Workflow Activity read")
}

type workflowGraphUnusedAttentionReadModel struct{}

func (workflowGraphUnusedAttentionReadModel) List(context.Context, serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	return serverapi.WorkflowAttentionListResponse{}, errors.New("unused Workflow Attention read")
}
func (workflowGraphUnusedAttentionReadModel) ListTask(context.Context, serverapi.WorkflowTaskAttentionListRequest) (serverapi.WorkflowTaskAttentionListResponse, error) {
	return serverapi.WorkflowTaskAttentionListResponse{}, errors.New("unused Workflow Task Attention read")
}

type workflowGraphUnusedApprovalReadModel struct{}

func (workflowGraphUnusedApprovalReadModel) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return serverapi.ApprovalListPendingBySessionResponse{}, errors.New("unused Workflow Approval read")
}

type workflowGraphUnavailableExecution struct{}

func (workflowGraphUnavailableExecution) StartTask(context.Context, workflow.TaskID, workflowexecution.TaskStartPreparation) (workflowstore.StartTaskResult, error) {
	return workflowstore.StartTaskResult{}, errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) ResumeTask(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error) {
	return nil, errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) ResumeTaskWithPreparation(context.Context, workflow.TaskID, workflowexecution.TaskStartPreparation) ([]workflow.CurrentNode, error) {
	return nil, errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error) {
	return workflowstore.PendingApprovalApplyResult{}, errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error) {
	return workflowstore.ManualMoveResult{}, errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) ManualMoveDisposition(workflow.TaskID) (workflowexecution.ManualMoveDisposition, error) {
	return workflowexecution.ManualMoveDispositionQuiescent, nil
}
func (workflowGraphUnavailableExecution) InterruptForManualMove(context.Context, workflow.TaskID, func() error) error {
	return errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) Interrupt(context.Context, workflowexecution.InterruptSelector) error {
	return errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) EnsureTaskQuiescent(workflow.TaskID) error {
	return errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) CompleteSessionCurrentNode(context.Context, runtimeids.SessionID, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error) {
	return workflowstore.CurrentNodeCompletionResult{}, errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) CompleteIdleCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector, string, map[string]string, string) (workflowstore.CurrentNodeCompletionResult, error) {
	return workflowstore.CurrentNodeCompletionResult{}, errors.New("Workflow execution is unavailable")
}
func (workflowGraphUnavailableExecution) AcceptWorkflowQuestion(context.Context, workflow.TaskID, string, askquestion.AskQuestionResolution, error) (workflowexecution.WorkflowQuestionAcceptance, error) {
	return nil, errors.New("Workflow execution is unavailable")
}
