package workflowview

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"core/server/workflowstore"
	"core/shared/serverapi"
)

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

	fingerprint, err := taskSearchRequestFingerprint(taskSearchRequest("needle"))
	if err != nil {
		t.Fatalf("taskSearchRequestFingerprint: %v", err)
	}
	raw, err := search.tokens.encode(taskSearchPageToken{
		Version:     taskSearchPageTokenVersion,
		Fingerprint: fingerprint,
		Ordinal:     1,
		RankBits:    math.Float64bits(math.NaN()),
		TaskID:      string(first.ID),
	})
	if err != nil {
		t.Fatalf("encode task-search page token: %v", err)
	}
	_, _, err = search.tokens.parse(&raw, fingerprint)
	var searchErr *serverapi.TaskSearchError
	if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonInvalidCursor {
		t.Fatalf("non-finite cursor error = %v", err)
	}
}

func TestTaskSearchCursorIsOpaqueAndRejectsCiphertextMutation(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	createTaskSearchTask(t, fixture, "First", "needle needle")
	createTaskSearchTask(t, fixture, "Second", "needle")
	request := taskSearchRequest("needle")
	request.PageSize = 1

	page, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if page.NextPageToken == nil {
		t.Fatalf("first Search omitted continuation token: %+v", page)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(*page.NextPageToken)
	if err != nil {
		t.Fatalf("decode opaque continuation token: %v", err)
	}
	if json.Valid(sealed) {
		t.Fatalf("continuation token exposes a JSON cursor payload: %q", sealed)
	}
	sealed[len(sealed)/2] ^= 1
	tampered := base64.RawURLEncoding.EncodeToString(sealed)
	request.PageToken = &tampered

	_, err = search.Search(fixture.ctx, request)
	var searchErr *serverapi.TaskSearchError
	if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonInvalidCursor {
		t.Fatalf("Search with a mutated continuation token = %v, want invalid cursor", err)
	}
}

func TestTaskSearchPaginatesBreadthFirstAcrossTasksAndRepeatsTaskAcrossPages(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	titleTask := createTaskSearchTask(t, fixture, "needle title", "different body")
	bodyTask := createTaskSearchTask(t, fixture, "body task", "needle first needle second")
	request := taskSearchRequest("needle")
	request.PageSize = 2

	first, err := search.Search(fixture.ctx, request)
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
	second, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if len(second.Groups) != 1 ||
		second.Groups[0].TaskID != string(bodyTask.ID) ||
		len(second.Groups[0].Hits) != 1 ||
		second.Groups[0].Hits[0].Ordinal != 2 ||
		second.NextPageToken != nil {
		t.Fatalf("second breadth-first page = %+v, want the repeated Task's second hit", second)
	}
}

func TestTaskSearchPageCrossesOrdinalRoundsAndRepeatsDeepTask(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	deep := createTaskSearchTask(t, fixture, "Deep", "needle needle needle")
	shallowTasks := []workflowstore.TaskRecord{
		createTaskSearchTask(t, fixture, "Shallow one", "needle"),
		createTaskSearchTask(t, fixture, "Shallow two", "needle"),
		createTaskSearchTask(t, fixture, "Shallow three", "needle"),
	}
	request := taskSearchRequest("needle")
	request.PageSize = 5

	first, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if first.NextPageToken == nil {
		t.Fatalf("first page omitted continuation token: %+v", first)
	}
	if len(first.Groups) != 4 {
		t.Fatalf("first page group count = %d, want 4: %+v", len(first.Groups), first)
	}
	groupsByTaskID := make(map[string]serverapi.TaskSearchGroup, len(first.Groups))
	totalHits := 0
	for _, group := range first.Groups {
		groupsByTaskID[group.TaskID] = group
		totalHits += len(group.Hits)
	}
	if totalHits != 5 {
		t.Fatalf("first page hit count = %d, want 5: %+v", totalHits, first)
	}
	deepGroup, ok := groupsByTaskID[string(deep.ID)]
	if !ok || deepGroup.TotalHitCount != 3 || len(deepGroup.Hits) != 2 ||
		deepGroup.Hits[0].Ordinal != 1 || deepGroup.Hits[1].Ordinal != 2 {
		t.Fatalf("first page deep Task group = %+v, want ordinals 1 and 2 of 3", deepGroup)
	}
	for _, shallow := range shallowTasks {
		group, ok := groupsByTaskID[string(shallow.ID)]
		if !ok || group.TotalHitCount != 1 || len(group.Hits) != 1 || group.Hits[0].Ordinal != 1 {
			t.Fatalf("first page shallow Task %s group = %+v, want ordinal 1", shallow.ID, group)
		}
	}

	request.PageToken = first.NextPageToken
	second, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if len(second.Groups) != 1 ||
		second.Groups[0].TaskID != string(deep.ID) ||
		len(second.Groups[0].Hits) != 1 ||
		second.Groups[0].Hits[0].Ordinal != 3 ||
		second.NextPageToken != nil {
		t.Fatalf("second page = %+v, want deep Task ordinal 3 with no continuation", second)
	}
}

func TestTaskSearchCursorCanonicalizesEmptyFiltersAndRejectsNonFiniteRank(t *testing.T) {
	codec, err := newTaskSearchPageTokenCodec()
	if err != nil {
		t.Fatalf("newTaskSearchPageTokenCodec: %v", err)
	}
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
		raw, err := codec.encode(taskSearchPageToken{
			Version:     taskSearchPageTokenVersion,
			Fingerprint: fingerprint,
			Ordinal:     1,
			RankBits:    rankBits,
			TaskID:      "task-1",
		})
		if err != nil {
			t.Fatalf("encode task-search page token: %v", err)
		}
		_, _, err = codec.parse(&raw, fingerprint)
		var searchErr *serverapi.TaskSearchError
		if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonInvalidCursor {
			t.Fatalf("rank bits %x error = %v, want invalid cursor", rankBits, err)
		}
	}
}

