package workflowstore

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
)

type graphAddSnapshot struct {
	version          int64
	nodeIDs          []string
	nodeGroupIDs     []string
	transitionGroups []string
	edgeIDs          []string
}

func workflowGraphAddSnapshot(t *testing.T, ctx context.Context, store *Store, workflowID runtimeids.WorkflowID) graphAddSnapshot {
	t.Helper()
	def, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	snapshot := graphAddSnapshot{version: record.Version}
	for _, node := range def.Nodes {
		snapshot.nodeIDs = append(snapshot.nodeIDs, string(workflow.NodeIDOf(node)))
	}
	for _, group := range def.NodeGroups {
		snapshot.nodeGroupIDs = append(snapshot.nodeGroupIDs, group.ID)
	}
	for _, group := range def.TransitionGroups {
		snapshot.transitionGroups = append(snapshot.transitionGroups, string(group.ID))
	}
	for _, edge := range def.Edges {
		snapshot.edgeIDs = append(snapshot.edgeIDs, string(edge.ID))
	}
	return snapshot
}

func TestGraphAddStoreContractRequiresAndPreservesCallerOwnedCanonicalIDs(t *testing.T) {
	invalidIDs := []struct {
		name string
		id   string
	}{
		{name: "missing", id: ""},
		{name: "blank", id: " "},
		{name: "prefixed", id: "node-12345678-1234-4234-9234-123456789abc"},
		{name: "noncanonical", id: "12345678-1234-4234-9234-123456789ABC"},
	}
	operations := []struct {
		name string
		call func(*testing.T, context.Context, *Store, runtimeids.WorkflowID) func(string) (int64, error)
		has  func(graphAddSnapshot, string) bool
	}{
		{
			name: "AddNode",
			call: func(_ *testing.T, ctx context.Context, store *Store, workflowID runtimeids.WorkflowID) func(string) (int64, error) {
				return func(id string) (int64, error) {
					return store.AddNode(ctx, NodeRecord{
						ID: workflow.NodeID(id), WorkflowID: workflowID, Key: "agent",
						Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder",
					})
				}
			},
			has: func(snapshot graphAddSnapshot, id string) bool { return slices.Contains(snapshot.nodeIDs, id) },
		},
		{
			name: "AddNodeGroup",
			call: func(_ *testing.T, ctx context.Context, store *Store, workflowID runtimeids.WorkflowID) func(string) (int64, error) {
				return func(id string) (int64, error) {
					_, version, err := store.AddNodeGroup(ctx, NodeGroupRecord{
						ID: id, WorkflowID: workflowID, Key: "parallel", DisplayName: "Parallel",
					})
					return version, err
				}
			},
			has: func(snapshot graphAddSnapshot, id string) bool { return slices.Contains(snapshot.nodeGroupIDs, id) },
		},
		{
			name: "AddTransitionGroup",
			call: func(t *testing.T, ctx context.Context, store *Store, workflowID runtimeids.WorkflowID) func(string) (int64, error) {
				def, _, err := store.GetDefinition(ctx, workflowID)
				if err != nil {
					t.Fatalf("GetDefinition: %v", err)
				}
				startID := workflow.NodeIDOf(nodeByKind(t, def, workflow.NodeKindStart))
				return func(id string) (int64, error) {
					return store.AddTransitionGroup(ctx, TransitionGroupRecord{
						ID: workflow.TransitionGroupID(id), WorkflowID: workflowID, SourceNodeID: startID,
						TransitionID: "start", DisplayName: "Start",
					})
				}
			},
			has: func(snapshot graphAddSnapshot, id string) bool { return slices.Contains(snapshot.transitionGroups, id) },
		},
		{
			name: "AddEdge",
			call: func(t *testing.T, ctx context.Context, store *Store, workflowID runtimeids.WorkflowID) func(string) (int64, error) {
				def, _, err := store.GetDefinition(ctx, workflowID)
				if err != nil {
					t.Fatalf("GetDefinition: %v", err)
				}
				startID := workflow.NodeIDOf(nodeByKind(t, def, workflow.NodeKindStart))
				doneID := workflow.NodeIDOf(nodeByKind(t, def, workflow.NodeKindTerminal))
				groupID := workflow.TransitionGroupID(runtimeids.NewGraphEntityID())
				if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{
					ID: groupID, WorkflowID: workflowID, SourceNodeID: startID,
					TransitionID: "start", DisplayName: "Start",
				}); err != nil {
					t.Fatalf("AddTransitionGroup prerequisite: %v", err)
				}
				return func(id string) (int64, error) {
					return store.AddEdge(ctx, EdgeRecord{
						ID: workflow.EdgeID(id), WorkflowID: workflowID, TransitionGroupID: groupID,
						Key: "start", TargetNodeID: doneID, AssigneeSelection: workflow.AssigneeSelectionConfigured,
						ThinkingSelection: workflow.ThinkingSelectionConfigured, ContextMode: workflow.ContextModeNewSession,
					})
				}
			},
			has: func(snapshot graphAddSnapshot, id string) bool { return slices.Contains(snapshot.edgeIDs, id) },
		},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			for _, invalid := range invalidIDs {
				t.Run(invalid.name, func(t *testing.T) {
					ctx, store, _ := newTestStoreContext(t)
					created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: operation.name + " invalid ID"})
					if err != nil {
						t.Fatalf("CreateWorkflow: %v", err)
					}
					call := operation.call(t, ctx, store, created.ID)
					before := workflowGraphAddSnapshot(t, ctx, store, created.ID)

					if _, err := call(invalid.id); err == nil {
						t.Fatalf("%s(%q) succeeded", operation.name, invalid.id)
					}

					after := workflowGraphAddSnapshot(t, ctx, store, created.ID)
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
				ctx, store, _ := newTestStoreContext(t)
				created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: operation.name + " canonical ID"})
				if err != nil {
					t.Fatalf("CreateWorkflow: %v", err)
				}
				call := operation.call(t, ctx, store, created.ID)
				before := workflowGraphAddSnapshot(t, ctx, store, created.ID)
				callerID := runtimeids.NewGraphEntityID()

				version, err := call(callerID)
				if err != nil {
					t.Fatalf("%s canonical ID: %v", operation.name, err)
				}
				after := workflowGraphAddSnapshot(t, ctx, store, created.ID)
				if version != before.version+1 || after.version != version {
					t.Fatalf("%s version = returned %d stored %d, want %d", operation.name, version, after.version, before.version+1)
				}
				if !operation.has(after, callerID) {
					t.Fatalf("%s did not store caller ID %q unchanged: %+v", operation.name, callerID, after)
				}
			})
		})
	}
}

