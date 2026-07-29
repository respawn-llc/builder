package workflowview

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestNewTaskSearchRequiresCurrentNodeAuthority(t *testing.T) {
	if _, err := NewTaskSearch(nil, NewTaskProjector(), nil); err == nil {
		t.Fatal("NewTaskSearch accepted absent metadata and authority")
	}
	store, err := metadata.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := NewTaskSearch(store, nil, nil); err == nil {
		t.Fatal("NewTaskSearch accepted absent projector and authority")
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	t.Cleanup(func() { _ = authority.Close(context.Background()) })
	if _, err := NewTaskSearch(store, NewTaskProjector(), authority); err != nil {
		t.Fatalf("NewTaskSearch with Current Node authority: %v", err)
	}
}

func TestTaskSearchFindsAndFiltersCanonicalTaskSources(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	first := createTaskSearchTask(t, fixture, "needle title", "needle body needle")
	comment, err := fixture.store.AddComment(fixture.ctx, first.ID, "needle comment", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	otherBinding, err := fixture.metadata.RegisterWorkspaceBinding(fixture.ctx, t.TempDir())
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if _, err := fixture.store.LinkWorkflow(fixture.ctx, otherBinding.ProjectID, fixture.workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow second project: %v", err)
	}
	if _, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
		ProjectID:  otherBinding.ProjectID,
		WorkflowID: &fixture.workflowID,
		Title:      "needle elsewhere",
		Body:       "other",
	}); err != nil {
		t.Fatalf("CreateTask second project: %v", err)
	}

	response, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeLiteral,
		Query:           "needle",
		Context:         serverapi.TaskSearchDefaultContext,
		ProjectIDs:      []string{fixture.binding.ProjectID},
		StatusKinds:     []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(first.ID) {
		t.Fatalf("filtered search response = %+v", response)
	}
	group := response.Groups[0]
	if group.Status.Kind != serverapi.WorkflowTaskStatusKindBacklog ||
		group.TotalHitCount != 4 ||
		len(group.Hits) != 4 ||
		group.Hits[0].Source.Kind != serverapi.TaskSearchSourceKindTitle ||
		group.Hits[1].Source.Kind != serverapi.TaskSearchSourceKindBody ||
		group.Hits[2].Source.Kind != serverapi.TaskSearchSourceKindBody ||
		group.Hits[3].Source.Kind != serverapi.TaskSearchSourceKindComment ||
		group.Hits[3].Source.CommentID == nil ||
		*group.Hits[3].Source.CommentID != comment.ID {
		t.Fatalf("filtered search group = %+v", group)
	}

	withoutComments, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "comment:needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search without Comments: %v", err)
	}
	if len(withoutComments.Groups) != 0 {
		t.Fatalf("Comment-excluded search = %+v", withoutComments)
	}
}

func TestTaskSearchReflectsTaskAndCommentMutationsImmediately(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	task := createTaskSearchTask(t, fixture, "Mutation", "needle body")
	request := taskSearchRequest("needle")
	assertTaskSearchTask(t, fixture.ctx, search, request, task.ID, serverapi.WorkflowTaskStatusKindBacklog)

	replacement := "replacement body"
	if _, err := fixture.store.UpdateTask(fixture.ctx, workflowstore.UpdateTaskRequest{TaskID: task.ID, Body: &replacement}); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	assertTaskSearchEmpty(t, fixture.ctx, search, request)

	comment, err := fixture.store.AddComment(fixture.ctx, task.ID, "needle comment", "user", "user-1")
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	request.IncludeComments = true
	response, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("Search after comment create: %v", err)
	}
	if len(response.Groups) != 1 || len(response.Groups[0].Hits) != 1 || response.Groups[0].Hits[0].Source.Kind != serverapi.TaskSearchSourceKindComment {
		t.Fatalf("search after Comment create = %+v", response)
	}
	if err := fixture.store.DeleteComment(fixture.ctx, comment.ID); err != nil {
		t.Fatalf("DeleteComment: %v", err)
	}
	assertTaskSearchEmpty(t, fixture.ctx, search, request)
}

