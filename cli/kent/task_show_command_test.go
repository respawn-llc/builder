package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestTaskShowHelpSmoke(t *testing.T) {
	_, stderr, code := runWorkflowRootCommand("task", "show", "--help")
	if code != 0 {
		t.Fatalf("task show --help exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("task show help output is empty")
	}
}

func TestTaskShowFindsSameProjectTaskOutsideSelectedWorkflow(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	defaultWorkflowID := createRunnableWorkflowForCommandTest(t, "Default Workflow")
	if _, linkErr, code := runWorkflowRootCommand("workflow", "link", binding.ProjectID, defaultWorkflowID, "--default"); code != 0 {
		t.Fatalf("default workflow link exit=%d stderr=%q", code, linkErr)
	}
	otherWorkflowID := createRunnableWorkflowForCommandTest(t, "Other Workflow")
	if _, linkErr, code := runWorkflowRootCommand("workflow", "link", binding.ProjectID, otherWorkflowID); code != 0 {
		t.Fatalf("other workflow link exit=%d stderr=%q", code, linkErr)
	}
	taskOut, taskErr, code := runWorkflowRootCommand("task", "create", "--title", "Other Task", "--body", "Body", "--workflow", otherWorkflowID, "--project", binding.ProjectID)
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)
	showOut, showErr, code := runWorkflowRootCommand("task", "show", "--project", binding.ProjectID, shortID)
	if code != 0 {
		t.Fatalf("task show exit=%d stderr=%q", code, showErr)
	}
	if !strings.Contains(showOut, shortID+": Other Task\n") {
		t.Fatalf("task show output = %q, want task short id %s", showOut, shortID)
	}
	if strings.Contains(showOut, "Note:") {
		t.Fatalf("task show output = %q, did not expect cross-project note", showOut)
	}
}

