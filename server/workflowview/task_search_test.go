package workflowview

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/serverapi"

	sqlitedriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestNewTaskSearchRequiresCanonicalReadDependencies(t *testing.T) {
	if _, err := NewTaskSearch(nil, NewTaskProjector(), nil); err == nil {
		t.Fatal("NewTaskSearch accepted absent metadata/status dependencies")
	}
	store, err := metadata.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := NewTaskSearch(store, nil, nil); err == nil {
		t.Fatal("NewTaskSearch accepted absent projector/status dependencies")
	}
}

func TestTaskSearchFindsLiteralBodyThroughCanonicalSnapshot(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Search title",
		Body:      "needle in canonical body",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	search, err := NewTaskSearch(
		metadataStore,
		fixture.projector,
		fixture.statusSnapshots,
	)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	response, err := search.Search(context.Background(), serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(task.ID) {
		t.Fatalf("literal task search response = %+v", response)
	}
	if len(response.Groups[0].Hits) != 1 || response.Groups[0].Hits[0].Source.Kind != serverapi.TaskSearchSourceKindBody {
		t.Fatalf("literal task search hits = %+v", response.Groups[0].Hits)
	}
}

func TestTaskSearchProjectsKnownColumnFTS5SnippetWithoutSourcePointLoad(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	body := strings.Repeat("prefix ", 40) + "needle " + strings.Repeat("suffix ", 40)
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Different title",
		Body:      body,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	response, err := search.Search(context.Background(), serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "body:needle",
		Context:  2,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(task.ID) {
		t.Fatalf("raw task search response = %+v", response)
	}
	hits := response.Groups[0].Hits
	if len(hits) != 1 || hits[0].Source.Kind != serverapi.TaskSearchSourceKindBody || hits[0].FTS5 == nil {
		t.Fatalf("raw task search hits = %+v", hits)
	}
	snippet := hits[0].FTS5.Snippet
	if snippet == "" {
		t.Fatal("FTS5 snippet is empty")
	}
	if snippet == body {
		t.Fatalf("FTS5 snippet returned the complete source body")
	}
	if !strings.Contains(snippet, "…") {
		t.Fatalf("FTS5 snippet = %q, want the contract truncation marker", snippet)
	}
}

func TestTaskSearchAppliesProjectCommentAndStatusFilters(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link first workflow: %v", err)
	}
	otherBinding, err := metadataStore.RegisterWorkspaceBinding(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("register second project: %v", err)
	}
	if err := metadataStore.SetProjectKey(ctx, otherBinding.ProjectID, "OTH"); err != nil {
		t.Fatalf("set second project key: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, otherBinding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link second workflow: %v", err)
	}
	first, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "needle title",
		Body:      "needle body needle",
	})
	if err != nil {
		t.Fatalf("create first task: %v", err)
	}
	comment, err := workflowStore.AddComment(ctx, first.ID, "needle comment", "user", "user-1")
	if err != nil {
		t.Fatalf("add comment: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: otherBinding.ProjectID,
		Title:     "needle elsewhere",
		Body:      "other body",
	}); err != nil {
		t.Fatalf("create second task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	response, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeLiteral,
		Query:           "needle",
		Context:         serverapi.TaskSearchDefaultContext,
		ProjectIDs:      []string{binding.ProjectID},
		StatusKinds:     []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindBacklog},
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(first.ID) {
		t.Fatalf("filtered task search response = %+v", response)
	}
	group := response.Groups[0]
	if group.TotalHitCount != 4 || len(group.Hits) != 4 {
		t.Fatalf("filtered hits = %+v, want title/body/body/comment", group)
	}
	last := group.Hits[len(group.Hits)-1]
	if last.Source.Kind != serverapi.TaskSearchSourceKindComment || last.Source.CommentID == nil || *last.Source.CommentID != comment.ID {
		t.Fatalf("comment hit = %+v, want %q", last, comment.ID)
	}
}

