package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

type pagedTaskListRemote struct {
	client.WorkflowClient
	board    serverapi.WorkflowBoard
	pages    map[string]serverapi.WorkflowBoard
	requests []serverapi.WorkflowTaskListRequest
}

func (r *pagedTaskListRemote) Close() error { return nil }

func (r *pagedTaskListRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *pagedTaskListRemote) GetWorkflowBoard(_ context.Context, req serverapi.WorkflowBoardRequest) (serverapi.WorkflowBoardResponse, error) {
	if strings.TrimSpace(req.PageToken) == "" {
		return serverapi.WorkflowBoardResponse{Board: r.board}, nil
	}
	return serverapi.WorkflowBoardResponse{Board: r.pages[req.PageToken]}, nil
}

func (r *pagedTaskListRemote) ListWorkflowTasks(_ context.Context, req serverapi.WorkflowTaskListRequest) (serverapi.WorkflowTaskListResponse, error) {
	r.requests = append(r.requests, req)
	board := r.board
	if strings.TrimSpace(req.PageToken) != "" {
		board = r.pages[req.PageToken]
	}
	return workflowTaskListResponseFromBoard(board), nil
}

func workflowTaskListResponseFromBoard(board serverapi.WorkflowBoard) serverapi.WorkflowTaskListResponse {
	cards := append([]serverapi.WorkflowBoardTaskCard{}, board.Cards...)
	cards = append(cards, board.DonePreview...)
	tasks := make([]serverapi.WorkflowTaskListItem, 0, len(cards))
	for _, card := range cards {
		runStatus := serverapi.WorkflowTaskRunStatus(testTaskListRunStatusFromCardStatus(card.Status))
		statusKey := "agent"
		if card.Status.Kind == "done" {
			statusKey = "done"
		}
		if card.Status.Kind == "backlog" {
			statusKey = "backlog"
		}
		tasks = append(tasks, serverapi.WorkflowTaskListItem{
			TaskID:          card.TaskID,
			ShortID:         card.ShortID,
			WorkflowID:      card.WorkflowID,
			Title:           card.Title,
			CreatedAtUnixMs: 1,
			UpdatedAtUnixMs: card.UpdatedAtUnixMs,
			StatusKeys:      []string{statusKey},
			RunStatus:       runStatus,
			RunCount:        len(card.Status.RunIDs),
		})
	}
	return serverapi.WorkflowTaskListResponse{ProjectID: board.ProjectID, WorkflowID: board.SelectedWorkflow.WorkflowID, SelectedWorkflow: &board.SelectedWorkflow, NextPageToken: board.NextPageToken, Tasks: tasks}
}

func testTaskListRunStatusFromCardStatus(status serverapi.WorkflowTaskStatus) string {
	switch status.Kind {
	case "done":
		return "done"
	case "canceled":
		return "canceled"
	case "interrupted":
		return "interrupted"
	case "running", "waiting_question", "waiting_approval":
		return "running"
	default:
		return "open"
	}
}

