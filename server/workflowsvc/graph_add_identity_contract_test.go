package workflowsvc

import (
	"context"
	"slices"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type serviceGraphAddSnapshot struct {
	version          int64
	nodeIDs          []string
	nodeGroupIDs     []string
	transitionGroups []string
	edgeIDs          []string
}

func workflowServiceGraphAddSnapshot(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workflowID runtimeids.WorkflowID,
) serviceGraphAddSnapshot {
	t.Helper()
	response, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	snapshot := serviceGraphAddSnapshot{version: response.Definition.Workflow.Version}
	for _, node := range response.Definition.Nodes {
		snapshot.nodeIDs = append(snapshot.nodeIDs, node.ID)
	}
	for _, group := range response.Definition.NodeGroups {
		snapshot.nodeGroupIDs = append(snapshot.nodeGroupIDs, group.GroupID)
	}
	for _, group := range response.Definition.TransitionGroups {
		snapshot.transitionGroups = append(snapshot.transitionGroups, group.ID)
	}
	for _, edge := range response.Definition.Edges {
		snapshot.edgeIDs = append(snapshot.edgeIDs, edge.ID)
	}
	return snapshot
}

func TestServiceGraphAddContractRequiresAndPreservesCallerOwnedCanonicalIDs(t *testing.T) {
	invalidIDs := []struct {
		name string
		id   string
	}{
		{name: "missing", id: ""},
		{name: "blank", id: " "},
		{name: "prefixed", id: "edge-12345678-1234-4234-9234-123456789abc"},
		{name: "noncanonical", id: "12345678-1234-4234-9234-123456789ABC"},
	}
	operations := []struct {
		name string
		call func(*testing.T, context.Context, *Service, runtimeids.WorkflowID) func(string) (int64, error)
		has  func(serviceGraphAddSnapshot, string) bool
	}{
		{
			name: "AddWorkflowNode",
			call: func(_ *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID) func(string) (int64, error) {
				return func(id string) (int64, error) {
					response, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{
						WorkflowID: workflowID, NodeID: id, Key: "agent", Kind: "agent",
						DisplayName: "Agent", SubagentRole: "coder",
					})
					return response.Version, err
				}
			},
			has: func(snapshot serviceGraphAddSnapshot, id string) bool { return slices.Contains(snapshot.nodeIDs, id) },
		},
		{
			name: "AddWorkflowNodeGroup",
			call: func(_ *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID) func(string) (int64, error) {
				return func(id string) (int64, error) {
					response, err := service.AddWorkflowNodeGroup(ctx, serverapi.WorkflowNodeGroupAddRequest{
						WorkflowID: workflowID, GroupID: id, GroupKey: "parallel", DisplayName: "Parallel",
					})
					return response.Version, err
				}
			},
			has: func(snapshot serviceGraphAddSnapshot, id string) bool {
				return slices.Contains(snapshot.nodeGroupIDs, id)
			},
		},
		{
			name: "AddWorkflowTransitionGroup",
			call: func(t *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID) func(string) (int64, error) {
				current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
				if err != nil {
					t.Fatalf("GetWorkflow: %v", err)
				}
				startID := workflowServiceNodeIDByKind(t, current.Definition, "start")
				return func(id string) (int64, error) {
					response, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{
						WorkflowID: workflowID, GroupID: id, SourceNodeID: startID,
						TransitionID: "start", DisplayName: "Start",
					})
					return response.Version, err
				}
			},
			has: func(snapshot serviceGraphAddSnapshot, id string) bool {
				return slices.Contains(snapshot.transitionGroups, id)
			},
		},
		{
			name: "AddWorkflowEdge",
			call: func(t *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID) func(string) (int64, error) {
				current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
				if err != nil {
					t.Fatalf("GetWorkflow: %v", err)
				}
				startID := workflowServiceNodeIDByKind(t, current.Definition, "start")
				doneID := workflowServiceNodeIDByKind(t, current.Definition, "terminal")
				groupID := runtimeids.NewGraphEntityID()
				if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{
					WorkflowID: workflowID, GroupID: groupID, SourceNodeID: startID,
					TransitionID: "start", DisplayName: "Start",
				}); err != nil {
					t.Fatalf("AddWorkflowTransitionGroup prerequisite: %v", err)
				}
				return func(id string) (int64, error) {
					response, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{
						WorkflowID: workflowID, EdgeID: id, TransitionGroupID: groupID,
						Key: "start", TargetNodeID: doneID, AssigneeSelection: "configured",
						ThinkingSelection: "configured", ContextMode: "new_session",
					})
					return response.Version, err
				}
			},
			has: func(snapshot serviceGraphAddSnapshot, id string) bool { return slices.Contains(snapshot.edgeIDs, id) },
		},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, invalid := range invalidIDs {
				t.Run(invalid.name, func(t *testing.T) {
					ctx, service, _ := newWorkflowServiceTestContext(t)
					created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: operation.name + " invalid ID"})
					if err != nil {
						t.Fatalf("CreateWorkflow: %v", err)
					}
					call := operation.call(t, ctx, service, created.Workflow.ID)
					before := workflowServiceGraphAddSnapshot(t, ctx, service, created.Workflow.ID)

					if _, err := call(invalid.id); err == nil {
						t.Fatalf("%s(%q) succeeded", operation.name, invalid.id)
					}

					after := workflowServiceGraphAddSnapshot(t, ctx, service, created.Workflow.ID)
					if after.version != before.version ||
						!slices.Equal(after.nodeIDs, before.nodeIDs) ||
						!slices.Equal(after.nodeGroupIDs, before.nodeGroupIDs) ||
						!slices.Equal(after.transitionGroups, before.transitionGroups) ||
						!slices.Equal(after.edgeIDs, before.edgeIDs) {
						t.Fatalf("%s(%q) mutated graph: before=%+v after=%+v", operation.name, invalid.id, before, after)
					}
				})
			}

			t.Run("canonical caller ID", func(t *testing.T) {
				ctx, service, _ := newWorkflowServiceTestContext(t)
				created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: operation.name + " canonical ID"})
				if err != nil {
					t.Fatalf("CreateWorkflow: %v", err)
				}
				call := operation.call(t, ctx, service, created.Workflow.ID)
				before := workflowServiceGraphAddSnapshot(t, ctx, service, created.Workflow.ID)
				callerID := runtimeids.NewGraphEntityID()

				version, err := call(callerID)
				if err != nil {
					t.Fatalf("%s canonical ID: %v", operation.name, err)
				}
				after := workflowServiceGraphAddSnapshot(t, ctx, service, created.Workflow.ID)
				if version != before.version+1 || after.version != version {
					t.Fatalf("%s version = returned %d stored %d, want %d", operation.name, version, after.version, before.version+1)
				}
				if !operation.has(after, callerID) {
					t.Fatalf("%s did not return/store caller ID %q unchanged: %+v", operation.name, callerID, after)
				}
			})
		})
	}
}