func TestTaskSearchReflectsTaskAndCommentMutationsImmediately(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Mutation visibility",
		Body:      "needle body",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	request := serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	}
	response, err := search.Search(ctx, request)
	if err != nil {
		t.Fatalf("search initial body: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(task.ID) {
		t.Fatalf("initial search response = %+v, want task %q", response, task.ID)
	}

	bodyWithoutNeedle := "replacement body"
	if _, err := workflowStore.UpdateTask(ctx, workflowstore.UpdateTaskRequest{TaskID: task.ID, Body: &bodyWithoutNeedle}); err != nil {
		t.Fatalf("replace task body: %v", err)
	}
	response, err = search.Search(ctx, request)
	if err != nil {
		t.Fatalf("search after task body replacement: %v", err)
	}
	if len(response.Groups) != 0 {
		t.Fatalf("search after task body replacement = %+v, want no matches", response)
	}

	comment, err := workflowStore.AddComment(ctx, task.ID, "needle comment", "user", "user-1")
	if err != nil {
		t.Fatalf("add matching comment: %v", err)
	}
	request.IncludeComments = true
	response, err = search.Search(ctx, request)
	if err != nil {
		t.Fatalf("search after comment create: %v", err)
	}
	if len(response.Groups) != 1 ||
		len(response.Groups[0].Hits) != 1 ||
		response.Groups[0].Hits[0].Source.Kind != serverapi.TaskSearchSourceKindComment {
		t.Fatalf("search after comment create = %+v, want one Comment hit", response)
	}
	if err := workflowStore.DeleteComment(ctx, comment.ID); err != nil {
		t.Fatalf("delete matching comment: %v", err)
	}
	response, err = search.Search(ctx, request)
	if err != nil {
		t.Fatalf("search after comment delete: %v", err)
	}
	if len(response.Groups) != 0 {
		t.Fatalf("search after comment delete = %+v, want no matches", response)
	}
}

func TestTaskSearchRawKeepsTermsWithinSourcesAndHonorsCommentInclusion(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "needle title",
		Body:      "needle body",
	})
	if err != nil {
		t.Fatalf("create searchable Task: %v", err)
	}
	if _, err := workflowStore.AddComment(ctx, task.ID, "needle comment", "user", "user-1"); err != nil {
		t.Fatalf("add searchable Comment: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "alphaone",
		Body:      "betatwo",
	}); err != nil {
		t.Fatalf("create split-term Task: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "ab",
		Body:      "short raw term fixture",
	}); err != nil {
		t.Fatalf("create short raw term Task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	response, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "needle",
		Context:         serverapi.TaskSearchDefaultContext,
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw Search: %v", err)
	}
	if len(response.Groups) != 1 || response.Groups[0].TaskID != string(task.ID) {
		t.Fatalf("raw response = %+v", response)
	}
	hits := response.Groups[0].Hits
	if len(hits) != 3 ||
		hits[0].Source.Kind != serverapi.TaskSearchSourceKindTitle ||
		hits[1].Source.Kind != serverapi.TaskSearchSourceKindBody ||
		hits[2].Source.Kind != serverapi.TaskSearchSourceKindComment {
		t.Fatalf("raw source order = %+v, want title/body/comment", hits)
	}
	withoutComments, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "comment:needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw Comment-only Search without inclusion: %v", err)
	}
	if len(withoutComments.Groups) != 0 {
		t.Fatalf("raw Comment-only Search without inclusion = %+v, want no matches", withoutComments)
	}
	splitTerms, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "alphaone betatwo",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw split-term Search: %v", err)
	}
	if len(splitTerms.Groups) != 0 {
		t.Fatalf("raw split-term Search = %+v, want no matches", splitTerms)
	}

	for _, test := range []struct {
		name            string
		query           string
		includeComments bool
		wantKind        serverapi.TaskSearchSourceKind
	}{
		{
			name:     "exact title column",
			query:    "title:needle",
			wantKind: serverapi.TaskSearchSourceKindTitle,
		},
		{
			name:     "exact body column",
			query:    "body:needle",
			wantKind: serverapi.TaskSearchSourceKindBody,
		},
		{
			name:            "exact comment column",
			query:           "comment:needle",
			includeComments: true,
			wantKind:        serverapi.TaskSearchSourceKindComment,
		},
		{
			name:     "phrase stays within body source",
			query:    `body:"needle body"`,
			wantKind: serverapi.TaskSearchSourceKindBody,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			filtered, err := search.Search(ctx, serverapi.TaskSearchRequest{
				Mode:            serverapi.TaskSearchModeFTS5,
				Query:           test.query,
				Context:         serverapi.TaskSearchDefaultContext,
				IncludeComments: test.includeComments,
				PageSize:        serverapi.TaskSearchDefaultPageSize,
			})
			if err != nil {
				t.Fatalf("raw Search: %v", err)
			}
			if len(filtered.Groups) != 1 ||
				filtered.Groups[0].TaskID != string(task.ID) ||
				len(filtered.Groups[0].Hits) != 1 ||
				filtered.Groups[0].Hits[0].Source.Kind != test.wantKind {
				t.Fatalf("raw %s response = %+v", test.name, filtered)
			}
		})
	}

	boolean, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "title:needle OR comment:needle",
		Context:         serverapi.TaskSearchDefaultContext,
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw boolean Search: %v", err)
	}
	if len(boolean.Groups) != 1 || len(boolean.Groups[0].Hits) != 2 ||
		boolean.Groups[0].Hits[0].Source.Kind != serverapi.TaskSearchSourceKindTitle ||
		boolean.Groups[0].Hits[1].Source.Kind != serverapi.TaskSearchSourceKindComment {
		t.Fatalf("raw boolean response = %+v, want title then Comment", boolean)
	}

	for _, rawTerm := range []string{"a", "ab"} {
		if _, err := search.Search(ctx, serverapi.TaskSearchRequest{
			Mode:     serverapi.TaskSearchModeFTS5,
			Query:    rawTerm,
			Context:  serverapi.TaskSearchDefaultContext,
			PageSize: serverapi.TaskSearchDefaultPageSize,
		}); err != nil {
			t.Fatalf("short raw term %q was rejected: %v", rawTerm, err)
		}
	}
}