func TestWorkflowCreationGeneratesDistinctCanonicalStartAndDoneIDs(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Canonical internal nodes"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	startID := workflow.NodeIDOf(nodeByKind(t, def, workflow.NodeKindStart))
	doneID := workflow.NodeIDOf(nodeByKind(t, def, workflow.NodeKindTerminal))
	if startID == doneID {
		t.Fatalf("Start and Done IDs are both %q", startID)
	}
	for kind, id := range map[string]workflow.NodeID{"Start": startID, "Done": doneID} {
		if _, err := runtimeids.GraphEntityIDBlob(string(id)); err != nil {
			t.Fatalf("%s ID %q is not canonical UUIDv4: %v", kind, id, err)
		}
	}
}

func TestGraphAddStoreSourceHasNoIDFallbackGenerationOrAssignment(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	storeFile := filepath.Join(filepath.Dir(currentFile), "store.go")
	file, err := parser.ParseFile(token.NewFileSet(), storeFile, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", storeFile, err)
	}
	targets := map[string]bool{
		"AddNode": false, "AddNodeGroup": false, "AddTransitionGroup": false, "AddEdge": false,
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv == nil {
			continue
		}
		if _, targeted := targets[function.Name.Name]; !targeted {
			continue
		}
		targets[function.Name.Name] = true
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.AssignStmt:
				for _, expression := range typed.Lhs {
					selector, ok := expression.(*ast.SelectorExpr)
					if ok && selector.Sel.Name == "ID" {
						t.Errorf("%s assigns an ID inside the Add method", function.Name.Name)
					}
				}
			case *ast.CallExpr:
				switch called := typed.Fun.(type) {
				case *ast.Ident:
					if called.Name == "prefixedID" {
						t.Errorf("%s calls fallback generator %s", function.Name.Name, called.Name)
					}
				case *ast.SelectorExpr:
					if called.Sel.Name == "NewGraphEntityID" {
						t.Errorf("%s calls fallback generator %s", function.Name.Name, called.Sel.Name)
					}
				}
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("store source audit did not find %s", name)
		}
	}
}
