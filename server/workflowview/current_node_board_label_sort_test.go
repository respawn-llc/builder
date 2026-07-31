package workflowview

import (
	"fmt"
	"testing"

	"core/server/workflow/label"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

type currentNodeBoardLabelSortFixture struct {
	currentNodeViewFixture
	labelIDs []label.ID
	taskIDs  map[string]string
}

func newCurrentNodeBoardLabelSortFixture(t *testing.T) currentNodeBoardLabelSortFixture {
	t.Helper()
	current := newCurrentNodeViewFixture(t, false)
	labelIDs := make([]label.ID, 100)
	for index := range labelIDs {
		created, err := current.store.CreateProjectLabel(
			current.ctx,
			current.binding.ProjectID,
			fmt.Sprintf("label-%03d", index+1),
		)
		if err != nil {
			t.Fatalf("CreateProjectLabel %d: %v", index+1, err)
		}
		labelIDs[index] = created.ID
	}
	start := func(t *testing.T, title string, labelIDs ...label.ID) string {
		t.Helper()
		workflowID := current.workflowID
		labelStrings := make([]string, 0, len(labelIDs))
		for _, id := range labelIDs {
			labelStrings = append(labelStrings, id.String())
		}
		task, err := current.store.CreateTask(current.ctx, workflowstore.CreateTaskRequest{
			ProjectID:  current.binding.ProjectID,
			WorkflowID: &workflowID,
			Title:      title,
			LabelIDs:   labelStrings,
		})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		if _, err := current.store.StartTask(current.ctx, task.ID); err != nil {
			t.Fatalf("StartTask %s: %v", title, err)
		}
		return string(task.ID)
	}
	return currentNodeBoardLabelSortFixture{
		currentNodeViewFixture: current,
		labelIDs:               labelIDs,
		taskIDs: map[string]string{
			"unlabeled":        start(t, "unlabeled"),
			"single-nine":      start(t, "single-nine", labelIDs[8]),
			"nine-ten":         start(t, "nine-ten", labelIDs[8], labelIDs[9]),
			"nine-hundred":     start(t, "nine-hundred", labelIDs[8], labelIDs[99]),
			"single-ten":       start(t, "single-ten", labelIDs[9]),
			"single-99":        start(t, "single-99", labelIDs[98]),
			"single-100":       start(t, "single-100", labelIDs[99]),
			"nine-ten-hundred": start(t, "nine-ten-hundred", labelIDs[8], labelIDs[9], labelIDs[99]),
		},
	}
}

func (f currentNodeBoardLabelSortFixture) list(
	t *testing.T,
	sortValue serverapi.WorkflowBoardNodeCardsSort,
	filter serverapi.WorkflowTaskLabelFilter,
	pageSize int,
) ([]string, serverapi.WorkflowBoardNodeCardsListResponse, serverapi.WorkflowBoardNodeCardsListResponse) {
	t.Helper()
	request := serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   f.binding.ProjectID,
		WorkflowID:  string(f.workflowID),
		NodeID:      string(f.agentNodeID),
		LabelFilter: filter,
		PageSize:    pageSize,
		Sort:        &sortValue,
	}
	var got []string
	var first, second serverapi.WorkflowBoardNodeCardsListResponse
	for pageIndex := 0; ; pageIndex++ {
		page, err := f.board.ListNodeCards(f.ctx, request)
		if err != nil {
			t.Fatalf("ListNodeCards page %d: %v", pageIndex, err)
		}
		if len(page.Cards) > pageSize {
			t.Fatalf("ListNodeCards page %d cards = %d, want <= %d", pageIndex, len(page.Cards), pageSize)
		}
		for _, card := range page.Cards {
			got = append(got, card.TaskID)
		}
		if pageIndex == 0 {
			first = page
		}
		if pageIndex == 1 {
			second = page
		}
		if page.NextPageToken == nil {
			break
		}
		request.PageToken = page.NextPageToken
	}
	return got, first, second
}