func TestTaskSearchCursorContinuesAbsoluteOccurrences(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "No match",
		Body:      "needle first needle second",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	request := serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: 1,
	}
	firstPage, err := search.Search(ctx, request)
	if err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if firstPage.NextPageToken == nil || len(firstPage.Groups) != 1 || firstPage.Groups[0].TaskID != string(task.ID) {
		t.Fatalf("first search page = %+v", firstPage)
	}
	firstHit := firstPage.Groups[0].Hits[0]
	request.PageToken = firstPage.NextPageToken
	secondPage, err := search.Search(ctx, request)
	if err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if len(secondPage.Groups) != 1 || secondPage.Groups[0].TaskID != string(task.ID) || len(secondPage.Groups[0].Hits) != 1 {
		t.Fatalf("second search page = %+v", secondPage)
	}
	secondHit := secondPage.Groups[0].Hits[0]
	if firstHit.Ordinal != 1 || secondHit.Ordinal != 2 {
		t.Fatalf("page hit ordinals = %d, %d; want 1, 2", firstHit.Ordinal, secondHit.Ordinal)
	}
	if secondPage.NextPageToken != nil {
		t.Fatalf("second page unexpectedly continued: %+v", secondPage)
	}
}

func TestTaskSearchPaginatesBreadthFirstAcrossTasks(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	titleTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "needle title",
		Body:      "different body",
	})
	if err != nil {
		t.Fatalf("create title Task: %v", err)
	}
	bodyTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "body Task",
		Body:      "needle first needle second",
	})
	if err != nil {
		t.Fatalf("create body Task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	request := serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: 2,
	}
	first, err := search.Search(ctx, request)
	if err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if len(first.Groups) != 2 || first.NextPageToken == nil {
		t.Fatalf("first breadth-first page = %+v", first)
	}
	if first.Groups[0].TaskID != string(titleTask.ID) ||
		len(first.Groups[0].Hits) != 1 ||
		first.Groups[0].Hits[0].Ordinal != 1 ||
		first.Groups[1].TaskID != string(bodyTask.ID) ||
		len(first.Groups[1].Hits) != 1 ||
		first.Groups[1].Hits[0].Ordinal != 1 {
		t.Fatalf("first breadth-first page = %+v, want first hit for each Task", first)
	}
	request.PageToken = first.NextPageToken
	second, err := search.Search(ctx, request)
	if err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if len(second.Groups) != 1 ||
		second.Groups[0].TaskID != string(bodyTask.ID) ||
		len(second.Groups[0].Hits) != 1 ||
		second.Groups[0].Hits[0].Ordinal != 2 ||
		second.NextPageToken != nil {
		t.Fatalf("second breadth-first page = %+v, want body Task's absolute second hit", second)
	}
}

