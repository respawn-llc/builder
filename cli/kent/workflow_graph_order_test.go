package main

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	servercore "core/server/core"
	"core/shared/apicontract"
	"core/shared/serverapi"
)

type workflowGraphServiceApplyRemote struct {
	apicontract.WorkflowService
	previewRequest          serverapi.WorkflowGraphSavePreviewRequest
	previewCalls, saveCalls int
}

func (r *workflowGraphServiceApplyRemote) PreviewWorkflowGraphSave(ctx context.Context, request serverapi.WorkflowGraphSavePreviewRequest) (serverapi.WorkflowGraphSavePreviewResponse, error) {
	r.previewCalls++
	r.previewRequest = request
	response, err := r.WorkflowService.PreviewWorkflowGraphSave(ctx, request)
	return response, err
}

func (r *workflowGraphServiceApplyRemote) SaveWorkflowGraph(ctx context.Context, request serverapi.WorkflowGraphSaveRequest) (serverapi.WorkflowGraphSaveResponse, error) {
	r.saveCalls++
	return r.WorkflowService.SaveWorkflowGraph(ctx, request)
}

func (*workflowGraphServiceApplyRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (*workflowGraphServiceApplyRemote) Close() error { return nil }

func TestWorkflowGraphApplyServiceNormalizesCurrentOrderToUnchangedWithoutSave(t *testing.T) {
	ctx := context.Background()
	home, workspace := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() {
		if err := runtimeSupport.Background.Close(); err != nil {
			t.Errorf("close background manager: %v", err)
		}
	})
	appCore, err := servercore.New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() {
		if err := appCore.Close(); err != nil {
			t.Errorf("close server core: %v", err)
		}
	})
	remote := &workflowGraphServiceApplyRemote{WorkflowService: appCore.WorkflowClient()}
	created, err := remote.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "CLI graph order"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	initial, err := remote.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow initial: %v", err)
	}
	graph := workflowGraphDraftFromDefinition(initial.Definition)
	startID, terminalID := initial.Definition.Nodes[0].ID, initial.Definition.Nodes[1].ID
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID: "node-agent", Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder",
	})
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "transition-start", SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: "transition-done", SourceNodeID: "node-agent", TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		serverapi.WorkflowGraphDraftEdge{ID: "edge-start", TransitionGroupID: "transition-start", Key: "start", TargetNodeID: "node-agent", AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", ContextSource: serverapi.WorkflowContextSource{Kind: "immediate_source"}, PromptTemplate: "Work."},
		serverapi.WorkflowGraphDraftEdge{ID: "edge-done", TransitionGroupID: "transition-done", Key: "done", TargetNodeID: terminalID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", ContextSource: serverapi.WorkflowContextSource{Kind: "immediate_source"}},
	)
	saved, err := remote.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: created.Workflow.ID, ExpectedVersion: initial.Definition.Workflow.Version, Graph: graph,
	})
	if err != nil || !saved.Saved || !saved.Changed {
		t.Fatalf("seed graph = %+v, err = %v", saved, err)
	}
	remote.saveCalls = 0
	for _, group := range []serverapi.WorkflowNodeGroupAddRequest{
		{WorkflowID: created.Workflow.ID, GroupID: "group-z", GroupKey: "zeta", DisplayName: "Zeta", SortOrder: 50},
		{WorkflowID: created.Workflow.ID, GroupID: "group-a", GroupKey: "alpha", DisplayName: "Alpha", SortOrder: 150},
	} {
		if _, err := remote.AddWorkflowNodeGroup(ctx, group); err != nil {
			t.Fatalf("AddWorkflowNodeGroup: %v", err)
		}
	}
	currentResponse, err := remote.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	current := currentResponse.Definition
	document, err := workflowGraphDocumentFromDefinition(current)
	if err != nil {
		t.Fatalf("workflowGraphDocumentFromDefinition: %v", err)
	}
	assertWorkflowGraphEntityIDs(t, document.Graph.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID }, []string{"group-a", "group-z"})
	slices.Reverse(document.Graph.Nodes)
	slices.Reverse(document.Graph.TransitionGroups)
	slices.Reverse(document.Graph.Edges)
	input, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	installWorkflowCommandRemote(t, remote)
	exit, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, string(input))
	if exit != 0 || outcome.Outcome != workflowGraphApplyUnchanged || remote.previewCalls != 1 ||
		remote.saveCalls != 0 {
		t.Fatalf("exit=%d outcome=%+v preview=%d save=%d stderr=%q", exit, outcome, remote.previewCalls, remote.saveCalls, stderr)
	}
	assertWorkflowGraphEntityIDs(t, remote.previewRequest.Graph.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID }, []string{"group-z", "group-a"})
	previewGraph, err := json.Marshal(remote.previewRequest.Graph)
	if err != nil {
		t.Fatalf("marshal preview graph: %v", err)
	}
	currentGraph, err := json.Marshal(workflowGraphDraftFromDefinition(current))
	if err != nil {
		t.Fatalf("marshal current graph: %v", err)
	}
	if string(previewGraph) != string(currentGraph) {
		t.Fatalf("preview graph = %s, want current order %s", previewGraph, currentGraph)
	}
	reloaded, err := remote.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("reload Workflow: %v", err)
	}
	if reloaded.Definition.Workflow.Version != current.Workflow.Version {
		t.Fatalf("Workflow Version = %d, want unchanged %d", reloaded.Definition.Workflow.Version, current.Workflow.Version)
	}
}

func assertWorkflowGraphEntityIDs[T any](t *testing.T, entities []T, id func(T) string, want []string) {
	t.Helper()
	got := make([]string, 0, len(entities))
	for _, entity := range entities {
		got = append(got, id(entity))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("entity IDs = %v, want %v", got, want)
	}
}