func TestCurrentNodeBoardLabelSortUsesProjectOrderAndBoundedCursors(t *testing.T) {
	fixture := newCurrentNodeBoardLabelSortFixture(t)
	filter := serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindNone,
	}
	asc := serverapi.WorkflowBoardNodeCardsSort{
		Field:     serverapi.WorkflowBoardNodeCardsSortFieldLabels,
		Direction: serverapi.WorkflowTaskListSortDirectionAsc,
	}
	desc := asc
	desc.Direction = serverapi.WorkflowTaskListSortDirectionDesc
	id := func(name string) string { return fixture.taskIDs[name] }
	ascWant := []string{
		id("single-nine"),
		id("nine-ten"),
		id("nine-ten-hundred"),
		id("nine-hundred"),
		id("single-ten"),
		id("single-99"),
		id("single-100"),
		id("unlabeled"),
	}
	descWant := []string{
		id("single-100"),
		id("single-99"),
		id("single-ten"),
		id("nine-hundred"),
		id("nine-ten-hundred"),
		id("nine-ten"),
		id("single-nine"),
		id("unlabeled"),
	}
	for _, test := range []struct {
		name string
		sort serverapi.WorkflowBoardNodeCardsSort
		want []string
	}{
		{name: "ascending", sort: asc, want: ascWant},
		{name: "descending", sort: desc, want: descWant},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, first, second := fixture.list(t, test.sort, filter, 2)
			if !equalStrings(got, test.want) {
				t.Fatalf("label sort = %v, want %v", got, test.want)
			}
			if first.PreviousPageToken != nil || first.NextPageToken == nil {
				t.Fatalf("first page tokens = previous %v next %v", first.PreviousPageToken, first.NextPageToken)
			}
			if second.PreviousPageToken == nil {
				t.Fatalf("second page token = previous %v", second.PreviousPageToken)
			}
			request := serverapi.WorkflowBoardNodeCardsListRequest{
				ProjectID:   fixture.binding.ProjectID,
				WorkflowID:  string(fixture.workflowID),
				NodeID:      string(fixture.agentNodeID),
				LabelFilter: filter,
				PageSize:    2,
				PageToken:   second.PreviousPageToken,
				Sort:        &test.sort,
			}
			previous, err := fixture.board.ListNodeCards(fixture.ctx, request)
			if err != nil {
				t.Fatalf("ListNodeCards previous: %v", err)
			}
			if len(previous.Cards) != 2 ||
				previous.Cards[0].TaskID != first.Cards[0].TaskID ||
				previous.Cards[1].TaskID != first.Cards[1].TaskID {
				t.Fatalf("previous page = %+v, want %+v", previous.Cards, first.Cards)
			}
		})
	}

	namedFilter := serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
		Named: &serverapi.WorkflowTaskNamedLabelFilter{
			Mode:     serverapi.WorkflowTaskNamedLabelFilterModeAny,
			LabelIDs: []string{fixture.labelIDs[8].String()},
		},
	}
	filtered, _, _ := fixture.list(t, asc, namedFilter, 2)
	filteredWant := []string{
		id("single-nine"),
		id("nine-ten"),
		id("nine-ten-hundred"),
		id("nine-hundred"),
	}
	if !equalStrings(filtered, filteredWant) {
		t.Fatalf("filtered label sort = %v, want %v", filtered, filteredWant)
	}

	reordered := make([]label.ID, 0, len(fixture.labelIDs))
	reordered = append(reordered, fixture.labelIDs[9], fixture.labelIDs[8])
	for index, labelID := range fixture.labelIDs {
		if index == 8 || index == 9 {
			continue
		}
		reordered = append(reordered, labelID)
	}
	if _, err := fixture.store.ReorderProjectLabels(fixture.ctx, fixture.binding.ProjectID, reordered); err != nil {
		t.Fatalf("ReorderProjectLabels: %v", err)
	}
	reorderedWant := []string{
		id("single-ten"),
		id("nine-ten"),
		id("nine-ten-hundred"),
		id("single-nine"),
		id("nine-hundred"),
		id("single-99"),
		id("single-100"),
		id("unlabeled"),
	}
	got, _, _ := fixture.list(t, asc, filter, 2)
	if !equalStrings(got, reorderedWant) {
		t.Fatalf("reordered label sort = %v, want %v", got, reorderedWant)
	}

	if _, err := fixture.store.DeleteProjectLabel(fixture.ctx, fixture.binding.ProjectID, fixture.labelIDs[0]); err != nil {
		t.Fatalf("DeleteProjectLabel churn seed: %v", err)
	}
	for cycle := 0; cycle < 3; cycle++ {
		created, err := fixture.store.CreateProjectLabel(
			fixture.ctx,
			fixture.binding.ProjectID,
			fmt.Sprintf("churn-%d", cycle),
		)
		if err != nil {
			t.Fatalf("CreateProjectLabel churn %d: %v", cycle, err)
		}
		if _, err := fixture.store.DeleteProjectLabel(fixture.ctx, fixture.binding.ProjectID, created.ID); err != nil {
			t.Fatalf("DeleteProjectLabel churn %d: %v", cycle, err)
		}
	}
}