func TestTaskSearchSearchKeepsLiteralResponseCoherentDuringCanonicalMutation(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	task := createTaskSearchTask(t, fixture, "Snapshot", "before needle after")
	request := taskSearchRequest("needle")
	started := make(chan struct{})
	mutationsDone := make(chan error, 1)
	go func() {
		close(started)
		for index := range 128 {
			body := "replacement"
			if index%2 == 0 {
				body = "before needle after"
			}
			if _, err := fixture.store.UpdateTask(fixture.ctx, workflowstore.UpdateTaskRequest{
				TaskID: task.ID,
				Body:   &body,
			}); err != nil {
				mutationsDone <- err
				return
			}
		}
		mutationsDone <- nil
	}()
	<-started
	for index := range 128 {
		response, err := search.Search(fixture.ctx, request)
		if err != nil {
			t.Fatalf("Search during canonical mutation %d: %v", index, err)
		}
		if len(response.Groups) == 0 {
			continue
		}
		if len(response.Groups) != 1 ||
			response.Groups[0].TaskID != string(task.ID) ||
			len(response.Groups[0].Hits) != 1 ||
			response.Groups[0].Hits[0].Literal == nil ||
			response.Groups[0].Hits[0].Literal.Match != "needle" {
			t.Fatalf("Search during canonical mutation %d = %+v, want a complete pre-mutation hit or no match", index, response)
		}
	}
	if err := <-mutationsDone; err != nil {
		t.Fatalf("canonical mutation: %v", err)
	}
}

func TestTaskSearchRawFTS5UsesMarkerFreeKnownColumnSnippets(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	task := createTaskSearchTask(
		t,
		fixture,
		"Different title",
		strings.Repeat("prefix ", 40)+"needle "+strings.Repeat("suffix ", 40),
	)
	response, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "body:needle",
		Context:  2,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw Search: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(task.ID) {
		t.Fatalf("raw search response = %+v", response)
	}
	hits := response.Groups[0].Hits
	if len(hits) != 1 || hits[0].Source.Kind != serverapi.TaskSearchSourceKindBody || hits[0].FTS5 == nil || hits[0].FTS5.Snippet != "eedl" {
		t.Fatalf("raw search hits = %+v", hits)
	}
}

func TestTaskSearchPaginatesAndPinsCursors(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	first := createTaskSearchTask(t, fixture, "First", "needle needle")
	second := createTaskSearchTask(t, fixture, "Second", "needle")
	request := taskSearchRequest("needle")
	request.PageSize = 1

	page, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(page.Groups) != 1 || len(page.Groups[0].Hits) != 1 || page.NextPageToken == nil {
		t.Fatalf("first page = %+v", page)
	}
	firstTaskID := page.Groups[0].TaskID
	request.PageToken = page.NextPageToken
	page, err = search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page.Groups) != 1 || len(page.Groups[0].Hits) != 1 || page.NextPageToken == nil {
		t.Fatalf("second page = %+v", page)
	}
	request.PageToken = page.NextPageToken
	page, err = search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(page.Groups) != 1 || len(page.Groups[0].Hits) != 1 || page.NextPageToken != nil {
		t.Fatalf("third page = %+v", page)
	}
	if firstTaskID != string(first.ID) && firstTaskID != string(second.ID) {
		t.Fatalf("first page task = %q, want a matching Task", firstTaskID)
	}

	token := *request.PageToken
	_ = token
	fingerprint, err := taskSearchRequestFingerprint(taskSearchRequest("needle"))
	if err != nil {
		t.Fatalf("taskSearchRequestFingerprint: %v", err)
	}
	raw, err := encodeTaskSearchPageToken(taskSearchPageToken{
		Version:     taskSearchPageTokenVersion,
		Fingerprint: fingerprint,
		Ordinal:     1,
		RankBits:    math.Float64bits(math.NaN()),
		TaskID:      string(first.ID),
	})
	if err != nil {
		t.Fatalf("encodeTaskSearchPageToken: %v", err)
	}
	_, _, err = parseTaskSearchPageToken(&raw, fingerprint)
	var searchErr *serverapi.TaskSearchError
	if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonInvalidCursor {
		t.Fatalf("non-finite cursor error = %v", err)
	}
}

