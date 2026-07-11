package workflowstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/workflow"
	"core/server/workflowscript"
)

func TestWorkflowCreateUpdateReadAndGraphPersistence(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)

	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Default Pipeline", Description: "desc"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, record, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if record.Version != 1 {
		t.Fatalf("workflow version = %d, want 1", record.Version)
	}
	if !hasNode(def, "backlog", workflow.NodeKindStart) || !hasNode(def, "done", workflow.NodeKindTerminal) {
		t.Fatalf("default nodes missing from %+v", def.Nodes)
	}
	if err := store.UpdateWorkflowInfo(ctx, created.ID, "Renamed", "new desc"); err != nil {
		t.Fatalf("UpdateWorkflowInfo: %v", err)
	}
	_, renamed, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition renamed: %v", err)
	}
	if renamed.Name != "Renamed" || renamed.Version != 2 {
		t.Fatalf("workflow info update = %+v, want name changed with version bump", renamed)
	}
	if err := store.UpdateWorkflowInfo(ctx, created.ID, "   ", "new desc"); !errors.Is(err, ErrWorkflowNameRequired) {
		t.Fatalf("UpdateWorkflowInfo blank name error = %v", err)
	}

	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	revision, err := store.AddNode(ctx, NodeRecord{ID: "node-agent", WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work.", InputFields: []workflow.InputField{{Name: "brief", Description: "Brief."}}, OutputFields: []workflow.OutputField{{Name: "summary", Description: "Summary."}}})
	if err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if revision != 3 {
		t.Fatalf("revision after add node = %d, want 3", revision)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-agent", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Start from {{.TaskTitle}}."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: "node-agent", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary for terminal history."}}, OutputRequirements: []workflow.OutputRequirement{{FieldName: "summary"}}}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	updated, updatedRecord, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition updated: %v", err)
	}
	if updatedRecord.Version != 7 {
		t.Fatalf("workflow version after graph edits = %d, want 7", updatedRecord.Version)
	}
	if len(updated.TransitionGroups) != 2 || len(updated.Edges) != 2 {
		t.Fatalf("graph persistence mismatch: groups=%+v edges=%+v", updated.TransitionGroups, updated.Edges)
	}
	agent := nodeByKey(t, updated, "agent")
	if workflow.NodePromptTemplate(agent) != "Do work." || len(workflow.NodeInputFields(agent)) != 1 || workflow.NodeInputFields(agent)[0].Name != "brief" || len(workflow.NodeOutputFields(agent)) != 1 || workflow.NodeOutputFields(agent)[0].Name != "summary" {
		t.Fatalf("legacy node contract fields = %+v, want prompt/input/output metadata round-tripped", agent)
	}
	startEdge := edgeByKey(t, updated, "start")
	if startEdge.PromptTemplate != "Start from {{.TaskTitle}}." {
		t.Fatalf("start edge prompt = %q, want transition prompt round-tripped", startEdge.PromptTemplate)
	}
	doneEdge := edgeByKey(t, updated, "done")
	if len(doneEdge.Parameters) != 1 || doneEdge.Parameters[0].Key != "summary" || doneEdge.Parameters[0].Description != "Summary for terminal history." {
		t.Fatalf("done edge parameters = %+v, want transition parameters round-tripped", doneEdge.Parameters)
	}
	workflows, err := store.ListWorkflows(ctx, ListWorkflowsRequest{})
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(workflows.Workflows) != 1 || workflows.Workflows[0].ID != created.ID {
		t.Fatalf("ListWorkflows = %+v", workflows)
	}
}