func TestTaskSearchDistinguishesEmptyResultsUnknownScopeAndCancellation(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "needle Task",
		Body:      "needle body",
	}); err != nil {
		t.Fatalf("create Task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	empty, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    "absentterm",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("raw empty Search: %v", err)
	}
	if empty.Groups == nil || len(empty.Groups) != 0 {
		t.Fatalf("raw empty response = %+v, want groups: []", empty)
	}
	projectIDs := []string{binding.ProjectID, "unknown-project"}
	if projectIDs[0] > projectIDs[1] {
		projectIDs[0], projectIDs[1] = projectIDs[1], projectIDs[0]
	}
	_, err = search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:       serverapi.TaskSearchModeLiteral,
		Query:      "needle",
		Context:    serverapi.TaskSearchDefaultContext,
		ProjectIDs: projectIDs,
		PageSize:   serverapi.TaskSearchDefaultPageSize,
	})
	var searchErr *serverapi.TaskSearchError
	if err == nil || errors.As(err, &searchErr) {
		t.Fatalf("unknown Project error = %v, want operational scope error", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = search.Search(canceled, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Search error = %v, want context.Canceled", err)
	}
}

func TestTaskSearchMapsMalformedRawExpressionAndRejectsForeignCursor(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "needle title",
		Body:      "needle body",
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	_, err = search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    `"`,
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	var searchErr *serverapi.TaskSearchError
	if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonMalformedFTS5 {
		t.Fatalf("malformed raw error = %v, want malformed FTS5", err)
	}
	request := serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: 1,
	}
	fingerprint, err := taskSearchRequestFingerprint(request)
	if err != nil {
		t.Fatalf("taskSearchRequestFingerprint: %v", err)
	}
	token, err := encodeTaskSearchPageToken(taskSearchPageToken{
		Version:     taskSearchPageTokenVersion,
		Fingerprint: fingerprint,
		Ordinal:     1,
		RankBits:    math.Float64bits(-1),
		TaskID:      "foreign-task",
	})
	if err != nil {
		t.Fatalf("encodeTaskSearchPageToken: %v", err)
	}
	request.PageToken = &token
	request.Context++
	_, err = search.Search(ctx, request)
	if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonInvalidCursor {
		t.Fatalf("foreign cursor error = %v, want invalid cursor", err)
	}
}

func TestTaskSearchChecksSchemaBeforeClassifyingRawExpressions(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	if _, err := metadataStore.DB().ExecContext(ctx, "DROP TABLE task_search_fts"); err != nil {
		t.Fatalf("remove task-search FTS schema for operational-failure test: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	_, err = search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    `"`,
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	var searchErr *serverapi.TaskSearchError
	if err == nil || errors.As(err, &searchErr) {
		t.Fatalf("missing schema + malformed raw expression error = %T %v, want operational schema error", err, err)
	}
}

func TestTaskSearchKeepsSQLiteLockContentionOperational(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	if _, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "needle title",
		Body:      "needle body",
	}); err != nil {
		t.Fatalf("create Task: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	if _, err := metadataStore.DB().ExecContext(ctx, "PRAGMA journal_mode = DELETE"); err != nil {
		t.Fatalf("switch isolated fixture to rollback journaling: %v", err)
	}
	metadataStore.DB().SetMaxOpenConns(1)
	metadataStore.DB().SetMaxIdleConns(1)
	searchConnection, err := metadataStore.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("acquire search database connection: %v", err)
	}
	if _, err := searchConnection.ExecContext(ctx, "PRAGMA busy_timeout = 1"); err != nil {
		_ = searchConnection.Close()
		t.Fatalf("set search connection busy timeout: %v", err)
	}
	if err := searchConnection.Close(); err != nil {
		t.Fatalf("release configured search database connection: %v", err)
	}

	databasePath := filepath.Join(metadataStore.PersistenceRoot(), "db", "main.sqlite3")
	lockURL := url.URL{Scheme: "file", Path: databasePath}
	lockURL.RawQuery = "_pragma=busy_timeout(1)"
	lockDB, err := sql.Open("sqlite", lockURL.String())
	if err != nil {
		t.Fatalf("open lock database connection: %v", err)
	}
	t.Cleanup(func() { _ = lockDB.Close() })
	lockConnection, err := lockDB.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire lock database connection: %v", err)
	}
	t.Cleanup(func() { _ = lockConnection.Close() })
	if _, err := lockConnection.ExecContext(ctx, "PRAGMA locking_mode = EXCLUSIVE"); err != nil {
		t.Fatalf("set exclusive locking mode: %v", err)
	}
	if _, err := lockConnection.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("acquire exclusive SQLite lock: %v", err)
	}
	t.Cleanup(func() { _, _ = lockConnection.ExecContext(context.Background(), "ROLLBACK") })

	busyCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	t.Cleanup(cancel)
	_, err = search.Search(busyCtx, serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeFTS5,
		Query:    `"`,
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: serverapi.TaskSearchDefaultPageSize,
	})
	var searchErr *serverapi.TaskSearchError
	if err == nil || errors.As(err, &searchErr) {
		t.Fatalf("SQLite lock contention error = %T %v, want an operational error instead of malformed FTS5", err, err)
	}
	var sqliteErr *sqlitedriver.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
		t.Fatalf("SQLite lock contention error = %T %v, want SQLITE_BUSY", err, err)
	}
}