func TestTaskSearchRawSchemaFailuresRemainOperational(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*metadata.Store) error
	}{
		{
			name: "missing FTS",
			mutate: func(store *metadata.Store) error {
				_, err := store.DB().Exec("DROP TABLE task_search_fts")
				return err
			},
		},
		{
			name: "ordinary same-name table",
			mutate: func(store *metadata.Store) error {
				if _, err := store.DB().Exec("DROP TABLE task_search_fts"); err != nil {
					return err
				}
				_, err := store.DB().Exec("CREATE TABLE task_search_fts (title TEXT, body TEXT, comment TEXT)")
				return err
			},
		},
		{
			name: "incomplete content view",
			mutate: func(store *metadata.Store) error {
				if _, err := store.DB().Exec("DROP VIEW task_search_content"); err != nil {
					return err
				}
				_, err := store.DB().Exec("CREATE VIEW task_search_content AS SELECT document_id, NULL AS title, NULL AS body FROM task_search_documents")
				return err
			},
		},
		{
			name: "nonpartial mapping index",
			mutate: func(store *metadata.Store) error {
				if _, err := store.DB().Exec("DROP INDEX task_search_documents_task_title_unique"); err != nil {
					return err
				}
				_, err := store.DB().Exec("CREATE INDEX task_search_documents_task_title_unique ON task_search_documents(task_id)")
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, search := newTaskSearchFixture(t, false)
			createTaskSearchTask(t, fixture, "Schema contract", "needle")
			if err := test.mutate(fixture.metadata); err != nil {
				t.Fatalf("mutate schema: %v", err)
			}
			_, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
				Mode:     serverapi.TaskSearchModeFTS5,
				Query:    `"`,
				Context:  serverapi.TaskSearchDefaultContext,
				PageSize: serverapi.TaskSearchDefaultPageSize,
			})
			var searchErr *serverapi.TaskSearchError
			if err == nil || errors.As(err, &searchErr) {
				t.Fatalf("schema failure error = %T %v", err, err)
			}
		})
	}
}

func TestTaskSearchRawFTS5SQLiteErrorsRemainOperational(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	_, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    `"`,
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	var searchErr *serverapi.TaskSearchError
	if err == nil || errors.As(err, &searchErr) {
		t.Fatalf("raw FTS5 SQLite error = %T %v, want a generic operational error", err, err)
	}
}

