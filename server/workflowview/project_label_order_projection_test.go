package workflowview

import (
	"slices"
	"testing"

	"core/server/workflow/label"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestProjectLabelOrderFlowsThroughTaskReadModels(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	first, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.binding.ProjectID, "first")
	if err != nil {
		t.Fatalf("CreateProjectLabel first: %v", err)
	}
	second, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.binding.ProjectID, "second")
	if err != nil {
		t.Fatalf("CreateProjectLabel second: %v", err)
	}
	third, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.binding.ProjectID, "third")
	if err != nil {
		t.Fatalf("CreateProjectLabel third: %v", err)
	}
	if _, err := fixture.store.ReorderProjectLabels(
		fixture.ctx,
		fixture.binding.ProjectID,
		[]label.ID{third.ID, first.ID, second.ID},
	); err != nil {
		t.Fatalf("ReorderProjectLabels: %v", err)
	}

	workflowID := fixture.workflowID
	task, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      "Ordered labels",
		LabelIDs:   []string{second.ID.String(), third.ID.String(), first.ID.String()},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := fixture.store.StartTask(fixture.ctx, task.ID); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	want := []string{third.ID.String(), first.ID.String(), second.ID.String()}

	detail, err := fixture.detail.GetTask(fixture.ctx, string(task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	if !slices.Equal(detail.LabelIDs, want) {
		t.Fatalf("task detail label IDs = %v, want Project order %v", detail.LabelIDs, want)
	}

	projectID := fixture.binding.ProjectID
	workflowIDString := string(fixture.workflowID)
	limit := 20
	list, err := fixture.tasks.List(fixture.ctx, serverapi.WorkflowTaskListRequest{
		ProjectID:  &projectID,
		WorkflowID: &workflowIDString,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
		Limit: &limit,
	})
	if err != nil {
		t.Fatalf("TaskList.List: %v", err)
	}
	var listed *serverapi.WorkflowTaskListItem
	for index := range list.Tasks {
		if list.Tasks[index].TaskID == string(task.ID) {
			listed = &list.Tasks[index]
			break
		}
	}
	if listed == nil {
		t.Fatalf("task list omitted task %q: %+v", task.ID, list.Tasks)
	}
	if !slices.Equal(listed.LabelIDs, want) {
		t.Fatalf("task list label IDs = %v, want Project order %v", listed.LabelIDs, want)
	}

	cards, err := fixture.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: string(fixture.workflowID),
		NodeID:     string(fixture.agentNodeID),
		PageSize:   20,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards: %v", err)
	}
	var card *serverapi.WorkflowBoardTaskCard
	for index := range cards.Cards {
		if cards.Cards[index].TaskID == string(task.ID) {
			card = &cards.Cards[index]
			break
		}
	}
	if card == nil {
		t.Fatalf("board omitted task %q: %+v", task.ID, cards.Cards)
	}
	if !slices.Equal(card.LabelIDs, want) {
		t.Fatalf("board card label IDs = %v, want Project order %v", card.LabelIDs, want)
	}
}
