package workflowstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
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

func TestAddNodeRejectsSecondStartWithDomainError(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	_, err = store.AddNode(ctx, NodeRecord{WorkflowID: created.ID, Key: "second_start", Kind: workflow.NodeKindStart, DisplayName: "Second Start"})
	if !errors.Is(err, ErrWorkflowStartNodeExists) {
		t.Fatalf("AddNode second Start error = %v, want typed duplicate-Start rejection", err)
	}
}

func TestScriptNodePersistsNullableScriptPath(t *testing.T) {
	ctx, store, _ := newTestStoreContext(t)
	created, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Script Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		req.Nodes = append(req.Nodes, NodeRecord{ID: "node-script", WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script"})
	})
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
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveNodeRecord(t, req.Nodes, "node-script").ScriptPath = "scripts/complete"
	})
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
	f := newScriptExecutionFixture(t, "scripts/complete", []byte("#!/bin/sh\nprintf '{}'\n"))
	started := startTask(t, f.ctx, f.store, f.task.ID)

	if started.RunID == "" {
		t.Fatalf("start result has no run id: %+v", started)
	}
	runs, err := f.store.ListRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].NodeID != f.scriptID {
		t.Fatalf("runs = %+v, want one script run", runs)
	}
	runnable, err := f.store.ListRunnableRuns(f.ctx, 10)
	if err != nil {
		t.Fatalf("ListRunnableRuns: %v", err)
	}
	if len(runnable) != 1 || runnable[0].ID != started.RunID || runnable[0].NodeID != f.scriptID {
		t.Fatalf("runnable = %+v, want script run", runnable)
	}
}

func TestRunStartContextLoadsClearedLiveScriptPath(t *testing.T) {
	f := newScriptExecutionFixture(t, "scripts/complete", []byte("#!/bin/sh\nprintf '{}'\n"))
	started := startTask(t, f.ctx, f.store, f.task.ID)
	if _, err := f.store.GetRunStartContext(f.ctx, started.RunID); err != nil {
		t.Fatalf("GetRunStartContext before clear: %v", err)
	}
	if err := f.store.InterruptRun(f.ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	saveWorkflowGraphFixture(t, f.ctx, f.store, f.workflowID, func(_ workflow.Definition, req *WorkflowGraphSaveRequest) {
		workflowGraphSaveNodeRecord(t, req.Nodes, f.scriptID).ScriptPath = ""
	})

	input, err := f.store.GetRunStartContext(f.ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunStartContext after clear: %v", err)
	}
	if input.Node.ScriptPath != "" {
		t.Fatalf("live script path = %q, want cleared", input.Node.ScriptPath)
	}
}

func TestScriptCompletionUsesLiveOutputContract(t *testing.T) {
	f := newScriptExecutionFixture(t, "scripts/complete", []byte("#!/bin/sh\nprintf '{}'\n"))
	started := startTask(t, f.ctx, f.store, f.task.ID)
	f.requireLiveSummary(t)
	_, err := f.store.CompleteRun(f.ctx, CompleteRunRequest{RunID: started.RunID, Actor: "script"})

	var validationErr CompletionValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("CompleteRun error = %v, want output validation", err)
	}
	if len(validationErr.Issues) != 1 || validationErr.Issues[0].Code != CompletionCodeRequiredOutputMissing || validationErr.Issues[0].Field != "summary" {
		t.Fatalf("validation issues = %+v, want live summary requirement", validationErr.Issues)
	}
}

func TestRunCompletionContextUsesLiveScriptContract(t *testing.T) {
	f := newScriptExecutionFixture(t, "scripts/complete", []byte("#!/bin/sh\nprintf '{}'\n"))
	started := startTask(t, f.ctx, f.store, f.task.ID)
	f.requireLiveSummary(t)

	contract, err := f.store.GetRunCompletionContext(f.ctx, started.RunID)
	if err != nil {
		t.Fatalf("GetRunCompletionContext: %v", err)
	}
	if len(contract.TransitionOptions) != 1 || contract.TransitionOptions[0].ID != "done" || len(contract.TransitionOptions[0].Parameters) != 1 || contract.TransitionOptions[0].Parameters[0].Key != "summary" {
		t.Fatalf("completion context = %+v, want live done transition requiring summary", contract)
	}
}

