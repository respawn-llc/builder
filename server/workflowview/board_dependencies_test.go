package workflowview

import (
	"testing"

	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestBoardCardsProjectExactDependencyProgressFromThePagedQuery(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Blocked board task")
	blocker := createViewTask(t, fixture, "Blocker")
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: started.task.ID,
	}); err != nil {
		t.Fatalf("add blocker dependency: %v", err)
	}
	request := serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(fixture.agentNodeID),
		PageSize:   1,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	}
	page, err := fixture.board.ListNodeCards(fixture.ctx, request)
	if err != nil {
		t.Fatalf("ListNodeCards incomplete dependency: %v", err)
	}
	if len(page.Cards) != 1 || page.Cards[0].DependencyProgress == nil ||
		page.Cards[0].DependencyProgress.SatisfiedCount != 0 ||
		page.Cards[0].DependencyProgress.TotalCount != 1 {
		t.Fatalf("incomplete dependency card = %+v, want 0/1 progress", page.Cards)
	}
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := fixture.store.ManualMoveTask(fixture.ctx, workflowstore.ManualMoveRequest{
		TaskID:       blocker.ID,
		TargetNodeID: terminalNodeID(t, definition),
	}); err != nil {
		t.Fatalf("complete blocker: %v", err)
	}
	page, err = fixture.board.ListNodeCards(fixture.ctx, request)
	if err != nil {
		t.Fatalf("ListNodeCards complete dependency: %v", err)
	}
	if len(page.Cards) != 1 || page.Cards[0].DependencyProgress == nil ||
		page.Cards[0].DependencyProgress.SatisfiedCount != 1 ||
		page.Cards[0].DependencyProgress.TotalCount != 1 {
		t.Fatalf("complete dependency card = %+v, want 1/1 progress", page.Cards)
	}
}