func TestTaskShowUsesRegisteredTaskWorktreeRootAsCurrentProject(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	worktreeRoot := t.TempDir()
	worktreeCfg := cfg
	worktreeCfg.WorkspaceRoot = worktreeRoot
	remote.projectBindingsByRoot = map[string]serverapi.ProjectBinding{
		worktreeRoot: {
			ProjectID:     binding.ProjectID,
			ProjectKey:    binding.ProjectKey,
			ProjectName:   binding.ProjectName,
			WorkspaceID:   binding.WorkspaceID,
			CanonicalRoot: worktreeRoot,
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, worktreeCfg, remote)
	defer restore()

	workflowID := createRunnableWorkflowForCommandTest(t, "Task Worktree Workflow")
	if _, linkErr, code := runWorkflowRootCommand("workflow", "link", binding.ProjectID, workflowID, "--default"); code != 0 {
		t.Fatalf("workflow link exit=%d stderr=%q", code, linkErr)
	}
	taskOut, taskErr, code := runWorkflowRootCommand("task", "create", "--title", "Worktree Task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)

	showOut, showErr, code := runWorkflowRootCommand("task", "show", shortID)
	if code != 0 {
		t.Fatalf("task show from worktree root exit=%d stderr=%q", code, showErr)
	}
	if !strings.Contains(showOut, shortID+": Worktree Task\n") {
		t.Fatalf("task show output = %q, want task short id %s", showOut, shortID)
	}
}

func TestTaskShowWarnsWhenShortIDBelongsToAnotherKnownProject(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &crossProjectTaskShowRemote{}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "show", "--project", "project-current", "OTH-1")
	if code != 0 {
		t.Fatalf("task show exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "Note:") {
		t.Fatalf("task show output = %q, did not expect cross-project note in stdout", stdout)
	}
	if !strings.Contains(stderr, "Note: This task belongs to another project OTH") {
		t.Fatalf("task show stderr = %q, want cross-project note", stderr)
	}
	if !strings.Contains(stdout, "OTH-1: Other Task\n") {
		t.Fatalf("task show output = %q, want other task", stdout)
	}
}

func TestTaskShowFallsBackAfterRemoteScopedShortIDNotFound(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &crossProjectTaskShowRemote{scopedErr: serverapi.ErrWorkflowTaskNotFound}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "show", "--project", "project-current", "OTH-1")
	if code != 0 {
		t.Fatalf("task show exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "OTH-1: Other Task\n") {
		t.Fatalf("task show output = %q, want global fallback task", stdout)
	}
	if remote.unscopedCalls != 1 {
		t.Fatalf("unscoped calls = %d, want one fallback lookup", remote.unscopedCalls)
	}
}

func TestTaskShowSurfacesScopedShortIDLookupErrors(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &crossProjectTaskShowRemote{scopedErr: errors.New("backend unavailable")}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("task", "show", "--project", "project-current", "OTH-1")
	if code == 0 {
		t.Fatalf("task show exit=%d, want failure", code)
	}
	if !strings.Contains(stderr, "backend unavailable") {
		t.Fatalf("task show stderr = %q, want scoped lookup error", stderr)
	}
	if remote.unscopedCalls != 0 {
		t.Fatalf("unscoped calls = %d, want no fallback after scoped lookup error", remote.unscopedCalls)
	}
}

func TestTaskShowSurfacesUnscopedShortIDLookupErrors(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &crossProjectTaskShowRemote{unscopedErr: errors.New("ambiguous short id")}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("task", "show", "--project", "project-current", "OTH-1")
	if code == 0 {
		t.Fatalf("task show exit=%d, want failure", code)
	}
	if !strings.Contains(stderr, "ambiguous short id") {
		t.Fatalf("task show stderr = %q, want unscoped lookup error", stderr)
	}
	if strings.Contains(stderr, "not found") {
		t.Fatalf("task show stderr = %q, want raw unscoped lookup error", stderr)
	}
}

func createRunnableWorkflowForCommandTest(t *testing.T, name string) string {
	t.Helper()
	workflowID := workflowCreateForTest(t, name).ID
	if _, nodeErr, code := runWorkflowRootCommand("workflow", "node", "add", workflowID, "--key", "implement", "--kind", "agent", "--agent", "workflow-test", "--prompt", "Do work"); code != 0 {
		t.Fatalf("workflow node add exit=%d stderr=%q", code, nodeErr)
	}
	if _, edgeErr, code := runWorkflowRootCommand("workflow", "edge", "add", workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "implement", "--context", "new_session", "--prompt", "Do work"); code != 0 {
		t.Fatalf("workflow start edge add exit=%d stderr=%q", code, edgeErr)
	}
	if _, edgeErr, code := runWorkflowRootCommand("workflow", "edge", "add", workflowID, "--from", "implement", "--transition", "done", "--edge-key", "done", "--to", "done", "--context", "new_session"); code != 0 {
		t.Fatalf("workflow done edge add exit=%d stderr=%q", code, edgeErr)
	}
	return workflowID
}

type crossProjectTaskShowRemote struct {
	client.WorkflowClient
	scopedErr     error
	unscopedErr   error
	unscopedCalls int
}

func (r *crossProjectTaskShowRemote) Close() error { return nil }

func (r *crossProjectTaskShowRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *crossProjectTaskShowRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	if req.ProjectID == "project-current" && req.ShortID == "OTH-1" {
		if r.scopedErr != nil {
			return serverapi.WorkflowTaskGetResponse{}, r.scopedErr
		}
		return serverapi.WorkflowTaskGetResponse{}, sql.ErrNoRows
	}
	if req.ProjectID == "" && req.ShortID == "OTH-1" {
		r.unscopedCalls++
		if r.unscopedErr != nil {
			return serverapi.WorkflowTaskGetResponse{}, r.unscopedErr
		}
		return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: "task-other", ProjectID: "project-other", WorkflowID: "workflow-other", ShortID: "OTH-1", Title: "Other Task"},
			Project: serverapi.ProjectBoardProject{ProjectID: "project-other", ProjectKey: "OTH", DisplayName: "Other"},
		}}, nil
	}
	return serverapi.WorkflowTaskGetResponse{}, sql.ErrNoRows
}