func TestTaskListUsesDefaultPageSizeAndPrintsNextPageToken(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &pagedTaskListRemote{
		board: serverapi.WorkflowBoard{
			ProjectID:        "project-1",
			SelectedWorkflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1"},
			Cards:            []serverapi.WorkflowBoardTaskCard{testTaskCard("task-a", "BLD-1", "A")},
			NextPageToken:    "next",
		},
		pages: map[string]serverapi.WorkflowBoard{
			"next": {
				ProjectID:        "project-1",
				SelectedWorkflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1"},
				Cards: []serverapi.WorkflowBoardTaskCard{
					testTaskCard("task-b", "BLD-2", "B"),
					testTaskCard("task-a", "BLD-1", "A"),
				},
			},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--project", "project-1")
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	if strings.Count(stdout, "BLD-1:") != 1 || strings.Contains(stdout, "BLD-2:") {
		t.Fatalf("task list output = %q, want only first page cards", stdout)
	}
	if strings.Contains(stdout, "short_id\t") {
		t.Fatalf("task list output = %q, want human-readable blocks without TSV header", stdout)
	}
	if !strings.Contains(stdout, "BLD-1: A.\nStatus: agent\nRun status: open\n") {
		t.Fatalf("task list output = %q, want readable status block", stdout)
	}
	if !strings.Contains(stderr, "Next page token: `next`") {
		t.Fatalf("task list stderr = %q, want next page token", stderr)
	}
	if len(remote.requests) != 1 || remote.requests[0].PageSize != 100 || remote.requests[0].PageToken != "" || !reflect.DeepEqual(remote.requests[0].Sort, defaultTaskListSortSelectors()) {
		t.Fatalf("task list requests = %+v, want one default-sized first page request with default sort", remote.requests)
	}
}

func TestTaskListUsesRequestedPageSizeAndToken(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &pagedTaskListRemote{
		board: serverapi.WorkflowBoard{
			ProjectID:        "project-1",
			SelectedWorkflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1"},
			Cards:            []serverapi.WorkflowBoardTaskCard{testTaskCard("task-a", "BLD-1", "A")},
			NextPageToken:    "next",
		},
		pages: map[string]serverapi.WorkflowBoard{
			"next": {
				ProjectID:        "project-1",
				SelectedWorkflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1"},
				Cards:            []serverapi.WorkflowBoardTaskCard{testTaskCard("task-b", "BLD-2", "B")},
				DonePreview:      []serverapi.WorkflowBoardTaskCard{testDoneTaskCard("task-c", "BLD-3", "C")},
			},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--project", "project-1", "--page-size", "1", "--page-token", "next")
	if code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, stderr)
	}
	// The requested open page holds BLD-2; BLD-1 is on the earlier page. Because
	// the open stream is exhausted (no next token), the bounded done preview is
	// surfaced so done tasks stay reachable even though open cards filled the page.
	if strings.Contains(stdout, "BLD-1:") || strings.Count(stdout, "BLD-2:") != 1 || strings.Count(stdout, "BLD-3:") != 1 {
		t.Fatalf("task list output = %q, want the open page plus the surfaced done preview", stdout)
	}
	if !strings.Contains(stdout, "BLD-2: B.\nStatus: agent\nRun status: open\n") {
		t.Fatalf("task list output = %q, want readable status block", stdout)
	}
	if !strings.Contains(stdout, "BLD-3: C.\nStatus: done\nRun status: done\n") {
		t.Fatalf("task list output = %q, want surfaced done task", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("task list stderr = %q, want no next page token", stderr)
	}
	if len(remote.requests) != 1 || remote.requests[0].PageSize != 1 || remote.requests[0].PageToken != "next" {
		t.Fatalf("task list requests = %+v, want requested page size and token", remote.requests)
	}
}

func TestTaskListJSONOutputsStructuredPage(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &pagedTaskListRemote{
		board: serverapi.WorkflowBoard{
			ProjectID:        "project-1",
			SelectedWorkflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1"},
			Cards: []serverapi.WorkflowBoardTaskCard{{
				TaskID:        "task-a",
				ShortID:       "BLD-1",
				WorkflowID:    "workflow-1",
				Title:         "A",
				ActiveNodeIDs: []string{"node-1"},
				Status:        serverapi.WorkflowTaskStatus{Kind: "running", RunIDs: []string{"run-1"}},
			}},
			NextPageToken: "next",
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "list", "--project", "project-1", "--json")
	if code != 0 {
		t.Fatalf("task list --json exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("task list --json stderr = %q, want empty stderr on success", stderr)
	}
	var output taskListOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("task list --json output = %q, want JSON: %v", stdout, err)
	}
	if output.ProjectID != "project-1" || output.NextPageToken != "next" {
		t.Fatalf("task list --json output = %+v, want project and next page token", output)
	}
	if len(output.Tasks) != 1 || output.Tasks[0].TaskID != "task-a" || output.Tasks[0].Status != "running" || output.Tasks[0].RunStatus != "running" || !reflect.DeepEqual(output.Tasks[0].StatusKeys, []string{"agent"}) || output.Tasks[0].CreatedAtUnixMs == 0 {
		t.Fatalf("task list --json tasks = %+v, want structured running task-a", output.Tasks)
	}
}

func TestTaskListHelpIncludesPaginationAndJSONFlags(t *testing.T) {
	_, stderr, code := runWorkflowRootCommand("task", "list", "--help")
	if code != 0 {
		t.Fatalf("task list --help exit=%d stderr=%q", code, stderr)
	}
	for _, want := range []string{"kent task list [--project <project>]", "--status", "--column", "--run-status", "--sort", "-json", "-page-size", "-page-token"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("task list --help stderr = %q, want %q", stderr, want)
		}
	}
}

func TestTaskListProjectPathResolutionRejectsUnboundPath(t *testing.T) {
	cfg, _, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	_, stderr, code := runWorkflowRootCommand("task", "list", "--project", t.TempDir())
	if code == 0 || !strings.Contains(stderr, "workspace is not registered") {
		t.Fatalf("task list unbound path code=%d stderr=%q", code, stderr)
	}
}
