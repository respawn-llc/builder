package runtime

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"core/server/session"
	"core/server/tools"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

func TestWorkflowTerminalStateHasOneProductionWriter(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read runtime package: %v", err)
	}
	var writers []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "recordWorkflowTerminalState" {
				writers = append(writers, name)
			}
			return true
		})
	}
	if len(writers) != 1 || writers[0] != "workflow_completion.go" {
		t.Fatalf("Workflow terminal writers = %v, want only exact Agent Step application", writers)
	}
}

func TestDetachedPublicationRejectsCurrentNodeExecutionWithoutScope(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSessionAt(t, t.TempDir()), &fakeClient{}, tools.NewRegistry(), Config{})
	_, err := engine.PrepareCurrentNodeExecutionPublication(&workflowruntime.CurrentNodeExecutionConfig{
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: mustTestCurrentNodeReference(t, "task-zero-scope", "node-zero-scope", nil),
		},
	})
	if err == nil {
		t.Fatal("detached publication accepted a Current Node execution without an exact scope")
	}
}

func TestCurrentNodeExecutionBindingHasOneOwner(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSessionAt(t, t.TempDir())
	workflowID := runtimeids.NewWorkflowID()
	execution := &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: runtimeids.NewExecutionScopeID(),
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: mustTestCurrentNodeReference(t, "task-binding-owner", "node-binding-owner", nil),
			WorkflowID:  workflowID,
		},
	}
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	first, err := engine.PrepareCurrentNodeExecutionPublication(execution)
	if err != nil {
		t.Fatalf("prepare Current Node execution: %v", err)
	}
	if err := first.Begin(); err != nil {
		t.Fatalf("begin Current Node execution publication: %v", err)
	}
	binding := first.Commit()
	duplicate, err := engine.PrepareCurrentNodeExecutionPublication(execution)
	if err != nil {
		t.Fatalf("prepare duplicate Current Node execution: %v", err)
	}
	if err := duplicate.Begin(); err == nil {
		duplicate.Cancel()
		t.Fatal("runtime granted a second Current Node execution publication")
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("close Current Node execution binding: %v", err)
	}
	retained, err := engine.WorkflowSessionState()
	if err != nil {
		t.Fatalf("retained Workflow Session state: %v", err)
	}
	if retained == nil || retained.TaskID != execution.Instructions.CurrentNode.TaskID || retained.WorkflowID != workflowID {
		t.Fatalf("retained Workflow Session state = %+v, want completion eligibility for prior assignment", retained)
	}
	successor := &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: runtimeids.NewExecutionScopeID(),
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: mustTestCurrentNodeReference(t, "task-binding-owner", "node-binding-successor", nil),
			WorkflowID:  workflowID,
		},
	}
	rebound, err := engine.PrepareCurrentNodeExecutionPublication(successor)
	if err != nil {
		t.Fatalf("prepare successor Current Node execution after owner close: %v", err)
	}
	if err := rebound.Begin(); err != nil {
		t.Fatalf("begin successor Current Node execution: %v", err)
	}
	successorBinding := rebound.Commit()
	if err := successorBinding.Close(); err != nil {
		t.Fatalf("close rebound Current Node execution: %v", err)
	}
	if !engine.currentNodeExecutionActive() {
		t.Fatal("successor completion contract was discarded when exact scope ownership ended")
	}
}

func TestCurrentNodeExecutionBindingClearsCompletedContract(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSessionAt(t, t.TempDir())
	execution := &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: runtimeids.NewExecutionScopeID(),
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: mustTestCurrentNodeReference(t, "task-completed-binding", "node-completed-binding", nil),
			WorkflowID:  runtimeids.NewWorkflowID(),
		},
	}
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	publication, err := engine.PrepareCurrentNodeExecutionPublication(execution)
	if err != nil {
		t.Fatalf("prepare Current Node execution: %v", err)
	}
	if err := publication.Begin(); err != nil {
		t.Fatalf("begin Current Node execution: %v", err)
	}
	binding := publication.Commit()
	if !engine.currentNodeExecutionActive() {
		t.Fatal("bound Current Node execution has no completion contract")
	}
	beforeClose, err := engine.WorkflowSessionState()
	if err != nil {
		t.Fatalf("WorkflowSessionState before completed binding close: %v", err)
	}
	if beforeClose == nil ||
		beforeClose.TaskID != execution.Instructions.CurrentNode.TaskID ||
		beforeClose.WorkflowID != execution.Instructions.WorkflowID {
		t.Fatalf("WorkflowSessionState before completed binding close = %+v, want configured workflow identity", beforeClose)
	}
	if _, err := engine.recordWorkflowTerminalState(
		WorkflowCompletionSourceTool,
		workflowruntime.AcceptedCompletion{},
	); err != nil {
		t.Fatalf("record Workflow terminal state: %v", err)
	}

	if err := binding.Close(); err != nil {
		t.Fatalf("close completed Current Node execution binding: %v", err)
	}
	if engine.currentNodeExecutionActive() {
		t.Fatal("completed Current Node execution retained its completion contract")
	}
	if state, err := engine.WorkflowSessionState(); err != nil || state != nil {
		t.Fatalf("completed Workflow Session state = %+v error=%v, want absent", state, err)
	}
}

func TestWorkflowCompactionBoundaryResetsPersistedAndActiveLockedContract(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSessionAt(t, t.TempDir())
	locked := session.LockedContract{Model: "locked-model"}
	if err := store.MarkModelDispatchLocked(locked); err != nil {
		t.Fatalf("lock Session contract: %v", err)
	}
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	engine.lockedContractState().Set(locked)

	if err := engine.ResetLockedContractForWorkflowCompactionBoundary(); err != nil {
		t.Fatalf("reset Workflow compaction boundary: %v", err)
	}
	if store.Meta().Locked != nil {
		t.Fatalf("persisted locked contract = %+v, want absent", store.Meta().Locked)
	}
	if active, ok := engine.lockedContractState().Snapshot(); ok {
		t.Fatalf("active locked contract = %+v, want absent", active)
	}
}

func TestWorkflowCompactionBoundaryRejectsActiveCurrentNodeExecution(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSessionAt(t, t.TempDir())
	locked := session.LockedContract{Model: "locked-model"}
	if err := store.MarkModelDispatchLocked(locked); err != nil {
		t.Fatalf("lock Session contract: %v", err)
	}
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	engine.lockedContractState().Set(locked)
	publication, err := engine.PrepareCurrentNodeExecutionPublication(&workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: runtimeids.NewExecutionScopeID(),
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: mustTestCurrentNodeReference(t, "task-active-boundary", "node-active-boundary", nil),
			WorkflowID:  runtimeids.NewWorkflowID(),
		},
	})
	if err != nil {
		t.Fatalf("prepare Current Node execution: %v", err)
	}
	if err := publication.Begin(); err != nil {
		t.Fatalf("begin Current Node execution: %v", err)
	}
	binding := publication.Commit()
	t.Cleanup(func() {
		if err := binding.Close(); err != nil {
			t.Errorf("close Current Node execution: %v", err)
		}
	})

	if err := engine.ResetLockedContractForWorkflowCompactionBoundary(); err == nil {
		t.Fatal("reset accepted an active Current Node execution")
	}
	if store.Meta().Locked == nil {
		t.Fatal("active Current Node reset cleared the persisted locked contract")
	}
	if _, ok := engine.lockedContractState().Snapshot(); !ok {
		t.Fatal("active Current Node reset cleared the active locked contract")
	}
}
