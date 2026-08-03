package workflowview

import (
	"errors"
	"sort"
	"testing"

	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestBoardProjectsStartedCurrentNode(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Board task")

	board, err := fixture.board.Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: &fixture.workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.Get: %v", err)
	}
	agentColumn := workflowViewBoardColumn(t, board, fixture.agentNodeID)
	if agentColumn.TaskCount != 1 {
		t.Fatalf("agent column task count = %d, want 1 Current Node", agentColumn.TaskCount)
	}

	cards, err := fixture.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(fixture.agentNodeID),
		PageSize:   20,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 {
		t.Fatalf("board cards = %+v, want one Current Node card", cards.Cards)
	}
	card := cards.Cards[0]
	if card.TaskID != string(started.task.ID) ||
		len(card.ActiveNodeIDs) != 1 ||
		card.ActiveNodeIDs[0] != string(fixture.agentNodeID) ||
		card.Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		card.Actions.CanStart {
		t.Fatalf("board card = %+v, want started Current Node projection", card)
	}
}

func TestBoardListNodeCardsPaginatesDeterministically(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := []startedCurrentNodeViewTask{
		fixture.startTask(t, "Board A"),
		fixture.startTask(t, "Board B"),
		fixture.startTask(t, "Board C"),
	}
	for _, task := range started {
		fixture.setTaskUpdatedAt(t, task.task.ID, 1_000)
	}
	want := []string{
		string(started[0].task.ID),
		string(started[1].task.ID),
		string(started[2].task.ID),
	}
	sort.Sort(sort.Reverse(sort.StringSlice(want)))

	request := serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(fixture.agentNodeID),
		PageSize:   1,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	}
	var got []string
	var firstPage serverapi.WorkflowBoardNodeCardsListResponse
	var secondPage serverapi.WorkflowBoardNodeCardsListResponse
	for pageIndex := 0; ; pageIndex++ {
		page, err := fixture.board.ListNodeCards(fixture.ctx, request)
		if err != nil {
			t.Fatalf("Board.ListNodeCards page %d: %v", pageIndex, err)
		}
		if len(page.Cards) != 1 {
			t.Fatalf("board page %d cards = %+v, want one", pageIndex, page.Cards)
		}
		got = append(got, page.Cards[0].TaskID)
		if pageIndex == 0 {
			firstPage = page
			if page.PreviousPageToken != nil || page.NextPageToken == nil {
				t.Fatalf("first board page tokens = previous %v next %v", page.PreviousPageToken, page.NextPageToken)
			}
		}
		if pageIndex == 1 {
			secondPage = page
			if page.PreviousPageToken == nil {
				t.Fatal("second board page has no newer-page token")
			}
		}
		if page.NextPageToken == nil {
			break
		}
		request.PageToken = page.NextPageToken
	}
	if !equalStrings(got, want) {
		t.Fatalf("board pagination order = %v, want %v", got, want)
	}
	request.PageToken = secondPage.PreviousPageToken
	newer, err := fixture.board.ListNodeCards(fixture.ctx, request)
	if err != nil {
		t.Fatalf("Board.ListNodeCards newer: %v", err)
	}
	if len(newer.Cards) != 1 || newer.Cards[0].TaskID != firstPage.Cards[0].TaskID {
		t.Fatalf("newer board page = %+v, want first page task %q", newer.Cards, firstPage.Cards[0].TaskID)
	}
	request.PageToken = firstPage.NextPageToken
	request.LabelFilter = serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindUnlabeled}
	if _, err := fixture.board.ListNodeCards(fixture.ctx, request); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("board token replay with changed filter error = %v, want invalid page token", err)
	}
}