func TestScriptNodePersistsNullableScriptPath(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Script Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if _, err := store.AddNode(ctx, NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script"}); err != nil {
		t.Fatalf("AddNode script: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	script := nodeByKey(t, def, "script")
	if script.Kind() != workflow.NodeKindScript {
		t.Fatalf("script kind = %q", script.Kind())
	}
	if path, ok := workflow.NodeScriptPath(script).Value(); ok || path != "" {
		t.Fatalf("script path = %q/%t, want absent", path, ok)
	}
	if _, err := store.UpdateNode(ctx, NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script", ScriptPath: "scripts/complete"}); err != nil {
		t.Fatalf("UpdateNode script path: %v", err)
	}
	def, _, err = store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition after path update: %v", err)
	}
	path, ok := workflow.NodeScriptPath(nodeByKey(t, def, "script")).Value()
	if !ok || path != "scripts/complete" {
		t.Fatalf("script path = %q/%t, want present scripts/complete", path, ok)
	}
}

func TestStartTaskSchedulesScriptFirstTargetRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Script Start Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddNode(ctx, NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script", ScriptPath: "scripts/complete"}); err != nil {
		t.Fatalf("AddNode script: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-script", ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: "node-script", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, created.ID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeRoot := filepath.Join(t.TempDir(), "script-worktree")
	scriptPath := filepath.Join(worktreeRoot, "scripts", "complete")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("create script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '{}'\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	attachManagedWorktree(t, ctx, store, binding.WorkspaceID, task.ID, worktreeRoot)

	started := startTask(t, ctx, store, task.ID)

	if started.RunID == "" {
		t.Fatalf("start result has no run id: %+v", started)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].NodeID != "node-script" {
		t.Fatalf("runs = %+v, want one script run", runs)
	}
	runnable, err := store.ListRunnableRuns(ctx, 10)
	if err != nil {
		t.Fatalf("ListRunnableRuns: %v", err)
	}
	if len(runnable) != 1 || runnable[0].ID != started.RunID || runnable[0].ProjectID != binding.ProjectID || runnable[0].NodeID != "node-script" {
		t.Fatalf("runnable = %+v, want script run", runnable)
	}
}

func TestRunStartContextLoadsClearedLiveScriptPath(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Script Clear Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddNode(ctx, NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script", ScriptPath: "scripts/complete"}); err != nil {
		t.Fatalf("AddNode script: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-script", ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: "node-script", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, created.ID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeRoot := filepath.Join(t.TempDir(), "script-worktree")
	scriptPath := filepath.Join(worktreeRoot, "scripts", "complete")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("create script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '{}'\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	attachManagedWorktree(t, ctx, store, binding.WorkspaceID, task.ID, worktreeRoot)
	started := startTask(t, ctx, store, task.ID)
	if _, err := store.GetRunStartContext(ctx, started.RunID); err != nil {
		t.Fatalf("GetRunStartContext before clear: %v", err)
	}
	if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	if _, err := store.UpdateNode(ctx, NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script"}); err != nil {
		t.Fatalf("UpdateNode clear script path: %v", err)
	}

	input, err := store.GetRunStartContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext after clear: %v", err)
	}
	if input.Node.ScriptPath != "" {
		t.Fatalf("live script path = %q, want cleared", input.Node.ScriptPath)
	}
}

func TestScriptCompletionUsesLiveOutputContract(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Script Live Contract Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddNode(ctx, NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script", ScriptPath: "scripts/complete"}); err != nil {
		t.Fatalf("AddNode script: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-script", ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: "node-script", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, created.ID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeRoot := filepath.Join(t.TempDir(), "script-worktree")
	scriptPath := filepath.Join(worktreeRoot, "scripts", "complete")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("create script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '{}'\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	attachManagedWorktree(t, ctx, store, binding.WorkspaceID, task.ID, worktreeRoot)
	started := startTask(t, ctx, store, task.ID)
	parameters, err := marshalJSONArray([]workflow.Parameter{{Key: "summary", Description: "Live summary."}})
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	// Intentional direct graph mutation: the separate graph-edit policy controls
	// whether live edits are accepted. This test isolates the script completion
	// contract once the current graph has changed.
	if _, err := store.db.ExecContext(ctx, `UPDATE workflow_edges SET parameters_json = ? WHERE id = ?`, parameters, "edge-done"); err != nil {
		t.Fatalf("force live script output contract: %v", err)
	}

	_, err = store.CompleteRun(ctx, CompleteRunRequest{RunID: started.RunID, Actor: "script"})

	var validationErr CompletionValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("CompleteRun error = %v, want output validation", err)
	}
	if len(validationErr.Issues) != 1 || validationErr.Issues[0].Code != CompletionCodeRequiredOutputMissing || validationErr.Issues[0].Field != "summary" {
		t.Fatalf("validation issues = %+v, want live summary requirement", validationErr.Issues)
	}
}

func TestRunCompletionContextUsesLiveScriptContract(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createScriptStartWorkflow(t, ctx, store, "scripts/complete")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeRoot := filepath.Join(t.TempDir(), "script-worktree")
	scriptPath := filepath.Join(worktreeRoot, "scripts", "complete")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("create script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '{}'\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	attachManagedWorktree(t, ctx, store, binding.WorkspaceID, task.ID, worktreeRoot)
	started := startTask(t, ctx, store, task.ID)
	parameters, err := marshalJSONArray([]workflow.Parameter{{Key: "summary", Description: "Live summary."}})
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE workflow_edges SET parameters_json = ? WHERE id = ?`, parameters, "edge-done-"+string(workflowID)); err != nil {
		t.Fatalf("force live script output contract: %v", err)
	}

	contract, err := store.GetRunCompletionContext(ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunCompletionContext: %v", err)
	}
	if len(contract.TransitionOptions) != 1 || contract.TransitionOptions[0].ID != "done" || len(contract.TransitionOptions[0].Parameters) != 1 || contract.TransitionOptions[0].Parameters[0].Key != "summary" {
		t.Fatalf("completion context = %+v, want live done transition requiring summary", contract)
	}
}

func TestResumeTaskRunsSkipsAgentRoleValidationForScriptRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createScriptStartWorkflow(t, ctx, store, "scripts/complete")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeRoot := filepath.Join(t.TempDir(), "script-worktree")
	scriptPath := filepath.Join(worktreeRoot, "scripts", "complete")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("create script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '{}'\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	attachManagedWorktree(t, ctx, store, binding.WorkspaceID, task.ID, worktreeRoot)
	started := startTask(t, ctx, store, task.ID)
	if err := store.InterruptRun(ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	store.roleResolver = workflow.StaticRoleResolver{}

	resumed, err := store.ResumeTaskRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumed) != 1 || resumed[0].ID != started.RunID || resumed[0].InterruptedAt != 0 {
		t.Fatalf("resumed = %+v, want script run requeued", resumed)
	}
}

func TestStartTaskBlocksInvalidScriptFirstTarget(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Missing Script Start Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	if _, err := store.AddNode(ctx, NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script", ScriptPath: "missing"}); err != nil {
		t.Fatalf("AddNode script: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-script", ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: "node-script", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, created.ID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	attachManagedWorktree(t, ctx, store, binding.WorkspaceID, task.ID, filepath.Join(t.TempDir(), "script-worktree"))

	_, err = store.StartTask(ctx, task.ID)
	var validationErr workflowscript.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("StartTask error = %v, want script validation", err)
	}
	if validationErr.Diagnostic.Code != workflowscript.CodePathNotFound {
		t.Fatalf("validation code = %q, want %q", validationErr.Diagnostic.Code, workflowscript.CodePathNotFound)
	}
}

func TestCompleteRunReturnsPreInterruptedScriptTargetRun(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Agent To Script Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := nodeByKind(t, def, workflow.NodeKindStart)
	done := nodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent")
	scriptID := workflow.NodeID("node-script")
	if _, err := store.AddNode(ctx, NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddNode agent: %v", err)
	}
	if _, err := store.AddNode(ctx, NodeRecord{ID: scriptID, WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script"}); err != nil {
		t.Fatalf("AddNode script: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-next", WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "next", DisplayName: "Next"}); err != nil {
		t.Fatalf("AddTransitionGroup next: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-next", WorkflowID: created.ID, TransitionGroupID: "group-next", Key: "next", TargetNodeID: scriptID, ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge next: %v", err)
	}
	if _, err := store.AddTransitionGroup(ctx, TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: scriptID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, created.ID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	attachManagedWorktree(t, ctx, store, binding.WorkspaceID, task.ID, filepath.Join(t.TempDir(), "script-worktree"))
	started := startTask(t, ctx, store, task.ID)

	result := completeRun(t, ctx, store, CompleteRunRequest{RunID: started.RunID, TransitionID: "next"})

	if len(result.RunIDs) != 1 || len(result.InterruptedRunIDs) != 1 || result.InterruptedRunIDs[0] != result.RunIDs[0] {
		t.Fatalf("completion result = %+v, want interrupted script target run", result)
	}
	runs, err := store.ListRuns(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	for _, run := range runs {
		if run.ID != result.InterruptedRunIDs[0] {
			continue
		}
		if run.InterruptedAt == 0 || run.InterruptionReason != workflowscript.ReasonValidationFailed {
			t.Fatalf("script run = %+v, want validation interruption", run)
		}
		return
	}
	t.Fatalf("script run %s not found in %+v", result.InterruptedRunIDs[0], runs)
}

func attachManagedWorktree(t *testing.T, ctx context.Context, store *Store, workspaceID string, taskID workflow.TaskID, worktreeRoot string) {
	t.Helper()
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatalf("create worktree root: %v", err)
	}
	worktreeID := "worktree-" + string(taskID)
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{ID: worktreeID, WorkspaceID: workspaceID, CanonicalRoot: worktreeRoot, Managed: true, CreatedBranch: true}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET source_workspace_id = ?, managed_worktree_id = ? WHERE id = ?`, workspaceID, worktreeID, string(taskID)); err != nil {
		t.Fatalf("attach managed worktree to task: %v", err)
	}
}

func TestWorkflowListPaginatesWithMostRecentOrderAndFilters(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	created := map[string]workflow.WorkflowID{}
	for index, name := range []string{"Gamma", "Alpha", "Beta", "Beta Searchable"} {
		record, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: name, Description: "desc " + name})
		if err != nil {
			t.Fatalf("CreateWorkflow %q: %v", name, err)
		}
		created[name] = record.ID
		// Intentional direct timestamp fixture: workflow updates use wall-clock time,
		// so pagination ordering needs pinned row times to stay deterministic.
		if _, err := store.db.ExecContext(ctx, `UPDATE workflows SET updated_at_unix_ms = ? WHERE id = ?`, int64(index+1), string(record.ID)); err != nil {
			t.Fatalf("force workflow timestamp: %v", err)
		}
	}

	page1, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("ListWorkflows page1: %v", err)
	}
	if len(page1.Workflows) != 2 || page1.NextPageToken == "" {
		t.Fatalf("page1 = %+v, want two workflows and next token", page1)
	}
	if page1.Workflows[0].ID != created["Beta Searchable"] || page1.Workflows[1].ID != created["Beta"] {
		t.Fatalf("page1 order = %+v", page1.Workflows)
	}
	page2, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 2, PageToken: page1.NextPageToken})
	if err != nil {
		t.Fatalf("ListWorkflows page2: %v", err)
	}
	if len(page2.Workflows) != 2 || page2.NextPageToken != "" {
		t.Fatalf("page2 = %+v, want final two workflows", page2)
	}
	if page2.Workflows[0].ID != created["Alpha"] || page2.Workflows[1].ID != created["Gamma"] {
		t.Fatalf("page2 order = %+v", page2.Workflows)
	}
	filtered, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 10, Query: "search"})
	if err != nil {
		t.Fatalf("ListWorkflows filtered: %v", err)
	}
	if len(filtered.Workflows) != 1 || filtered.Workflows[0].ID != created["Beta Searchable"] {
		t.Fatalf("filtered = %+v", filtered.Workflows)
	}
	exact, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 10, ExactName: "Beta"})
	if err != nil {
		t.Fatalf("ListWorkflows exact: %v", err)
	}
	if len(exact.Workflows) != 1 || exact.Workflows[0].ID != created["Beta"] {
		t.Fatalf("exact = %+v", exact.Workflows)
	}

	// A filter and a page cursor must compose: the filter applies inside the
	// workflow_list CTE while the cursor applies to the outer query, so paging
	// through a filtered result set must stay valid and ordered.
	filteredPage1, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 1, Query: "Beta"})
	if err != nil {
		t.Fatalf("ListWorkflows filtered page1: %v", err)
	}
	if len(filteredPage1.Workflows) != 1 || filteredPage1.NextPageToken == "" {
		t.Fatalf("filtered page1 = %+v, want one workflow and next token", filteredPage1)
	}
	if filteredPage1.Workflows[0].ID != created["Beta Searchable"] {
		t.Fatalf("filtered page1 order = %+v", filteredPage1.Workflows)
	}
	filteredPage2, err := store.ListWorkflows(ctx, ListWorkflowsRequest{
		PageSize:  1,
		Query:     "Beta",
		PageToken: filteredPage1.NextPageToken,
	})
	if err != nil {
		t.Fatalf("ListWorkflows filtered page2: %v", err)
	}
	if len(filteredPage2.Workflows) != 1 || filteredPage2.NextPageToken != "" {
		t.Fatalf("filtered page2 = %+v, want final filtered workflow", filteredPage2)
	}
	if filteredPage2.Workflows[0].ID != created["Beta"] {
		t.Fatalf("filtered page2 order = %+v", filteredPage2.Workflows)
	}
}