func TestTaskSearchCursorPinsEveryRequestFilterAndPreservesExactRankBits(t *testing.T) {
	codec, err := newTaskSearchPageTokenCodec()
	if err != nil {
		t.Fatalf("newTaskSearchPageTokenCodec: %v", err)
	}
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
	raw, err := codec.encode(token)
	if err != nil {
		t.Fatalf("encode task-search page token: %v", err)
	}
	decoded, hasCursor, err := codec.parse(&raw, fingerprint)
	if err != nil {
		t.Fatalf("parse task-search page token: %v", err)
	}
	if !hasCursor || decoded.Ordinal != token.Ordinal || decoded.TaskID != token.TaskID || decoded.RankBits != token.RankBits {
		t.Fatalf("round-tripped cursor = %+v (has=%t), want %+v", decoded, hasCursor, token)
	}

	for _, test := range []struct {
		name   string
		mutate func(*serverapi.TaskSearchRequest)
	}{
		{name: "mode", mutate: func(request *serverapi.TaskSearchRequest) {
			request.Mode = serverapi.TaskSearchModeFTS5
			request.CaseSensitive = false
		}},
		{name: "query", mutate: func(request *serverapi.TaskSearchRequest) { request.Query = "other" }},
		{name: "context", mutate: func(request *serverapi.TaskSearchRequest) { request.Context++ }},
		{name: "case mode", mutate: func(request *serverapi.TaskSearchRequest) { request.CaseSensitive = true }},
		{name: "Comment inclusion", mutate: func(request *serverapi.TaskSearchRequest) { request.IncludeComments = false }},
		{name: "Project scope", mutate: func(request *serverapi.TaskSearchRequest) {
			request.ProjectIDs = []string{"project-a"}
		}},
		{name: "status scope", mutate: func(request *serverapi.TaskSearchRequest) {
			request.StatusKinds = []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindDone}
		}},
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
			_, _, err = codec.parse(&raw, changedFingerprint)
			var searchErr *serverapi.TaskSearchError
			if !errors.As(err, &searchErr) || searchErr.Reason != serverapi.TaskSearchErrorReasonInvalidCursor {
				t.Fatalf("cursor with changed %s error = %v, want invalid cursor", test.name, err)
			}
		})
	}
}
