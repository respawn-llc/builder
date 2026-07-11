package main

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

func TestTaskCommentAuthorForAddUsesUserWithoutKentSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	remote := &commentAuthorRemote{}
	got := taskCommentAuthorForAdd(context.Background(), remote, "task-1", "", false)
	if got.Kind != "user" || got.ID != "" {
		t.Fatalf("taskCommentAuthorForAdd = %+v, want user without author id", got)
	}
}

func TestTaskCommentAuthorForAddUsesWorkflowRunRole(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-workflow")
	remote := &commentAuthorRemote{task: serverapi.WorkflowTaskDetail{
		Runs: []serverapi.WorkflowRun{{SessionID: "session-workflow", Role: "code_review", NodeID: "node-review"}},
	}}
	got := taskCommentAuthorForAdd(context.Background(), remote, "task-1", "", false)
	if got.Kind != "agent" || got.ID != "code_review" {
		t.Fatalf("taskCommentAuthorForAdd = %+v, want workflow role agent", got)
	}
}

func TestTaskCommentAuthorForAddUsesWorkflowNodeWhenRoleMissing(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-workflow")
	remote := &commentAuthorRemote{task: serverapi.WorkflowTaskDetail{
		Placements: []serverapi.WorkflowPlacement{{NodeID: "node-implement", NodeKey: "implement"}},
		Runs:       []serverapi.WorkflowRun{{SessionID: "session-workflow", NodeID: "node-implement"}},
	}}
	got := taskCommentAuthorForAdd(context.Background(), remote, "task-1", "", false)
	if got.Kind != "agent" || got.ID != "Node implement agent" {
		t.Fatalf("taskCommentAuthorForAdd = %+v, want workflow node agent", got)
	}
}

func TestTaskCommentAuthorForAddUsesDeterministicCurrentWorkflowRun(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-workflow")
	remote := &commentAuthorRemote{task: serverapi.WorkflowTaskDetail{
		Status: serverapi.WorkflowTaskStatus{RunIDs: []string{"run-current"}},
		Placements: []serverapi.WorkflowPlacement{
			{NodeID: "node-old", NodeKey: "old"},
			{NodeID: "node-current", NodeKey: "current"},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-old", SessionID: "session-workflow", NodeID: "node-old", StartedAtUnixMs: optionalUnixMillis(20)},
			{ID: "run-current", SessionID: "session-workflow", NodeID: "node-current", StartedAtUnixMs: optionalUnixMillis(10)},
		},
	}}
	got := taskCommentAuthorForAdd(context.Background(), remote, "task-1", "", false)
	if got.Kind != "agent" || got.ID != "Node current agent" {
		t.Fatalf("taskCommentAuthorForAdd = %+v, want current workflow run agent", got)
	}
}

func TestTaskCommentAuthorForAddUsesLatestWorkflowRunWhenNoneCurrent(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-workflow")
	remote := &commentAuthorRemote{task: serverapi.WorkflowTaskDetail{
		Placements: []serverapi.WorkflowPlacement{
			{NodeID: "node-old", NodeKey: "old"},
			{NodeID: "node-new", NodeKey: "new"},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-old", SessionID: "session-workflow", NodeID: "node-old", StartedAtUnixMs: optionalUnixMillis(10)},
			{ID: "run-new", SessionID: "session-workflow", NodeID: "node-new", StartedAtUnixMs: optionalUnixMillis(20)},
		},
	}}
	got := taskCommentAuthorForAdd(context.Background(), remote, "task-1", "", false)
	if got.Kind != "agent" || got.ID != "Node new agent" {
		t.Fatalf("taskCommentAuthorForAdd = %+v, want latest workflow run agent", got)
	}
}

func TestTaskCommentAuthorForAddUsesSessionFallbackForNonWorkflowAgent(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-other")
	remote := &commentAuthorRemote{sessionName: "triage"}
	got := taskCommentAuthorForAdd(context.Background(), remote, "task-1", "", false)
	if got.Kind != "agent" || got.ID != "Session triage agent" {
		t.Fatalf("taskCommentAuthorForAdd = %+v, want session fallback agent", got)
	}
}