func TestTaskSearchSearchDoesNotPreflightEntirePersistenceCorpus(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	healthy := createTaskSearchTask(t, fixture, "Healthy", "healthy needle")
	if _, err := fixture.metadata.DB().Exec(`DROP TRIGGER task_search_task_insert`); err != nil {
		t.Fatalf("drop Task insert trigger: %v", err)
	}
	if _, err := fixture.metadata.DB().Exec(`
CREATE TRIGGER task_search_task_insert
AFTER INSERT ON tasks
BEGIN
    SELECT 1;
END`); err != nil {
		t.Fatalf("create no-op Task insert trigger: %v", err)
	}
	for index := range 64 {
		createTaskSearchTask(t, fixture, fmt.Sprintf("Drift %d", index), "unrelated")
	}
	response, err := search.Search(fixture.ctx, taskSearchRequest("healthy needle"))
	if err != nil {
		t.Fatalf("Search across persistence drift: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(healthy.ID) {
		t.Fatalf("Search across persistence drift = %+v, want the valid indexed Task", response)
	}
}

func TestTaskSearchFiltersDurableCurrentNodeStatuses(t *testing.T) {
	tests := []struct {
		name             string
		requiresApproval bool
		prepare          func(t *testing.T, fixture currentNodeViewFixture, task workflowstore.TaskRecord)
		want             serverapi.WorkflowTaskStatusKind
	}{
		{
			name: "backlog",
			want: serverapi.WorkflowTaskStatusKindBacklog,
		},
		{
			name: "active",
			prepare: func(t *testing.T, fixture currentNodeViewFixture, task workflowstore.TaskRecord) {
				startTaskSearchTask(t, fixture, task)
			},
			want: serverapi.WorkflowTaskStatusKindActive,
		},
		{
			name: "interrupted",
			prepare: func(t *testing.T, fixture currentNodeViewFixture, task workflowstore.TaskRecord) {
				started := startTaskSearchTask(t, fixture, task)
				if err := fixture.store.InterruptCurrentNode(
					fixture.ctx,
					started.currentNode,
					workflow.CurrentNodeInterruptionReason("server_restart"),
					workflow.CurrentNodeInterruptionDetail{Code: "restart"},
				); err != nil {
					t.Fatalf("InterruptCurrentNode: %v", err)
				}
			},
			want: serverapi.WorkflowTaskStatusKindInterrupted,
		},
		{
			name:             "waiting approval",
			requiresApproval: true,
			prepare: func(t *testing.T, fixture currentNodeViewFixture, task workflowstore.TaskRecord) {
				started := startTaskSearchTask(t, fixture, task)
				if _, err := fixture.store.CompleteCurrentNode(fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
					Source:       started.currentNode,
					TransitionID: "done",
				}); err != nil {
					t.Fatalf("CompleteCurrentNode: %v", err)
				}
			},
			want: serverapi.WorkflowTaskStatusKindWaitingApproval,
		},
		{
			name: "done",
			prepare: func(t *testing.T, fixture currentNodeViewFixture, task workflowstore.TaskRecord) {
				started := startTaskSearchTask(t, fixture, task)
				if _, err := fixture.store.CompleteCurrentNode(fixture.ctx, workflowstore.CurrentNodeCompletionRequest{
					Source:       started.currentNode,
					TransitionID: "done",
				}); err != nil {
					t.Fatalf("CompleteCurrentNode: %v", err)
				}
			},
			want: serverapi.WorkflowTaskStatusKindDone,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, search := newTaskSearchFixture(t, test.requiresApproval)
			task := createTaskSearchTask(t, fixture, test.name, "needle")
			if test.prepare != nil {
				test.prepare(t, fixture, task)
			}
			request := taskSearchRequest("needle")
			request.StatusKinds = []serverapi.WorkflowTaskStatusKind{test.want}
			assertTaskSearchTask(t, fixture.ctx, search, request, task.ID, test.want)
		})
	}
}

func TestTaskSearchFiltersQueuedAndRunningCurrentNodeExecutions(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh is unavailable: %v", err)
	}
	fixture, search := newTaskSearchFixture(t, false)
	queued := startTaskSearchTask(t, fixture, createTaskSearchTask(t, fixture, "Queued", "needle"))
	running := startTaskSearchTask(t, fixture, createTaskSearchTask(t, fixture, "Running", "needle"))
	queuedLease := newTaskSearchLease(t, fixture, queued)
	runningLease := newTaskSearchLease(t, fixture, running)
	queuedHandle := startTaskSearchScript(t, fixture, shellPath, &queuedLease)
	runningHandle := startTaskSearchScript(t, fixture, shellPath, &runningLease)
	t.Cleanup(func() {
		queuedLease.Cancel()
		runningHandle.RequestStop()
		_, _ = queuedHandle.Wait(context.Background())
		_, _ = runningHandle.Wait(context.Background())
	})
	runningLease.Release()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshots, snapshotErr := fixture.authority.CurrentProjectWorkflowTaskExecutionSnapshots(fixture.binding.ProjectID, fixture.workflowID)
		if snapshotErr != nil {
			return false
		}
		return len(snapshots[queued.task.ID].Executions) == 1 &&
			snapshots[queued.task.ID].Executions[0].Queued &&
			len(snapshots[running.task.ID].Executions) == 1 &&
			!snapshots[running.task.ID].Executions[0].Queued
	}, "timed out waiting for queued and running Current Node executions")
	for _, test := range []struct {
		task workflowstore.TaskRecord
		kind serverapi.WorkflowTaskStatusKind
	}{
		{task: queued.task, kind: serverapi.WorkflowTaskStatusKindQueued},
		{task: running.task, kind: serverapi.WorkflowTaskStatusKindRunning},
	} {
		request := taskSearchRequest("needle")
		request.StatusKinds = []serverapi.WorkflowTaskStatusKind{test.kind}
		assertTaskSearchTask(t, fixture.ctx, search, request, test.task.ID, test.kind)
	}
}