func TestResumeTaskRunsSkipsAgentRoleValidationForScriptRun(t *testing.T) {
	f := newScriptExecutionFixture(t, "scripts/complete", []byte("#!/bin/sh\nprintf '{}'\n"))
	started := startTask(t, f.ctx, f.store, f.task.ID)
	if err := f.store.InterruptRun(f.ctx, started.RunID, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRun: %v", err)
	}
	f.store.roleResolver = testsetup.QuestionsEnabled()

	resumed, err := f.store.ResumeTaskRuns(f.ctx, f.task.ID)
	if err != nil {
		t.Fatalf("ResumeTaskRuns: %v", err)
	}
	if len(resumed) != 1 || resumed[0].ID != started.RunID || resumed[0].InterruptedAt != nil {
		t.Fatalf("resumed = %+v, want script run requeued", resumed)
	}
}

func TestStartTaskBlocksInvalidScriptFirstTarget(t *testing.T) {
	f := newScriptExecutionFixture(t, "missing", nil)
	_, err := f.store.StartTask(f.ctx, f.task.ID)
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
	agentID := workflow.NodeID("node-agent")
	scriptID := workflow.NodeID("node-script")
	saveWorkflowGraphFixture(t, ctx, store, created.ID, func(def workflow.Definition, req *WorkflowGraphSaveRequest) {
		start := nodeByKind(t, def, workflow.NodeKindStart)
		done := nodeByKind(t, def, workflow.NodeKindTerminal)
		req.Nodes = append(req.Nodes,
			NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work."},
			NodeRecord{ID: scriptID, WorkflowID: created.ID, Key: "script", Kind: workflow.NodeKindScript, DisplayName: "Script"},
		)
		req.TransitionGroups = append(req.TransitionGroups,
			TransitionGroupRecord{ID: "group-start", WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"},
			TransitionGroupRecord{ID: "group-next", WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "next", DisplayName: "Next"},
			TransitionGroupRecord{ID: "group-done", WorkflowID: created.ID, SourceNodeID: scriptID, TransitionID: "done", DisplayName: "Done"},
		)
		req.Edges = append(req.Edges,
			EdgeRecord{ID: "edge-start", WorkflowID: created.ID, TransitionGroupID: "group-start", Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."},
			EdgeRecord{ID: "edge-next", WorkflowID: created.ID, TransitionGroupID: "group-next", Key: "next", TargetNodeID: scriptID, ContextMode: workflow.ContextModeNewSession},
			EdgeRecord{ID: "edge-done", WorkflowID: created.ID, TransitionGroupID: "group-done", Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession},
		)
	})
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
		if run.InterruptedAt == nil || run.InterruptionReason == nil || *run.InterruptionReason != workflowscript.ReasonValidationFailed {
			t.Fatalf("script run = %+v, want validation interruption", run)
		}
		return
	}
	t.Fatalf("script run %s not found in %+v", result.InterruptedRunIDs[0], runs)
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
	exactWorkflowID := created["Beta"]
	exact, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageSize: 10, WorkflowID: &exactWorkflowID})
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