func TestWriteTaskDetailIncludesParallelBranchIDs(t *testing.T) {
	var stdout bytes.Buffer
	writeTaskDetail(&stdout, serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{
			ID:              "task-1",
			ShortID:         "WOR-1",
			WorkflowID:      "workflow-1",
			Title:           "Task",
			CreatedAtUnixMs: 1735689600000,
		},
		Project:         serverapi.ProjectBoardProject{ProjectID: "project-1", DisplayName: "Project"},
		Workflow:        serverapi.WorkflowPickerItem{WorkflowID: "workflow-1", DisplayName: "Workflow"},
		Body:            "Do the work.",
		SourceWorkspace: serverapi.ProjectWorkspaceSummary{RootPath: "/workspace"},
		ManagedWorktree: &serverapi.WorktreeView{CanonicalRoot: "/workspace-task"},
		SourceURL:       "https://example.test/source",
		Status:          serverapi.WorkflowTaskStatus{Kind: "backlog"},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-1"},
			{ID: "run-2"},
		},
	})

	output := stdout.String()
	for _, want := range []string{
		"WOR-1: Task\n",
		"Body:\n```md\nDo the work.\n```\n",
		"Status: open\n",
		"Project: \"Project\" (project-1)\n",
		"Workflow: \"Workflow\" (workflow-1)\n",
		"Created at 2025-01-01T00:00:00Z UTC\n",
		"Total agent runs: 2\n",
		"Main workspace: /workspace\n",
		"Worktree: /workspace-task\n",
		"Imported from: https://example.test/source\n",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("task detail output = %q, want %q", output, want)
		}
	}
	if strings.Contains(output, "placements") || strings.Contains(output, "transitions") {
		t.Fatalf("task detail output = %q, did not expect internal placement/transition dump", output)
	}
}

func TestWriteTaskDetailComments(t *testing.T) {
	var stdout bytes.Buffer
	writeTaskDetail(&stdout, serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ShortID: "WOR-1", Title: "Task", CreatedAtUnixMs: 1735689600000},
		Project:  serverapi.ProjectBoardProject{ProjectID: "project-1", DisplayName: "Project"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1", DisplayName: "Workflow"},
		Comments: []serverapi.WorkflowTaskComment{
			{ID: "comment-old", Author: "user", Body: "old", CreatedAtUnixMs: 1735689600000},
			{ID: "comment-new", Author: "agent", AuthorID: "reviewer", Body: "new", CreatedAtUnixMs: 1735776000000},
		},
	})

	output := stdout.String()
	want := "Comments (2):\nreviewer at 2025-01-02T00:00:00Z UTC:\nnew\n---\nUser at 2025-01-01T00:00:00Z UTC:\nold\n"
	if !strings.Contains(output, want) {
		t.Fatalf("task detail output = %q, want sorted comments block %q", output, want)
	}
}

func TestWriteTaskDetailCommentOverflowPointsToCommentCommand(t *testing.T) {
	comments := make([]serverapi.WorkflowTaskComment, 10)
	for i := range comments {
		comments[i] = serverapi.WorkflowTaskComment{ID: fmt.Sprintf("comment-%d", i), Author: "user", Body: "comment", CreatedAtUnixMs: 1735689600000 + int64(i)}
	}
	var stdout bytes.Buffer
	writeTaskDetail(&stdout, serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ShortID: "WOR-1", Title: "Task", CreatedAtUnixMs: 1735689600000},
		Project:  serverapi.ProjectBoardProject{ProjectID: "project-1", DisplayName: "Project"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1", DisplayName: "Workflow"},
		Comments: comments,
	})

	output := stdout.String()
	if !strings.Contains(output, "Comments under this task: 10. `kent task comment list WOR-1` to show them.\n") {
		t.Fatalf("task detail output = %q, want comment overflow pointer", output)
	}
	if strings.Contains(output, "Comments (10):") {
		t.Fatalf("task detail output = %q, did not expect inline overflow comments", output)
	}
}