func TestProjectWorkflowLinkFirstDefaultAndDuplicateIdempotency(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowA, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow A"})
	if err != nil {
		t.Fatalf("CreateWorkflow A: %v", err)
	}
	workflowB, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow B"})
	if err != nil {
		t.Fatalf("CreateWorkflow B: %v", err)
	}

	first, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, workflowA.ID, WorkflowLinkDefaultIfProjectHasNone)
	if err != nil {
		t.Fatalf("LinkWorkflowWithDefaultPolicy first: %v", err)
	}
	if !first.IsDefault {
		t.Fatalf("first link = %+v, want default", first)
	}
	duplicate, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, workflowA.ID, WorkflowLinkDefaultIfProjectHasNone)
	if err != nil {
		t.Fatalf("duplicate LinkWorkflowWithDefaultPolicy: %v", err)
	}
	if duplicate.ID != first.ID || !duplicate.IsDefault {
		t.Fatalf("duplicate link = %+v, want existing default link %+v", duplicate, first)
	}
	second, err := store.LinkWorkflowWithDefaultPolicy(ctx, binding.ProjectID, workflowB.ID, WorkflowLinkDefaultIfProjectHasNone)
	if err != nil {
		t.Fatalf("LinkWorkflowWithDefaultPolicy second: %v", err)
	}
	if second.IsDefault {
		t.Fatalf("second link = %+v, want non-default", second)
	}
}

