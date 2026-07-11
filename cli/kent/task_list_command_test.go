package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

const taskListWorkflowSelector = "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"

func TestTaskListSendsTypedFiltersAndSorts(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		ProjectID:  "project-1",
		WorkflowID: taskListWorkflowSelector,
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand(
		"task", "list",
		"--project", "project-1",
		"--workflow", taskListWorkflowSelector,
		"--status", "queued,running",
		"--attention", "approval",
		"--column", "plan",
		"--sort", "status:asc",
		"--sort", "column:desc",
	)
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.requests) != 1 {
		t.Fatalf("requests = %+v, want one request", remote.requests)
	}
	request := remote.requests[0]
	if request.ProjectID == nil || *request.ProjectID != "project-1" || request.WorkflowID == nil || *request.WorkflowID != taskListWorkflowSelector {
		t.Fatalf("scope = %+v, want exact pair", request)
	}
	if !reflect.DeepEqual(request.StatusKinds, []serverapi.WorkflowTaskStatusKind{
		serverapi.WorkflowTaskStatusKindQueued,
		serverapi.WorkflowTaskStatusKindRunning,
	}) {
		t.Fatalf("status kinds = %+v", request.StatusKinds)
	}
	if !reflect.DeepEqual(request.AttentionKinds, []serverapi.WorkflowTaskAttentionKind{
		serverapi.WorkflowTaskAttentionKindApproval,
	}) {
		t.Fatalf("attention kinds = %+v", request.AttentionKinds)
	}
	if !reflect.DeepEqual(request.ColumnKeys, []string{"plan"}) {
		t.Fatalf("column keys = %+v", request.ColumnKeys)
	}
	if !reflect.DeepEqual(request.Sort, []serverapi.WorkflowTaskListSort{
		{Field: serverapi.WorkflowTaskListSortFieldStatus, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
		{Field: serverapi.WorkflowTaskListSortFieldColumn, Direction: serverapi.WorkflowTaskListSortDirectionDesc},
	}) {
		t.Fatalf("sort = %+v", request.Sort)
	}
}

func TestTaskListWorkflowOnlyLeavesProjectScopeAbsent(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		ProjectID:  "project-1",
		WorkflowID: taskListWorkflowSelector,
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("task", "list", "--workflow", taskListWorkflowSelector)
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.requests) != 1 || remote.requests[0].ProjectID != nil || remote.requests[0].WorkflowID == nil || *remote.requests[0].WorkflowID != taskListWorkflowSelector {
		t.Fatalf("requests = %+v, want workflow-only scope", remote.requests)
	}
}

func TestTaskListTokenContinuationLeavesScopeAbsent(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		ProjectID:  "project-1",
		WorkflowID: "workflow-1",
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("task", "list", "--page-token", "opaque-token")
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.requests) != 1 || remote.requests[0].ProjectID != nil || remote.requests[0].WorkflowID != nil || remote.requests[0].PageToken != "opaque-token" {
		t.Fatalf("requests = %+v, want token-only scope", remote.requests)
	}
}

func TestTaskListRejectsUnknownResponseStatus(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		ProjectID:  "project-1",
		WorkflowID: "workflow-1",
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID: "task-1",
			Status: serverapi.WorkflowTaskStatus{Kind: "future_status"},
		}},
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--project", "project-1")
	if code != 1 || stdout != "" {
		t.Fatalf("task list exit=%d stdout=%q stderr=%q, want unsupported-status failure without output", code, stdout, stderr)
	}
}

func TestTaskListRejectsInvalidFlagsBeforeOpeningRemote(t *testing.T) {
	called := false
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		called = true
		return config.App{}, nil, nil
	}
	defer func() { workflowCommandRemoteOpener = original }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := taskListSubcommand([]string{"--run-status", "running"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("task list code=%d stderr=%q, want usage failure", code, stderr.String())
	}
	if called {
		t.Fatal("remote opener was called for invalid flags")
	}
}

func TestTaskListRejectsBlankWorkflowBeforeOpeningRemote(t *testing.T) {
	called := false
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		called = true
		return config.App{}, nil, nil
	}
	defer func() { workflowCommandRemoteOpener = original }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := taskListSubcommand([]string{"--workflow", " "}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("task list code=%d stderr=%q, want usage failure", code, stderr.String())
	}
	if called {
		t.Fatal("remote opener was called for a blank workflow selector")
	}
}

func TestTaskListWorkflowSelectorRequiresPrefixedV4UUID(t *testing.T) {
	value, err := workflowPointer(taskListWorkflowSelector)
	if err != nil || value == nil || *value != taskListWorkflowSelector {
		t.Fatalf("workflowPointer valid selector = %v, %v", value, err)
	}
	for _, invalid := range []string{"", " ", "workflow-7e8d24d2-8a98-1dcf-a197-6214db1cb3c0", "workflow-not-a-uuid", "other-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"} {
		if _, err := workflowPointer(invalid); err == nil {
			t.Fatalf("workflowPointer(%q) succeeded", invalid)
		}
	}
}

func TestTaskListParsesTypedStatusFilters(t *testing.T) {
	statuses, err := parseTaskListStatusKinds([]string{"queued,running"})
	if err != nil {
		t.Fatalf("parseTaskListStatusKinds: %v", err)
	}
	if len(statuses) != 2 || statuses[0] != serverapi.WorkflowTaskStatusKindQueued || statuses[1] != serverapi.WorkflowTaskStatusKindRunning {
		t.Fatalf("statuses = %+v", statuses)
	}
	attention, err := parseTaskListAttentionKinds([]string{"approval"})
	if err != nil || len(attention) != 1 || attention[0] != serverapi.WorkflowTaskAttentionKindApproval {
		t.Fatalf("attention = %+v, err=%v", attention, err)
	}
}

