package main

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestTaskListParsesStructuredFiltersAndSort(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &pagedTaskListRemote{
		board: serverapi.WorkflowBoard{
			ProjectID:        "project-1",
			SelectedWorkflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1"},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand(
		"task", "list",
		"--project", "project-1",
		"--status", "backlog,recon",
		"--column", "plan",
		"--run-status", "open,running,interrupted",
		"--sort", "created_at:desc",
		"--sort", "run_count:asc",
	)
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if len(remote.requests) != 1 {
		t.Fatalf("requests = %+v, want one request", remote.requests)
	}
	req := remote.requests[0]
	if !reflect.DeepEqual(req.StatusKeys, []string{"backlog", "recon", "plan"}) {
		t.Fatalf("status keys = %+v", req.StatusKeys)
	}
	if !reflect.DeepEqual(req.RunStatuses, []serverapi.WorkflowTaskRunStatus{serverapi.WorkflowTaskRunStatusOpen, serverapi.WorkflowTaskRunStatusRunning, serverapi.WorkflowTaskRunStatusInterrupted}) {
		t.Fatalf("run statuses = %+v", req.RunStatuses)
	}
	wantSort := []serverapi.WorkflowTaskListSort{
		{Field: serverapi.WorkflowTaskListSortFieldCreated, Direction: serverapi.WorkflowTaskListSortDirectionDesc},
		{Field: serverapi.WorkflowTaskListSortFieldRunCount, Direction: serverapi.WorkflowTaskListSortDirectionAsc},
	}
	if !reflect.DeepEqual(req.Sort, wantSort) {
		t.Fatalf("sort = %+v, want %+v", req.Sort, wantSort)
	}
}

func TestTaskListParserErrorsBeforeOpeningRemote(t *testing.T) {
	called := false
	original := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		called = true
		return config.App{}, nil, nil
	}
	defer func() { workflowCommandRemoteOpener = original }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := taskListSubcommand([]string{"--sort", "updated"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("task list code=%d stderr=%q, want usage error", code, stderr.String())
	}
	if called {
		t.Fatalf("remote opener was called for invalid sort")
	}
	if !strings.Contains(stderr.String(), "field:direction") {
		t.Fatalf("stderr = %q, want actionable sort error", stderr.String())
	}
}

func TestTaskListCommandFiltersSortsAndPaginatesThroughRealService(t *testing.T) {
	ctx := context.Background()
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()
	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Task List Workflow")
	alpha, err := remote.store.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflow.WorkflowID(workflowID), Title: "Alpha", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask alpha: %v", err)
	}
	bravo, err := remote.store.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflow.WorkflowID(workflowID), Title: "Bravo", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask bravo: %v", err)
	}
	done, err := remote.store.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: workflow.WorkflowID(workflowID), Title: "Charlie", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask done: %v", err)
	}
	started, err := remote.store.StartTask(ctx, done.ID)
	if err != nil {
		t.Fatalf("StartTask done: %v", err)
	}
	if _, err := remote.store.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun done: %v", err)
	}

	firstRaw, _ := runWorkflowRootCommandOK(t, "task", "list", "--project", binding.ProjectID, "--status", "backlog", "--run-status", "open", "--sort", "title:desc", "--page-size", "1", "--json")
	var first taskListOutput
	if err := json.Unmarshal([]byte(firstRaw), &first); err != nil {
		t.Fatalf("first page json = %q: %v", firstRaw, err)
	}
	if len(first.Tasks) != 1 || first.Tasks[0].TaskID != string(bravo.ID) || first.Tasks[0].Status != "open" || first.Tasks[0].RunStatus != "open" || !reflect.DeepEqual(first.Tasks[0].StatusKeys, []string{"backlog"}) || first.NextPageToken == "" {
		t.Fatalf("first page = %+v, want Bravo backlog/open with next token", first)
	}
	secondRaw, _ := runWorkflowRootCommandOK(t, "task", "list", "--project", binding.ProjectID, "--status", "backlog", "--run-status", "open", "--sort", "title:desc", "--page-size", "1", "--page-token", first.NextPageToken, "--json")
	var second taskListOutput
	if err := json.Unmarshal([]byte(secondRaw), &second); err != nil {
		t.Fatalf("second page json = %q: %v", secondRaw, err)
	}
	if len(second.Tasks) != 1 || second.Tasks[0].TaskID != string(alpha.ID) || second.NextPageToken != "" {
		t.Fatalf("second page = %+v, want Alpha and no next token", second)
	}
	if strings.Contains(firstRaw+secondRaw, "Charlie") {
		t.Fatalf("filtered output includes done task: first=%q second=%q", firstRaw, secondRaw)
	}
}