func TestCreateAndLinkWorkflowIsAtomicAndAppliesFirstDefaultPolicy(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created, link, err := store.CreateAndLinkWorkflow(ctx, CreateAndLinkWorkflowRequest{
		Name:          "Created from Project",
		ProjectID:     binding.ProjectID,
		DefaultPolicy: WorkflowLinkDefaultIfProjectHasNone,
	})
	if err != nil {
		t.Fatalf("CreateAndLinkWorkflow: %v", err)
	}
	if created.ID == "" || link.WorkflowID != created.ID || !link.IsDefault {
		t.Fatalf("created=%+v link=%+v, want linked first default", created, link)
	}
	if _, _, err := store.CreateAndLinkWorkflow(ctx, CreateAndLinkWorkflowRequest{
		Name:          "Broken",
		ProjectID:     "missing-project",
		DefaultPolicy: WorkflowLinkDefaultIfProjectHasNone,
	}); err == nil {
		t.Fatalf("expected invalid project create-and-link to fail")
	}
	listed, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 10, Query: "Broken"})
	if err != nil {
		t.Fatalf("ListWorkflows after failed create-and-link: %v", err)
	}
	if len(listed.Workflows) != 0 {
		t.Fatalf("failed create-and-link left workflows: %+v", listed.Workflows)
	}
}