type capturingTaskListRemote struct {
	client.WorkflowClient
	requests []serverapi.WorkflowTaskListRequest
	response serverapi.WorkflowTaskListResponse
	err      error
}

func (r *capturingTaskListRemote) Close() error { return nil }

func (r *capturingTaskListRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, errors.New("unexpected project path resolution")
}

func (r *capturingTaskListRemote) ListWorkflowTasks(_ context.Context, request serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	r.requests = append(r.requests, request)
	if r.err != nil {
		return serverapi.WorkflowTaskListResponse{}, r.err
	}
	return r.response, nil
}

func TestTaskListJSONUsesTypedStatusObject(t *testing.T) {
	remote := &capturingTaskListRemote{response: serverapi.WorkflowTaskListResponse{
		ProjectID:  "project-1",
		WorkflowID: "workflow-1",
		Tasks: []serverapi.WorkflowTaskListItem{{
			TaskID: "task-1",
			Status: serverapi.WorkflowTaskStatus{
				Kind:        serverapi.WorkflowTaskStatusKindQueued,
				NativeState: "queued",
			},
			ColumnKeys: []string{"plan"},
		}},
	}}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--project", "project-1", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("list json = %q: %v", stdout, err)
	}
	if _, exists := payload["run_status"]; exists {
		t.Fatalf("list json = %q, must not contain run_status", stdout)
	}
}

func TestTaskListLoopbackResolvesUniqueProjectAndWorkflowScopes(t *testing.T) {
	ctx := context.Background()
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()
	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Unique Scope")
	task := createTaskForListCommandTest(t, ctx, remote, binding.ProjectID, workflowID, "Task")

	for _, args := range [][]string{
		{"task", "list", "--project", binding.ProjectID, "--json"},
		{"task", "list", "--workflow", workflowID, "--json"},
	} {
		stdout, stderr, code := runWorkflowRootCommand(args...)
		if code != 0 {
			t.Fatalf("%v exit=%d stderr=%q", args, code, stderr)
		}
		var response taskListOutput
		if err := json.Unmarshal([]byte(stdout), &response); err != nil {
			t.Fatalf("%v JSON = %q: %v", args, stdout, err)
		}
		if response.ProjectID != binding.ProjectID || response.WorkflowID != workflowID || len(response.Tasks) != 1 || response.Tasks[0].TaskID != string(task.ID) {
			t.Fatalf("%v response = %+v, want exact resolved scope and task", args, response)
		}
	}
}

func TestTaskListLoopbackRejectsAmbiguousWorkflowScope(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()
	setupLinkedWorkflow(t, binding.ProjectID, "First Workflow")
	setupLinkedWorkflow(t, binding.ProjectID, "Second Workflow")

	_, stderr, code := runWorkflowRootCommand("task", "list", "--project", binding.ProjectID)
	if code != 1 {
		t.Fatalf("task list exit=%d stderr=%q, want ambiguity failure", code, stderr)
	}
}

func TestTaskListLoopbackValidatesExactPairAndContinuesFromToken(t *testing.T) {
	ctx := context.Background()
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()
	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Exact Scope")
	createTaskForListCommandTest(t, ctx, remote, binding.ProjectID, workflowID, "First")
	createTaskForListCommandTest(t, ctx, remote, binding.ProjectID, workflowID, "Second")

	firstJSON, firstErr, code := runWorkflowRootCommand("task", "list", "--project", binding.ProjectID, "--workflow", workflowID, "--page-size", "1", "--json")
	if code != 0 {
		t.Fatalf("first list exit=%d stderr=%q", code, firstErr)
	}
	var first taskListOutput
	if err := json.Unmarshal([]byte(firstJSON), &first); err != nil {
		t.Fatalf("first list JSON = %q: %v", firstJSON, err)
	}
	if first.ProjectID != binding.ProjectID || first.WorkflowID != workflowID || len(first.Tasks) != 1 || first.NextPageToken == "" {
		t.Fatalf("first page = %+v, want exact scope and continuation", first)
	}

	secondJSON, secondErr, code := runWorkflowRootCommand("task", "list", "--page-token", first.NextPageToken, "--page-size", "1", "--json")
	if code != 0 {
		t.Fatalf("continuation exit=%d stderr=%q", code, secondErr)
	}
	var second taskListOutput
	if err := json.Unmarshal([]byte(secondJSON), &second); err != nil {
		t.Fatalf("continuation JSON = %q: %v", secondJSON, err)
	}
	if second.ProjectID != binding.ProjectID || second.WorkflowID != workflowID || len(second.Tasks) != 1 || second.Tasks[0].TaskID == first.Tasks[0].TaskID {
		t.Fatalf("continuation = %+v, want distinct next task in exact token scope", second)
	}

	unlinkedWorkflowID := createRunnableWorkflowForCommandTest(t, "Unlinked Workflow")
	_, invalidPairErr, invalidPairCode := runWorkflowRootCommand("task", "list", "--project", binding.ProjectID, "--workflow", unlinkedWorkflowID)
	if invalidPairCode != 1 {
		t.Fatalf("unlinked exact pair exit=%d stderr=%q, want typed not-linked failure", invalidPairCode, invalidPairErr)
	}
}

func createTaskForListCommandTest(t *testing.T, ctx context.Context, remote *workflowCommandLoopbackRemote, projectID string, workflowID string, title string) workflowstore.TaskRecord {
	t.Helper()
	task, err := remote.store.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  projectID,
		WorkflowID: workflow.WorkflowID(workflowID),
		Title:      title,
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask %q: %v", title, err)
	}
	return task
}