func TestBoardListNodeCardsDependencyFilterRunsBeforePaginationAndBindsCursor(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	noDependencies := fixture.startTask(t, "No dependencies")
	satisfied := fixture.startTask(t, "Satisfied")
	blocked := fixture.startTask(t, "Blocked")
	blockedAgain := fixture.startTask(t, "Blocked again")
	for _, task := range []startedCurrentNodeViewTask{noDependencies, satisfied, blocked, blockedAgain} {
		fixture.setTaskUpdatedAt(t, task.task.ID, 1_000)
	}

	satisfiedBlocker := createViewTask(t, fixture, "Satisfied blocker")
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{
		BlockerTaskID: satisfiedBlocker.ID,
		BlockedTaskID: satisfied.task.ID,
	}); err != nil {
		t.Fatalf("AddTaskDependency satisfied: %v", err)
	}
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := fixture.store.ManualMoveTask(fixture.ctx, workflowstore.ManualMoveRequest{
		TaskID:       satisfiedBlocker.ID,
		TargetNodeID: terminalNodeID(t, definition),
	}); err != nil {
		t.Fatalf("ManualMoveTask satisfied blocker: %v", err)
	}

	otherWorkflowID := currentNodeViewWorkflow(t, fixture.store, false)
	if _, err := fixture.store.LinkWorkflow(fixture.ctx, fixture.binding.ProjectID, otherWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow other workflow: %v", err)
	}
	otherWorkflowBlocker, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: &otherWorkflowID,
		Title:      "Other workflow blocker",
	})
	if err != nil {
		t.Fatalf("CreateTask other workflow blocker: %v", err)
	}
	for _, task := range []startedCurrentNodeViewTask{blocked, blockedAgain} {
		if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{
			BlockerTaskID: otherWorkflowBlocker.ID,
			BlockedTaskID: task.task.ID,
		}); err != nil {
			t.Fatalf("AddTaskDependency blocked task %q: %v", task.task.ID, err)
		}
	}
	fixture.setTaskUpdatedAt(t, blocked.task.ID, 4_000)
	fixture.setTaskUpdatedAt(t, blockedAgain.task.ID, 3_000)
	fixture.setTaskUpdatedAt(t, noDependencies.task.ID, 2_000)
	fixture.setTaskUpdatedAt(t, satisfied.task.ID, 1_000)

	filterValue := func(value bool) *bool { return &value }
	requestFor := func(filter *bool) serverapi.WorkflowBoardNodeCardsListRequest {
		return serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:        fixture.binding.ProjectID,
			WorkflowID:       fixture.workflowID,
			NodeID:           string(fixture.agentNodeID),
			DependencyFilter: filter,
			PageSize:         1,
			LabelFilter:      serverapi.WorkflowTaskLabelFilterNone(),
		}
	}
	filters := []*bool{nil, filterValue(true), filterValue(false)}
	tokens := make([]string, len(filters))
	for index, filter := range filters {
		page, err := fixture.board.ListNodeCards(fixture.ctx, requestFor(filter))
		if err != nil {
			t.Fatalf("ListNodeCards filter %d: %v", index, err)
		}
		if len(page.Cards) != 1 || page.NextPageToken == nil {
			t.Fatalf("filter %d page = %+v, want one card and next token", index, page)
		}
		tokens[index] = *page.NextPageToken
	}

	unblockedPage, err := fixture.board.ListNodeCards(fixture.ctx, requestFor(filterValue(true)))
	if err != nil {
		t.Fatalf("ListNodeCards unblocked first page: %v", err)
	}
	if unblockedPage.Cards[0].TaskID != string(noDependencies.task.ID) {
		t.Fatalf("unblocked first page card = %q, want no-dependency task %q", unblockedPage.Cards[0].TaskID, noDependencies.task.ID)
	}
	unblockedPage, err = fixture.board.ListNodeCards(fixture.ctx, requestWithToken(requestFor(filterValue(true)), unblockedPage.NextPageToken))
	if err != nil {
		t.Fatalf("ListNodeCards unblocked second page: %v", err)
	}
	if len(unblockedPage.Cards) != 1 || unblockedPage.Cards[0].TaskID != string(satisfied.task.ID) {
		t.Fatalf("unblocked second page = %+v, want satisfied task %q", unblockedPage.Cards, satisfied.task.ID)
	}
	newer, err := fixture.board.ListNodeCards(fixture.ctx, requestWithToken(requestFor(filterValue(true)), unblockedPage.PreviousPageToken))
	if err != nil {
		t.Fatalf("ListNodeCards unblocked newer page: %v", err)
	}
	if len(newer.Cards) != 1 || newer.Cards[0].TaskID != string(noDependencies.task.ID) {
		t.Fatalf("unblocked newer page = %+v, want no-dependency task %q", newer.Cards, noDependencies.task.ID)
	}

	for sourceIndex, token := range tokens {
		for targetIndex, filter := range filters {
			if sourceIndex == targetIndex {
				continue
			}
			if _, err := fixture.board.ListNodeCards(fixture.ctx, requestWithToken(requestFor(filter), &token)); !errors.Is(err, ErrInvalidPageToken) {
				t.Fatalf("token from filter %d replayed under filter %d with error %v, want invalid page token", sourceIndex, targetIndex, err)
			}
		}
	}
}

func requestWithToken(request serverapi.WorkflowBoardNodeCardsListRequest, token *string) serverapi.WorkflowBoardNodeCardsListRequest {
	request.PageToken = token
	return request
}