func TestTaskSearchFiltersWaitingQuestionCurrentNodeExecution(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	task := createTaskSearchTask(t, fixture, "Question", "needle")
	question := fixture.startCurrentNodeQuestion(t, startTaskSearchTask(t, fixture, task))
	search, err := NewTaskSearch(fixture.metadata, NewTaskProjector(), question.authority)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	request := taskSearchRequest("needle")
	request.StatusKinds = []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindWaitingQuestion}
	assertTaskSearchTask(t, fixture.ctx, search, request, task.ID, serverapi.WorkflowTaskStatusKindWaitingQuestion)
	question.resolve(t, fixture.ctx)
}

func TestTaskSearchRanksEquivalentBodyBeforeComment(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	bodyTask := createTaskSearchTask(t, fixture, "Body", "needle")
	commentTask := createTaskSearchTask(t, fixture, "Comment", "other")
	if _, err := fixture.store.AddComment(fixture.ctx, commentTask.ID, "needle", "user", "user-1"); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	response, err := search.Search(fixture.ctx, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "needle",
		Context:         serverapi.TaskSearchDefaultContext,
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Groups) != 2 ||
		response.Groups[0].TaskID != string(bodyTask.ID) ||
		response.Groups[1].TaskID != string(commentTask.ID) {
		t.Fatalf("ranked groups = %+v", response.Groups)
	}
}

func newTaskSearchFixture(t *testing.T, requiresApproval bool) (currentNodeViewFixture, *TaskSearch) {
	t.Helper()
	fixture := newCurrentNodeViewFixture(t, requiresApproval)
	search, err := NewTaskSearch(fixture.metadata, NewTaskProjector(), fixture.authority)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	return fixture, search
}

func createTaskSearchTask(t *testing.T, fixture currentNodeViewFixture, title string, body string) workflowstore.TaskRecord {
	t.Helper()
	task, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: &fixture.workflowID,
		Title:      title,
		Body:       body,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func startTaskSearchTask(t *testing.T, fixture currentNodeViewFixture, task workflowstore.TaskRecord) startedCurrentNodeViewTask {
	t.Helper()
	started, err := fixture.store.StartTask(fixture.ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v", started.Mutation)
	}
	return startedCurrentNodeViewTask{task: task, currentNode: started.Mutation.Created[0].Reference}
}

func newTaskSearchLease(t *testing.T, fixture currentNodeViewFixture, started startedCurrentNodeViewTask) sessionruntime.WorkflowExecutionLease {
	t.Helper()
	lease, err := fixture.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{
		ProjectID:   fixture.binding.ProjectID,
		WorkflowID:  fixture.workflowID,
		CurrentNode: started.currentNode,
	})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	return lease
}

func startTaskSearchScript(t *testing.T, fixture currentNodeViewFixture, shellPath string, lease *sessionruntime.WorkflowExecutionLease) sessionruntime.ExecutionHandle {
	t.Helper()
	handle, err := fixture.authority.StartScriptExecution(fixture.ctx, sessionruntime.ScriptExecutionRequest{
		Workflow: lease,
		Command: sessionruntime.ScriptCommand{
			Path: shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	return handle
}

func taskSearchRequest(query string) serverapi.TaskSearchRequest {
	return serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    query,
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	}
}

func assertTaskSearchTask(t *testing.T, ctx context.Context, search *TaskSearch, request serverapi.TaskSearchRequest, taskID workflow.TaskID, kind serverapi.WorkflowTaskStatusKind) {
	t.Helper()
	response, err := search.Search(ctx, request)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Groups) != 1 ||
		response.Groups[0].TaskID != string(taskID) ||
		response.Groups[0].Status.Kind != kind {
		t.Fatalf("search response = %+v", response)
	}
}

func assertTaskSearchEmpty(t *testing.T, ctx context.Context, search *TaskSearch, request serverapi.TaskSearchRequest) {
	t.Helper()
	response, err := search.Search(ctx, request)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Groups) != 0 || response.NextPageToken != nil {
		t.Fatalf("empty search response = %+v", response)
	}
}