func TestTaskSearchCursorCanonicalizesEmptyFiltersAndRejectsNonFiniteRank(t *testing.T) {
	request := serverapi.TaskSearchRequest{
		Mode:     serverapi.TaskSearchModeLiteral,
		Query:    "needle",
		Context:  serverapi.TaskSearchDefaultContext,
		PageSize: 1,
	}
	fingerprint, err := taskSearchRequestFingerprint(request)
	if err != nil {
		t.Fatalf("taskSearchRequestFingerprint without filters: %v", err)
	}
	request.ProjectIDs = []string{}
	request.StatusKinds = []serverapi.WorkflowTaskStatusKind{}
	withExplicitEmptyFilters, err := taskSearchRequestFingerprint(request)
	if err != nil {
		t.Fatalf("taskSearchRequestFingerprint with explicit empty filters: %v", err)
	}
	if fingerprint != withExplicitEmptyFilters {
		t.Fatalf("empty filter fingerprint = %q, want %q", withExplicitEmptyFilters, fingerprint)
	}
	for _, rankBits := range []uint64{
		math.Float64bits(math.Inf(1)),
		math.Float64bits(math.Inf(-1)),
		math.Float64bits(math.NaN()),
	} {
		raw, err := encodeTaskSearchPageToken(taskSearchPageToken{
			Version:     taskSearchPageTokenVersion,
			Fingerprint: fingerprint,
			Ordinal:     1,
			RankBits:    rankBits,
			TaskID:      "task-1",
		})
		if err != nil {
			t.Fatalf("encodeTaskSearchPageToken: %v", err)
		}
		_, _, err = parseTaskSearchPageToken(&raw, fingerprint)
		var searchErr *serverapi.TaskSearchError
		if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonInvalidCursor {
			t.Fatalf("rank bits %x error = %v, want invalid cursor", rankBits, err)
		}
	}
}

func TestTaskSearchCursorPinsEveryRequestFilterAndPreservesExactRankBits(t *testing.T) {
	base := serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeLiteral,
		Query:           "needle",
		Context:         serverapi.TaskSearchDefaultContext,
		IncludeComments: true,
		ProjectIDs:      []string{"project-a", "project-b"},
		StatusKinds: []serverapi.WorkflowTaskStatusKind{
			serverapi.WorkflowTaskStatusKindActive,
			serverapi.WorkflowTaskStatusKindDone,
		},
		PageSize: 1,
	}
	fingerprint, err := taskSearchRequestFingerprint(base)
	if err != nil {
		t.Fatalf("taskSearchRequestFingerprint: %v", err)
	}
	token := taskSearchPageToken{
		Version:     taskSearchPageTokenVersion,
		Fingerprint: fingerprint,
		Ordinal:     7,
		RankBits:    math.Float64bits(-1.2345678901234567),
		TaskID:      "task-7",
	}
	raw, err := encodeTaskSearchPageToken(token)
	if err != nil {
		t.Fatalf("encodeTaskSearchPageToken: %v", err)
	}
	decoded, hasCursor, err := parseTaskSearchPageToken(&raw, fingerprint)
	if err != nil {
		t.Fatalf("parseTaskSearchPageToken: %v", err)
	}
	if !hasCursor || decoded.Ordinal != token.Ordinal || decoded.TaskID != token.TaskID || decoded.RankBits != token.RankBits {
		t.Fatalf("round-tripped cursor = %+v (has=%t), want %+v", decoded, hasCursor, token)
	}

	for _, test := range []struct {
		name   string
		mutate func(*serverapi.TaskSearchRequest)
	}{
		{
			name: "mode",
			mutate: func(request *serverapi.TaskSearchRequest) {
				request.Mode = serverapi.TaskSearchModeFTS5
				request.CaseSensitive = false
			},
		},
		{
			name: "query",
			mutate: func(request *serverapi.TaskSearchRequest) {
				request.Query = "other"
			},
		},
		{
			name: "context",
			mutate: func(request *serverapi.TaskSearchRequest) {
				request.Context++
			},
		},
		{
			name: "case mode",
			mutate: func(request *serverapi.TaskSearchRequest) {
				request.CaseSensitive = true
			},
		},
		{
			name: "Comment inclusion",
			mutate: func(request *serverapi.TaskSearchRequest) {
				request.IncludeComments = false
			},
		},
		{
			name: "Project scope",
			mutate: func(request *serverapi.TaskSearchRequest) {
				request.ProjectIDs = []string{"project-a"}
			},
		},
		{
			name: "status scope",
			mutate: func(request *serverapi.TaskSearchRequest) {
				request.StatusKinds = []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindDone}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := base
			request.ProjectIDs = append([]string{}, base.ProjectIDs...)
			request.StatusKinds = append([]serverapi.WorkflowTaskStatusKind{}, base.StatusKinds...)
			test.mutate(&request)
			changedFingerprint, err := taskSearchRequestFingerprint(request)
			if err != nil {
				t.Fatalf("taskSearchRequestFingerprint: %v", err)
			}
			if changedFingerprint == fingerprint {
				t.Fatalf("%s did not change task-search cursor fingerprint", test.name)
			}
			_, _, err = parseTaskSearchPageToken(&raw, changedFingerprint)
			var searchErr *serverapi.TaskSearchError
			if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonInvalidCursor {
				t.Fatalf("cursor with changed %s error = %v, want invalid cursor", test.name, err)
			}
		})
	}
}