func TestTaskCommentListUsesReadablePaginatedOutput(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &commentListRemote{
		taskID: "task-1",
		comments: []serverapi.WorkflowTaskComment{
			{ID: "comment-old", TaskID: "task-1", Author: "user", Body: "old", CreatedAtUnixMs: 1735689600000},
			{ID: "comment-new", TaskID: "task-1", Author: "agent", AuthorID: "reviewer", Body: "new", CreatedAtUnixMs: 1735776000000},
			{ID: "comment-extra", TaskID: "task-1", Author: "user", Body: "extra", CreatedAtUnixMs: 1735862400000},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "comment", "list", "task-1", "--page-size", "2")
	if code != 0 {
		t.Fatalf("task comment list exit=%d stderr=%q", code, stderr)
	}
	want := "Comments (2):\nUser at 2025-01-03T00:00:00Z UTC:\nextra\n---\nreviewer at 2025-01-02T00:00:00Z UTC:\nnew\n"
	if stdout != want {
		t.Fatalf("task comment list output = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "Next page token: `2`") {
		t.Fatalf("task comment list stderr = %q, want next page token", stderr)
	}
	if len(remote.listRequests) != 1 || remote.listRequests[0].TaskID != "task-1" || remote.listRequests[0].PageSize != 2 || remote.listRequests[0].PageToken != "" {
		t.Fatalf("comment list requests = %+v, want first page request", remote.listRequests)
	}
}

func TestTaskCommentListUsesPageToken(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &commentListRemote{
		taskID: "task-1",
		comments: []serverapi.WorkflowTaskComment{
			{ID: "comment-old", TaskID: "task-1", Author: "user", Body: "old", CreatedAtUnixMs: 1735689600000},
			{ID: "comment-new", TaskID: "task-1", Author: "agent", AuthorID: "reviewer", Body: "new", CreatedAtUnixMs: 1735776000000},
			{ID: "comment-extra", TaskID: "task-1", Author: "user", Body: "extra", CreatedAtUnixMs: 1735862400000},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "comment", "list", "task-1", "--page-size", "2", "--page-token", "2")
	if code != 0 {
		t.Fatalf("task comment list exit=%d stderr=%q", code, stderr)
	}
	want := "Comments (1):\nUser at 2025-01-01T00:00:00Z UTC:\nold\n"
	if stdout != want {
		t.Fatalf("task comment list output = %q, want %q", stdout, want)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("task comment list stderr = %q, want empty", stderr)
	}
	if len(remote.listRequests) != 1 || remote.listRequests[0].PageSize != 2 || remote.listRequests[0].PageToken != "2" {
		t.Fatalf("comment list requests = %+v, want second page request", remote.listRequests)
	}
}

func TestTaskCommentsPluralListAliasUsesCommentList(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &commentListRemote{
		taskID: "task-1",
		comments: []serverapi.WorkflowTaskComment{
			{ID: "comment-1", TaskID: "task-1", Author: "agent", AuthorID: "reviewer", Body: "note", CreatedAtUnixMs: 1735689600000},
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "comments", "list", "task-1")
	if code != 0 {
		t.Fatalf("task comments list exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "reviewer at 2025-01-01T00:00:00Z UTC:\nnote\n") {
		t.Fatalf("task comments list output = %q, want comment output", stdout)
	}
	if len(remote.listRequests) != 1 || remote.listRequests[0].TaskID != "task-1" {
		t.Fatalf("comment list requests = %+v, want plural alias to route to list", remote.listRequests)
	}
}

func TestTaskCommentsPluralAddAliasUsesCommentAdd(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &commentAddRemote{taskID: "task-1"}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "comments", "add", "task-1", "--body", "note", "--author", "user")
	if code != 0 {
		t.Fatalf("task comments add exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "comment_id\tcomment-1\n") {
		t.Fatalf("task comments add output = %q, want comment id", stdout)
	}
	if len(remote.addRequests) != 1 || remote.addRequests[0].TaskID != "task-1" || remote.addRequests[0].Body != "note" {
		t.Fatalf("comment add requests = %+v, want plural alias to route to add", remote.addRequests)
	}
}

type commentAddRemote struct {
	client.WorkflowClient
	taskID      string
	addRequests []serverapi.WorkflowTaskCommentAddRequest
}

func (r *commentAddRemote) Close() error { return nil }

func (r *commentAddRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *commentAddRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{Summary: serverapi.WorkflowTaskSummary{ID: strings.TrimSpace(req.TaskID)}}}, nil
}

func (r *commentAddRemote) AddWorkflowTaskComment(_ context.Context, req serverapi.WorkflowTaskCommentAddRequest) (serverapi.WorkflowTaskCommentAddResponse, error) {
	r.addRequests = append(r.addRequests, req)
	return serverapi.WorkflowTaskCommentAddResponse{Comment: serverapi.WorkflowTaskComment{ID: "comment-1", TaskID: r.taskID, Body: req.Body, Author: req.Author, AuthorID: req.AuthorID}}, nil
}

type commentListRemote struct {
	client.WorkflowClient
	taskID       string
	comments     []serverapi.WorkflowTaskComment
	listRequests []serverapi.WorkflowTaskCommentListRequest
}

func (r *commentListRemote) Close() error { return nil }

func (r *commentListRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *commentListRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{Summary: serverapi.WorkflowTaskSummary{ID: r.taskID, ShortID: "TASK-1"}}}, nil
}

func (r *commentListRemote) ListWorkflowTaskComments(_ context.Context, req serverapi.WorkflowTaskCommentListRequest) (serverapi.WorkflowTaskCommentListResponse, error) {
	r.listRequests = append(r.listRequests, req)
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = taskCommentListDefaultPageSize
	}
	offset := 0
	if strings.TrimSpace(req.PageToken) != "" {
		parsed, err := strconv.Atoi(req.PageToken)
		if err != nil {
			return serverapi.WorkflowTaskCommentListResponse{}, err
		}
		offset = parsed
	}
	sortedComments := sortedTaskCommentsByCreatedAt(r.comments)
	if offset >= len(sortedComments) {
		return serverapi.WorkflowTaskCommentListResponse{}, nil
	}
	end := offset + pageSize
	nextPageToken := ""
	if end < len(sortedComments) {
		nextPageToken = strconv.Itoa(end)
	} else {
		end = len(sortedComments)
	}
	return serverapi.WorkflowTaskCommentListResponse{Comments: sortedComments[offset:end], NextPageToken: nextPageToken}, nil
}

type commentAuthorRemote struct {
	client.WorkflowClient
	task        serverapi.WorkflowTaskDetail
	sessionName string
}

func (r *commentAuthorRemote) Close() error { return nil }

func (r *commentAuthorRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *commentAuthorRemote) GetWorkflowTask(context.Context, serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{Task: r.task}, nil
}

func (r *commentAuthorRemote) GetSessionMainView(context.Context, serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
	return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{
		Session: clientui.RuntimeSessionView{SessionName: r.sessionName},
	}}, nil
}