func TestAddNodeRejectsNodeGroupFromDifferentWorkflow(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	workflowA, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow A"})
	if err != nil {
		t.Fatalf("CreateWorkflow A: %v", err)
	}
	workflowB, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow B"})
	if err != nil {
		t.Fatalf("CreateWorkflow B: %v", err)
	}
	group, _, err := store.AddNodeGroup(ctx, NodeGroupRecord{ID: "group-a", WorkflowID: workflowA.ID, Key: "impl", DisplayName: "Implementation"})
	if err != nil {
		t.Fatalf("AddNodeGroup: %v", err)
	}

	_, err = store.AddNode(ctx, NodeRecord{ID: "node-cross-group", WorkflowID: workflowB.ID, GroupID: group.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent"})
	if !errors.Is(err, ErrBelongsToOtherWorkflow) {
		t.Fatalf("AddNode cross-workflow group error = %v", err)
	}
}

func TestWorkflowEventPublisherNormalizesAndDispatchesEvents(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	store.now = func() time.Time { return time.UnixMilli(1234).UTC() }
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{Action: "created"}); !errors.Is(err, ErrEventResourceRequired) {
		t.Fatalf("missing resource error = %v", err)
	}
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{Resource: "task"}); !errors.Is(err, ErrEventActionRequired) {
		t.Fatalf("missing action error = %v", err)
	}
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{Resource: "task", Action: "created"}); err != nil {
		t.Fatalf("PublishWorkflowEvent with default no-op sink: %v", err)
	}

	sink := &recordingWorkflowEventPublisher{}
	store.SetWorkflowEventPublisher(sink)
	changedIDs := []string{"task-1"}
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{
		ProjectID:  " project-1 ",
		WorkflowID: " workflow-1 ",
		Resource:   " task ",
		Action:     " updated ",
		ChangedIDs: changedIDs,
	}); err != nil {
		t.Fatalf("PublishWorkflowEvent: %v", err)
	}
	changedIDs[0] = "mutated"
	if len(sink.records) != 1 {
		t.Fatalf("published records = %+v, want one", sink.records)
	}
	record := sink.records[0]
	if record.ProjectID != "project-1" || record.WorkflowID != "workflow-1" || record.Resource != "task" || record.Action != "updated" || record.OccurredAtUnixMs != 1234 {
		t.Fatalf("published record = %+v, want normalized fields and default timestamp", record)
	}
	if len(record.ChangedIDs) != 1 || record.ChangedIDs[0] != "task-1" {
		t.Fatalf("changed ids = %+v, want defensive copy", record.ChangedIDs)
	}
	store.SetWorkflowEventPublisher(nil)
	if err := store.PublishWorkflowEvent(ctx, WorkflowEventRecord{Resource: "task", Action: "deleted"}); err != nil {
		t.Fatalf("PublishWorkflowEvent after nil publisher reset: %v", err)
	}
}