func TestTaskSearchStaysAvailableAtRealScriptLifecycleBarriers(t *testing.T) {
	t.Run("durable completion wins while a live script is still cleaning up", func(t *testing.T) {
		shellPath, err := exec.LookPath("sh")
		if err != nil {
			t.Skipf("sh is unavailable: %v", err)
		}
		ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
		_, task, started, claimed := newClaimedTaskStatusTestFixture(t, ctx, workflowStore, binding.ProjectID)
		authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
		t.Cleanup(func() {
			if closeErr := authority.Close(context.Background()); closeErr != nil {
				t.Errorf("close authority: %v", closeErr)
			}
		})
		search := newTaskSearchWithRealLifecycleSources(t, metadataStore, authority, task.ID, started, claimed)

		releasePath := filepath.Join(t.TempDir(), "release-script")
		finalizeEntered := make(chan struct{})
		releaseFinalizer := make(chan struct{})
		handle, err := authority.StartScriptExecution(ctx, sessionruntime.ScriptExecutionRequest{
			Workflow: &sessionruntime.WorkflowExecutionRef{
				TaskID:     task.ID,
				RunID:      started.RunID,
				Generation: claimed.Generation,
			},
			Command: sessionruntime.ScriptCommand{
				Path: shellPath,
				Args: []string{"-c", `while [ ! -f "$1" ]; do sleep 0.01; done`, "sh", releasePath},
			},
			Finalize: func(context.Context, sessionruntime.ExecutionScope, sessionruntime.ScriptResult, error) error {
				close(finalizeEntered)
				<-releaseFinalizer
				return nil
			},
		})
		if err != nil {
			t.Fatalf("StartScriptExecution: %v", err)
		}
		t.Cleanup(func() {
			_ = os.WriteFile(releasePath, []byte("release"), 0o600)
			select {
			case <-releaseFinalizer:
			default:
				close(releaseFinalizer)
			}
			_ = handle.Close(context.Background())
		})

		if _, err := workflowStore.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
			t.Fatalf("CompleteRun while script remains live: %v", err)
		}
		requireTaskSearchLifecycleStatus(t, ctx, search, task.ID, serverapi.WorkflowTaskStatusKindDone)

		if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
			t.Fatalf("release script: %v", err)
		}
		select {
		case <-finalizeEntered:
		case <-t.Context().Done():
			t.Fatalf("wait for live script finalizer: %v", t.Context().Err())
		}
		requireTaskSearchLifecycleStatus(t, ctx, search, task.ID, serverapi.WorkflowTaskStatusKindDone)

		close(releaseFinalizer)
		if _, err := handle.Wait(context.Background()); err != nil {
			t.Fatalf("wait for completed script: %v", err)
		}
	})

	t.Run("retired script stays active until its durable finalizer commits", func(t *testing.T) {
		truePath, err := exec.LookPath("true")
		if err != nil {
			t.Skipf("true is unavailable: %v", err)
		}
		ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
		_, task, started, claimed := newClaimedTaskStatusTestFixture(t, ctx, workflowStore, binding.ProjectID)
		authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
		t.Cleanup(func() {
			if closeErr := authority.Close(context.Background()); closeErr != nil {
				t.Errorf("close authority: %v", closeErr)
			}
		})
		search := newTaskSearchWithRealLifecycleSources(t, metadataStore, authority, task.ID, started, claimed)

		finalizeEntered := make(chan struct{})
		releaseFinalizer := make(chan struct{})
		handle, err := authority.StartScriptExecution(ctx, sessionruntime.ScriptExecutionRequest{
			Workflow: &sessionruntime.WorkflowExecutionRef{
				TaskID:     task.ID,
				RunID:      started.RunID,
				Generation: claimed.Generation,
			},
			Command: sessionruntime.ScriptCommand{Path: truePath},
			Finalize: func(finalizeCtx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.ScriptResult, _ error) error {
				close(finalizeEntered)
				<-releaseFinalizer
				_, completionErr := workflowStore.CompleteRun(finalizeCtx, workflowstore.CompleteRunRequest{
					RunID:        started.RunID,
					TransitionID: "done",
					Actor:        "script",
				})
				return completionErr
			},
		})
		if err != nil {
			t.Fatalf("StartScriptExecution: %v", err)
		}
		t.Cleanup(func() {
			select {
			case <-releaseFinalizer:
			default:
				close(releaseFinalizer)
			}
			_ = handle.Close(context.Background())
		})
		select {
		case <-finalizeEntered:
		case <-t.Context().Done():
			t.Fatalf("wait for retired script finalizer: %v", t.Context().Err())
		}
		requireTaskSearchLifecycleStatus(t, ctx, search, task.ID, serverapi.WorkflowTaskStatusKindActive)

		close(releaseFinalizer)
		if _, err := handle.Wait(context.Background()); err != nil {
			t.Fatalf("wait for finalized script: %v", err)
		}
		requireTaskSearchLifecycleStatus(t, ctx, search, task.ID, serverapi.WorkflowTaskStatusKindDone)
	})
}