func TestWorkflowListPageTokenRejectsMalformedOptionalScope(t *testing.T) {
	const cursorWorkflowID = "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	projectDefault := int64(0)
	projectName := "workflow"
	validProjectID := "project-1"
	validFilterWorkflowID := "workflow-8e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	activityAtUnixMs := int64(1)
	base := workflowListPageTokenPayload{
		Version:           workflowListPageTokenVersion,
		ActivityAtUnixMs:  &activityAtUnixMs,
		WorkflowID:        cursorWorkflowID,
		FilterFingerprint: "fingerprint",
	}
	for name, mutate := range map[string]func(*workflowListPageTokenPayload){
		"global missing activity": func(payload *workflowListPageTokenPayload) {
			payload.ActivityAtUnixMs = nil
		},
		"malformed cursor workflow": func(payload *workflowListPageTokenPayload) {
			payload.WorkflowID = "workflow-1"
		},
		"blank project": func(payload *workflowListPageTokenPayload) {
			value := ""
			payload.ProjectID = &value
			payload.ProjectDefault = &projectDefault
			payload.ProjectName = &projectName
		},
		"padded project": func(payload *workflowListPageTokenPayload) {
			value := " project-1"
			payload.ProjectID = &value
			payload.ProjectDefault = &projectDefault
			payload.ProjectName = &projectName
		},
		"padded project name": func(payload *workflowListPageTokenPayload) {
			payload.ProjectID = &validProjectID
			payload.ProjectDefault = &projectDefault
			value := " workflow"
			payload.ProjectName = &value
		},
		"non-canonical project name": func(payload *workflowListPageTokenPayload) {
			payload.ProjectID = &validProjectID
			payload.ProjectDefault = &projectDefault
			value := "Workflow"
			payload.ProjectName = &value
		},
		"padded search query": func(payload *workflowListPageTokenPayload) {
			payload.SearchQuery = " workflow "
		},
		"non-canonical search query": func(payload *workflowListPageTokenPayload) {
			payload.SearchQuery = "Workflow"
		},
		"blank exact workflow filter": func(payload *workflowListPageTokenPayload) {
			value := ""
			payload.FilterWorkflowID = &value
		},
		"padded exact workflow filter": func(payload *workflowListPageTokenPayload) {
			value := " " + validFilterWorkflowID
			payload.FilterWorkflowID = &value
		},
		"malformed exact workflow filter": func(payload *workflowListPageTokenPayload) {
			value := "workflow-1"
			payload.FilterWorkflowID = &value
		},
	} {
		t.Run(name, func(t *testing.T) {
			payload := base
			mutate(&payload)
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal token payload: %v", err)
			}
			if _, err := parseWorkflowListPageToken(base64.RawURLEncoding.EncodeToString(encoded)); err == nil {
				t.Fatalf("parseWorkflowListPageToken accepted %s", name)
			}
		})
	}

	valid := base
	valid.ProjectID = &validProjectID
	valid.ProjectDefault = &projectDefault
	valid.ProjectName = &projectName
	valid.FilterWorkflowID = &validFilterWorkflowID
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid token payload: %v", err)
	}
	if _, err := parseWorkflowListPageToken(base64.RawURLEncoding.EncodeToString(encoded)); err != nil {
		t.Fatalf("parseWorkflowListPageToken rejected valid optional scope: %v", err)
	}

	valid.ActivityAtUnixMs = nil
	encoded, err = json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid project token without activity: %v", err)
	}
	if _, err := parseWorkflowListPageToken(base64.RawURLEncoding.EncodeToString(encoded)); err != nil {
		t.Fatalf("parseWorkflowListPageToken rejected absent project activity: %v", err)
	}
}

func TestWorkflowListProjectScopeOrdersDefaultActivityAndName(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	defaultWorkflow, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Default"})
	if err != nil {
		t.Fatalf("CreateWorkflow default: %v", err)
	}
	activeWorkflow, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Zulu Active"})
	if err != nil {
		t.Fatalf("CreateWorkflow active: %v", err)
	}
	alphaWorkflow, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Alpha"})
	if err != nil {
		t.Fatalf("CreateWorkflow alpha: %v", err)
	}
	unlinkedWorkflow, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: "Unlinked"})
	if err != nil {
		t.Fatalf("CreateWorkflow unlinked: %v", err)
	}
	linkWorkflow(t, ctx, store, binding.ProjectID, defaultWorkflow.ID, true)
	linkWorkflow(t, ctx, store, binding.ProjectID, activeWorkflow.ID, false)
	linkWorkflow(t, ctx, store, binding.ProjectID, alphaWorkflow.ID, false)
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &activeWorkflow.ID,
		Title:      "Active",
		Body:       "Body",
	})
	for _, workflowID := range []workflow.WorkflowID{defaultWorkflow.ID, activeWorkflow.ID, alphaWorkflow.ID, unlinkedWorkflow.ID} {
		if _, err := store.db.ExecContext(ctx, `UPDATE workflows SET updated_at_unix_ms = 10 WHERE id = ?`, string(workflowID)); err != nil {
			t.Fatalf("force workflow timestamp: %v", err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = 100 WHERE id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force task timestamp: %v", err)
	}

	projectID := binding.ProjectID
	page1, err := store.ListWorkflows(ctx, ListWorkflowsRequest{ProjectID: &projectID, PageSize: 2})
	if err != nil {
		t.Fatalf("ListWorkflows project page1: %v", err)
	}
	if len(page1.Workflows) != 2 || page1.NextPageToken == "" {
		t.Fatalf("project page1 = %+v, want two workflows and token", page1)
	}
	if page1.Workflows[0].ID != defaultWorkflow.ID || page1.Workflows[0].ProjectLink == nil || !page1.Workflows[0].ProjectLink.Default {
		t.Fatalf("project default row = %+v", page1.Workflows[0])
	}
	if page1.Workflows[1].ID != activeWorkflow.ID || page1.Workflows[1].ProjectLink == nil || page1.Workflows[1].ProjectLink.Default {
		t.Fatalf("project active row = %+v", page1.Workflows[1])
	}
	page2, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageToken: page1.NextPageToken})
	if err != nil {
		t.Fatalf("ListWorkflows restored project page2: %v", err)
	}
	if len(page2.Workflows) != 1 || page2.Workflows[0].ID != alphaWorkflow.ID || page2.NextPageToken != "" {
		t.Fatalf("project page2 = %+v, want alpha final row", page2)
	}
	for _, record := range append(page1.Workflows, page2.Workflows...) {
		if record.ID == unlinkedWorkflow.ID {
			t.Fatalf("project discovery included unlinked workflow: %+v", record)
		}
	}

	otherProject := "project-other"
	if _, err := store.ListWorkflows(ctx, ListWorkflowsRequest{ProjectID: &otherProject, PageToken: page1.NextPageToken}); err == nil {
		t.Fatal("project cursor accepted conflicting project filter")
	}
}

