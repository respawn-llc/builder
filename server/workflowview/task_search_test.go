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
