package workflowview

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/runtimeids"
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
	for _, nodeID := range card.ActiveNodeIDs {
		if _, err := runtimeids.GraphEntityIDBlob(nodeID); err != nil {
			t.Fatalf("board card active Node ID %q is not canonical UUIDv4: %v", nodeID, err)
		}
	}
	if card.TaskID != string(started.task.ID) ||
		len(card.ActiveNodeIDs) != 1 ||
		card.ActiveNodeIDs[0] != string(fixture.agentNodeID) ||
		card.Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		card.Actions.CanStart ||
		card.Actions.CanDelete {
		t.Fatalf("board card = %+v, want started Current Node projection", card)
	}
}

func TestBoardDoesNotResolveLiveSessionLabelsForMultipleCards(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := []startedCurrentNodeViewTask{
		fixture.startTask(t, "Board live A"),
		fixture.startTask(t, "Board live B"),
	}
	executions := make(map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot, len(started))
	quiescence := make(map[workflow.TaskID]bool, len(started))
	for _, task := range started {
		sessionID := fixture.bindCurrentNodeSession(t, task)
		if _, err := fixture.metadata.DB().ExecContext(
			fixture.ctx,
			`UPDATE sessions SET name = ? WHERE id = ?`,
			" ",
			sessionID.String(),
		); err != nil {
			t.Fatalf("invalidate Session name: %v", err)
		}
		executions[task.task.ID] = sessionruntime.TaskExecutionSnapshot{
			Executions: []sessionruntime.TaskExecution{{
				Ref: sessionruntime.WorkflowExecutionRef{
					ProjectID:   fixture.binding.ProjectID,
					WorkflowID:  fixture.workflowID,
					CurrentNode: task.currentNode,
				},
				Agent: &sessionruntime.TaskAgentExecutionTarget{SessionID: sessionID},
			}},
		}
		quiescence[task.task.ID] = false
	}
	projection, err := NewTaskStatusProjection(
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{
			observation: workflowexecution.WorkflowTaskExecutionObservation{
				Executions: executions,
				Quiescence: quiescence,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	board, err := NewBoard(
		fixture.metadata,
		mustDefinitionProjection(t, fixture.store),
		testsetup.QuestionsEnabled("coder"),
		projection,
	)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}

	page, err := board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   fixture.binding.ProjectID,
		WorkflowID:  fixture.workflowID,
		NodeID:      string(fixture.agentNodeID),
		PageSize:    20,
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
	})
	if err != nil {
		t.Fatalf("Board.ListNodeCards: %v", err)
	}
	if len(page.Cards) != len(started) {
		t.Fatalf("board cards = %d, want %d", len(page.Cards), len(started))
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
		string(started[2].task.ID),
		string(started[1].task.ID),
		string(started[0].task.ID),
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
	var got []string
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
			if page.NextOffset == nil {
				t.Fatal("first board page has no next offset")
			}
		}
		if page.NextOffset == nil {
			break
		}
		request.Offset = page.NextOffset
	}
	if !equalStrings(got, want) {
		t.Fatalf("board pagination order = %v, want %v", got, want)
	}
	request.Offset = nil
	request.Sort = &serverapi.WorkflowTaskListSort{
		Field:     serverapi.WorkflowTaskListSortFieldCreated,
		Direction: serverapi.WorkflowTaskListSortDirectionAsc,
	}
	page, err := fixture.board.ListNodeCards(fixture.ctx, request)
	if err != nil {
		t.Fatalf("Board.ListNodeCards created sort: %v", err)
	}
	if page.Cards[0].TaskID != string(started[0].task.ID) {
		t.Fatalf("created ascending first card = %q, want %q", page.Cards[0].TaskID, started[0].task.ID)
	}
}

func TestBoardListNodeCardsAllowsMutationBetweenOffsetRequests(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := []startedCurrentNodeViewTask{
		fixture.startTask(t, "Board A"),
		fixture.startTask(t, "Board B"),
		fixture.startTask(t, "Board C"),
	}
	fixture.setTaskUpdatedAt(t, started[0].task.ID, 3_000)
	fixture.setTaskUpdatedAt(t, started[1].task.ID, 2_000)
	fixture.setTaskUpdatedAt(t, started[2].task.ID, 1_000)
	request := serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(fixture.agentNodeID),
		PageSize:   1,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	}
	first, err := fixture.board.ListNodeCards(fixture.ctx, request)
	if err != nil {
		t.Fatalf("first board page: %v", err)
	}
	if first.NextOffset == nil {
		t.Fatal("first board page has no next offset")
	}
	fixture.setTaskUpdatedAt(t, started[2].task.ID, 4_000)
	request.Offset = first.NextOffset
	if _, err := fixture.board.ListNodeCards(fixture.ctx, request); err != nil {
		t.Fatalf("board page after task mutation: %v", err)
	}
}

func TestBoardListNodeCardsDependencyFilterRunsBeforePagination(t *testing.T) {
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
	for index, filter := range filters {
		page, err := fixture.board.ListNodeCards(fixture.ctx, requestFor(filter))
		if err != nil {
			t.Fatalf("ListNodeCards filter %d: %v", index, err)
		}
		if len(page.Cards) != 1 || page.NextOffset == nil {
			t.Fatalf("filter %d page = %+v, want one card and next offset", index, page)
		}
	}

	unblockedPage, err := fixture.board.ListNodeCards(fixture.ctx, requestFor(filterValue(true)))
	if err != nil {
		t.Fatalf("ListNodeCards unblocked first page: %v", err)
	}
	if unblockedPage.Cards[0].TaskID != string(noDependencies.task.ID) {
		t.Fatalf("unblocked first page card = %q, want no-dependency task %q", unblockedPage.Cards[0].TaskID, noDependencies.task.ID)
	}
	unblockedRequest := requestFor(filterValue(true))
	unblockedRequest.Offset = unblockedPage.NextOffset
	unblockedPage, err = fixture.board.ListNodeCards(fixture.ctx, unblockedRequest)
	if err != nil {
		t.Fatalf("ListNodeCards unblocked second page: %v", err)
	}
	if len(unblockedPage.Cards) != 1 || unblockedPage.Cards[0].TaskID != string(satisfied.task.ID) {
		t.Fatalf("unblocked second page = %+v, want satisfied task %q", unblockedPage.Cards, satisfied.task.ID)
	}
}