func newTaskSearchWithRealLifecycleSources(
	t *testing.T,
	metadataStore *metadata.Store,
	authority *sessionruntime.Authority,
	taskID workflow.TaskID,
	started workflowstore.StartTaskResult,
	claimed workflowstore.RunnableRunRecord,
) *TaskSearch {
	t.Helper()
	snapshots, err := newTaskStatusSnapshotCoordinator(
		metadataStore.DB(),
		metadataStore.Queries(),
		workflowexecution.NewMutationPermit(),
		authority,
		staticSchedulerObservations{snapshot: workflowexecution.SchedulerActiveRunSnapshot{
			Revision: 1,
			ActiveRuns: []workflowexecution.SchedulerActiveRunObservation{{
				RunID:       started.RunID,
				TaskID:      taskID,
				PlacementID: claimed.PlacementID,
				NodeID:      claimed.NodeID,
				Generation:  claimed.Generation,
				Phase:       workflowexecution.SchedulerActiveRunPhaseRunning,
			}},
		}},
	)
	if err != nil {
		t.Fatalf("newTaskStatusSnapshotCoordinator: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, NewTaskProjector(), snapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	return search
}

func requireTaskSearchLifecycleStatus(
	t *testing.T,
	ctx context.Context,
	search *TaskSearch,
	taskID workflow.TaskID,
	want serverapi.WorkflowTaskStatusKind,
) {
	t.Helper()
	response, err := search.Search(ctx, serverapi.TaskSearchRequest{
		Mode:        serverapi.TaskSearchModeLiteral,
		Query:       "Body",
		Context:     serverapi.TaskSearchDefaultContext,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{want},
		PageSize:    serverapi.TaskSearchDefaultPageSize,
	})
	if err != nil {
		t.Fatalf("Search at lifecycle barrier: %v", err)
	}
	if len(response.Groups) != 1 ||
		response.Groups[0].TaskID != string(taskID) ||
		response.Groups[0].Status.Kind != want {
		t.Fatalf("search at lifecycle barrier = %+v, want task %q status %q", response, taskID, want)
	}
}

func TestTaskSearchUsesCanonicalStatusForFilteringAndResponse(t *testing.T) {
	for _, test := range []struct {
		name         string
		prepare      func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflow.TaskID) (workflowstore.StartTaskResult, workflowstore.RunnableRunRecord)
		observations func(task workflow.TaskID, started workflowstore.StartTaskResult, claimed workflowstore.RunnableRunRecord) taskStatusTestObservations
		wantStatus   serverapi.WorkflowTaskStatusKind
	}{
		{
			name:    "stale durable running without exact live authority remains active",
			prepare: claimTaskStatusTestRun,
			observations: func(workflow.TaskID, workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) taskStatusTestObservations {
				return taskStatusTestObservations{}
			},
			wantStatus: serverapi.WorkflowTaskStatusKindActive,
		},
		{
			name: "durable terminal wins while exact live cleanup remains observable",
			prepare: func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflow.TaskID) (workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) {
				t.Helper()
				started, claimed := claimTaskStatusTestRun(t, ctx, store, task)
				if _, err := store.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: started.RunID, TransitionID: "done"}); err != nil {
					t.Fatalf("CompleteRun: %v", err)
				}
				return started, claimed
			},
			observations: taskStatusTestExactRunningObservations,
			wantStatus:   serverapi.WorkflowTaskStatusKindDone,
		},
		{
			name: "durable and live question disagreement remains running",
			prepare: func(t *testing.T, ctx context.Context, store *workflowstore.Store, task workflow.TaskID) (workflowstore.StartTaskResult, workflowstore.RunnableRunRecord) {
				t.Helper()
				started, claimed := claimTaskStatusTestRun(t, ctx, store, task)
				if err := store.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-disagreement"); err != nil {
					t.Fatalf("SetRunWaitingAsk: %v", err)
				}
				return started, claimed
			},
			observations: taskStatusTestExactRunningObservations,
			wantStatus:   serverapi.WorkflowTaskStatusKindRunning,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
			workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
			if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
				t.Fatalf("LinkWorkflow: %v", err)
			}
			task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
				ProjectID: binding.ProjectID,
				Title:     test.name,
				Body:      "needle",
			})
			if err != nil {
				t.Fatalf("CreateTask: %v", err)
			}
			started, claimed := test.prepare(t, ctx, workflowStore, task.ID)
			observations := test.observations(task.ID, started, claimed)
			snapshots := newTaskStatusTestSnapshotCoordinator(
				t,
				metadataStore,
				&taskStatusTestAuthoritySource{snapshot: observations.authority},
				observations.scheduler,
			)
			search, err := NewTaskSearch(metadataStore, NewTaskProjector(), snapshots)
			if err != nil {
				t.Fatalf("NewTaskSearch: %v", err)
			}
			response, err := search.Search(ctx, serverapi.TaskSearchRequest{
				Mode:        serverapi.TaskSearchModeLiteral,
				Query:       "needle",
				Context:     serverapi.TaskSearchDefaultContext,
				StatusKinds: []serverapi.WorkflowTaskStatusKind{test.wantStatus},
				PageSize:    serverapi.TaskSearchDefaultPageSize,
			})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(response.Groups) != 1 || response.Groups[0].TaskID != string(task.ID) || response.Groups[0].Status.Kind != test.wantStatus {
				t.Fatalf("search response = %+v, want task %q status %q", response, task.ID, test.wantStatus)
			}
		})
	}
}