func TestWorkflowListProjectScopeUsesSQLiteUnicodeOrderKeyAcrossPages(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	for _, name := range []string{"Äworkflow", "Öworkflow"} {
		record, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: name})
		if err != nil {
			t.Fatalf("CreateWorkflow %q: %v", name, err)
		}
		linkWorkflow(t, ctx, store, binding.ProjectID, record.ID, false)
	}
	projectID := binding.ProjectID
	first, err := store.ListWorkflows(ctx, ListWorkflowsRequest{ProjectID: &projectID, PageSize: 1})
	if err != nil {
		t.Fatalf("ListWorkflows first page: %v", err)
	}
	if len(first.Workflows) != 1 || first.NextPageToken == "" {
		t.Fatalf("first page = %+v, want one row and continuation", first)
	}
	second, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageToken: first.NextPageToken, PageSize: 1})
	if err != nil {
		t.Fatalf("ListWorkflows second page: %v", err)
	}
	if len(second.Workflows) != 1 || second.Workflows[0].ID == first.Workflows[0].ID {
		t.Fatalf("paginated workflows first=%+v second=%+v, want distinct complete rows", first.Workflows, second.Workflows)
	}
}

func TestWorkflowListProjectScopePreservesAbsentActivityAfterEpoch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	created := map[string]WorkflowRecord{}
	for _, name := range []string{"Epoch", "Alpha", "Beta"} {
		record, err := store.CreateWorkflow(ctx, CreateWorkflowRequest{Name: name})
		if err != nil {
			t.Fatalf("CreateWorkflow %q: %v", name, err)
		}
		linkWorkflow(t, ctx, store, binding.ProjectID, record.ID, false)
		created[name] = record
	}
	epochWorkflowID := created["Epoch"].ID
	task := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &epochWorkflowID,
		Title:      "Epoch activity",
		Body:       "Body",
	})
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = 0 WHERE id = ?`, string(task.ID)); err != nil {
		t.Fatalf("force epoch task timestamp: %v", err)
	}

	projectID := binding.ProjectID
	first, err := store.ListWorkflows(ctx, ListWorkflowsRequest{ProjectID: &projectID, PageSize: 1})
	if err != nil {
		t.Fatalf("ListWorkflows first page: %v", err)
	}
	if len(first.Workflows) != 1 || first.Workflows[0].ID != epochWorkflowID || first.NextPageToken == "" {
		t.Fatalf("first page = %+v, want epoch activity workflow and continuation", first)
	}
	firstCursor, err := parseWorkflowListPageToken(first.NextPageToken)
	if err != nil {
		t.Fatalf("parse first page token: %v", err)
	}
	if firstCursor.activityAtUnixMs == nil || *firstCursor.activityAtUnixMs != 0 {
		t.Fatalf("first cursor activity = %v, want present Unix epoch", firstCursor.activityAtUnixMs)
	}

	second, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageToken: first.NextPageToken, PageSize: 1})
	if err != nil {
		t.Fatalf("ListWorkflows second page: %v", err)
	}
	if len(second.Workflows) != 1 || second.Workflows[0].ID != created["Alpha"].ID || second.NextPageToken == "" {
		t.Fatalf("second page = %+v, want alpha no-activity workflow and continuation", second)
	}
	secondCursor, err := parseWorkflowListPageToken(second.NextPageToken)
	if err != nil {
		t.Fatalf("parse second page token: %v", err)
	}
	if secondCursor.activityAtUnixMs != nil {
		t.Fatalf("second cursor activity = %v, want typed absence", secondCursor.activityAtUnixMs)
	}

	third, err := store.ListWorkflows(ctx, ListWorkflowsRequest{PageToken: second.NextPageToken, PageSize: 1})
	if err != nil {
		t.Fatalf("ListWorkflows third page: %v", err)
	}
	if len(third.Workflows) != 1 || third.Workflows[0].ID != created["Beta"].ID || third.NextPageToken != "" {
		t.Fatalf("third page = %+v, want beta final row", third)
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
