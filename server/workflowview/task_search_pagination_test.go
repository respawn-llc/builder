package workflowview

import (
	"testing"

	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestTaskSearchPaginatesWithOffsets(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	first := createTaskSearchTask(t, fixture, "First", "needle needle")
	second := createTaskSearchTask(t, fixture, "Second", "needle")
	request := taskSearchRequest("needle")
	request.PageSize = 1

	page, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(page.Groups) != 1 || len(page.Groups[0].Hits) != 1 || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatalf("first page = %+v", page)
	}
	firstTaskID := page.Groups[0].TaskID

	request.Offset = page.NextOffset
	page, err = search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(page.Groups) != 1 || len(page.Groups[0].Hits) != 1 || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("second page = %+v", page)
	}

	request.Offset = page.NextOffset
	page, err = search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(page.Groups) != 1 || len(page.Groups[0].Hits) != 1 || page.NextOffset != nil {
		t.Fatalf("third page = %+v", page)
	}
	if firstTaskID != string(first.ID) && firstTaskID != string(second.ID) {
		t.Fatalf("first page task = %q, want a matching Task", firstTaskID)
	}
}

func TestTaskSearchRejectsNegativeOffset(t *testing.T) {
	fixture, search := newTaskSearchFixture(t, false)
	createTaskSearchTask(t, fixture, "First", "needle")
	negative := -1
	request := taskSearchRequest("needle")
	request.Offset = &negative

	if _, err := search.Search(fixture.ctx, request); err == nil {
		t.Fatal("Search accepted a negative offset")
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
	if len(first.Groups) != 2 || first.NextOffset == nil || *first.NextOffset != 2 {
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

	request.Offset = first.NextOffset
	second, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if len(second.Groups) != 1 ||
		second.Groups[0].TaskID != string(bodyTask.ID) ||
		len(second.Groups[0].Hits) != 1 ||
		second.Groups[0].Hits[0].Ordinal != 2 ||
		second.NextOffset != nil {
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
	if first.NextOffset == nil || *first.NextOffset != 5 {
		t.Fatalf("first page omitted next offset: %+v", first)
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

	request.Offset = first.NextOffset
	second, err := search.Search(fixture.ctx, request)
	if err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if len(second.Groups) != 1 ||
		second.Groups[0].TaskID != string(deep.ID) ||
		len(second.Groups[0].Hits) != 1 ||
		second.Groups[0].Hits[0].Ordinal != 3 ||
		second.NextOffset != nil {
		t.Fatalf("second page = %+v, want deep Task ordinal 3 with no continuation", second)
	}
}