type recordingWorkflowEventPublisher struct {
	records []WorkflowEventRecord
}

func (p *recordingWorkflowEventPublisher) PublishWorkflowEvent(_ context.Context, record WorkflowEventRecord) error {
	p.records = append(p.records, record)
	return nil
}

func TestWorkflowGraphUpdatesRejectCrossWorkflowReferences(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	firstWorkflowID := createValidWorkflow(t, ctx, store)
	secondWorkflowID := createValidWorkflow(t, ctx, store)
	firstDef, _, err := store.GetDefinition(ctx, firstWorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition first: %v", err)
	}
	secondDef, _, err := store.GetDefinition(ctx, secondWorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition second: %v", err)
	}
	firstAgent := nodeByKey(t, firstDef, "agent")
	secondAgent := nodeByKey(t, secondDef, "agent")
	secondDone := nodeByKind(t, secondDef, workflow.NodeKindTerminal)

	if _, err := store.UpdateTransitionGroup(ctx, TransitionGroupRecord{ID: workflow.TransitionGroupID("group-done-" + string(firstWorkflowID)), WorkflowID: firstWorkflowID, SourceNodeID: workflow.NodeIDOf(secondAgent), TransitionID: "done", DisplayName: "Done"}); !errors.Is(err, ErrBelongsToOtherWorkflow) {
		t.Fatalf("UpdateTransitionGroup cross-workflow error = %v, want workflow mismatch", err)
	}
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(firstWorkflowID)), WorkflowID: firstWorkflowID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(secondWorkflowID)), Key: "done", TargetNodeID: workflow.NodeIDOf(firstAgent), ContextMode: workflow.ContextModeNewSession}); !errors.Is(err, ErrBelongsToOtherWorkflow) {
		t.Fatalf("UpdateEdge cross-workflow group error = %v, want workflow mismatch", err)
	}
	if _, err := store.UpdateEdge(ctx, EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(firstWorkflowID)), WorkflowID: firstWorkflowID, TransitionGroupID: workflow.TransitionGroupID("group-done-" + string(firstWorkflowID)), Key: "done", TargetNodeID: workflow.NodeIDOf(secondDone), ContextMode: workflow.ContextModeNewSession}); !errors.Is(err, ErrBelongsToOtherWorkflow) {
		t.Fatalf("UpdateEdge cross-workflow target error = %v, want workflow mismatch", err)
	}
}
