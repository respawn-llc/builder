package workflowview

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"core/server/metadata"
	"core/server/workflowstore"
	"core/shared/serverapi"
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