func TestTaskSearchRawRanksEquivalentBodyAboveComment(t *testing.T) {
	ctx, metadataStore, workflowStore, binding, fixture := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("link workflow: %v", err)
	}
	bodyTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Body Task",
		Body:      "needle",
	})
	if err != nil {
		t.Fatalf("create body Task: %v", err)
	}
	commentTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Comment Task",
		Body:      "different",
	})
	if err != nil {
		t.Fatalf("create comment Task: %v", err)
	}
	if _, err := workflowStore.AddComment(ctx, commentTask.ID, "needle", "user", "user-1"); err != nil {
		t.Fatalf("add comment: %v", err)
	}
	search, err := NewTaskSearch(metadataStore, fixture.projector, fixture.statusSnapshots)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}
	snapshot, err := fixture.statusSnapshots.Capture(ctx)
	if err != nil {
		t.Fatalf("capture status snapshot: %v", err)
	}
	t.Cleanup(func() {
		if err := snapshot.Close(); err != nil {
			t.Errorf("close status snapshot: %v", err)
		}
	})
	rows, err := search.queryPage(ctx, snapshot, serverapi.TaskSearchRequest{
		Mode:            serverapi.TaskSearchModeFTS5,
		Query:           "needle",
		Context:         serverapi.TaskSearchDefaultContext,
		IncludeComments: true,
		PageSize:        serverapi.TaskSearchDefaultPageSize,
	}, taskSearchPageToken{}, false)
	if err != nil {
		t.Fatalf("query raw search page: %v", err)
	}
	ranks := map[string]float64{}
	for _, row := range rows {
		ranks[row.TaskID] = row.TaskWeightedRank
	}
	bodyRank, bodyFound := ranks[string(bodyTask.ID)]
	commentRank, commentFound := ranks[string(commentTask.ID)]
	if !bodyFound || !commentFound {
		t.Fatalf("raw search rows = %+v, want body and Comment Task", rows)
	}
	if bodyRank <= commentRank {
		t.Fatalf("body rank = %f, comment rank = %f; want body above equivalent Comment", bodyRank, commentRank)
	}
}
