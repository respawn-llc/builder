package workflowview

import (
	"context"
	"sort"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestQueuedRunStatusFiltersAndPaginatesAcrossTaskSurfaces(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	ordinary := fixture.startTask(t, "Queued pagination A")
	launching := fixture.startTask(t, "Queued pagination B")
	observation := workflowexecution.WorkflowTaskExecutionObservation{
		Runs: map[workflow.TaskID]workflowexecution.WorkflowTaskRunSnapshot{
			ordinary.task.ID: {
				Queued: []workflow.CurrentNodeReference{ordinary.currentNode},
			},
			launching.task.ID: {
				InterruptibleLaunching: []workflow.CurrentNodeReference{launching.currentNode},
			},
		},
		Quiescence: map[workflow.TaskID]bool{
			ordinary.task.ID:  false,
			launching.task.ID: false,
		},
	}
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{observation: observation},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	definitions := mustDefinitionProjection(t, fixture.store)
	dependencies, err := NewTaskDependencies(fixture.metadata, projection, fixture.dependencyCounter)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	detail, err := NewTaskDetail(fixture.metadata, projection, dependencies)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	tasks, err := NewTaskList(fixture.metadata, definitions, projection)
	if err != nil {
		t.Fatalf("NewTaskList: %v", err)
	}
	board, err := NewBoard(fixture.metadata, definitions, testsetup.QuestionsEnabled("coder"), projection)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	search, err := NewTaskSearch(fixture.metadata, projection)
	if err != nil {
		t.Fatalf("NewTaskSearch: %v", err)
	}

	for _, test := range []struct {
		task          startedCurrentNodeViewTask
		wantInterrupt bool
	}{
		{task: ordinary},
		{task: launching, wantInterrupt: true},
	} {
		projected, err := detail.GetTask(context.Background(), string(test.task.task.ID))
		if err != nil {
			t.Fatalf("TaskDetail.GetTask(%s): %v", test.task.task.ID, err)
		}
		if projected.Status.Kind != serverapi.WorkflowTaskStatusKindQueued ||
			projected.Actions.CanInterrupt != test.wantInterrupt ||
			projected.Actions.CanResume {
			t.Fatalf("queued detail %s = %+v/%+v", test.task.task.ID, projected.Status, projected.Actions)
		}
	}

	projectID := fixture.binding.ProjectID
	limit := 1
	listRequest := serverapi.WorkflowTaskListRequest{
		ProjectID:   &projectID,
		WorkflowID:  &fixture.workflowID,
		StatusKinds: []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindQueued},
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		Limit:       &limit,
	}
	var listedIDs []string
	for {
		page, err := tasks.List(context.Background(), listRequest)
		if err != nil {
			t.Fatalf("TaskList.List: %v", err)
		}
		if len(page.Tasks) != 1 || page.Tasks[0].Status.Kind != serverapi.WorkflowTaskStatusKindQueued {
			t.Fatalf("queued Task List page = %+v", page)
		}
		listedIDs = append(listedIDs, page.Tasks[0].TaskID)
		if page.NextOffset == nil {
			break
		}
		listRequest.Offset = page.NextOffset
	}

	boardRequest := serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:   fixture.binding.ProjectID,
		WorkflowID:  fixture.workflowID,
		NodeID:      string(fixture.agentNodeID),
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		PageSize:    1,
	}
	var boardIDs []string
	for {
		page, err := board.ListNodeCards(context.Background(), boardRequest)
		if err != nil {
			t.Fatalf("Board.ListNodeCards: %v", err)
		}
		if len(page.Cards) != 1 || page.Cards[0].Status.Kind != serverapi.WorkflowTaskStatusKindQueued {
			t.Fatalf("queued Board page = %+v", page)
		}
		boardIDs = append(boardIDs, page.Cards[0].TaskID)
		if page.NextOffset == nil {
			break
		}
		boardRequest.Offset = page.NextOffset
	}

	searchRequest := taskSearchRequest("Queued pagination")
	searchRequest.ProjectIDs = []string{fixture.binding.ProjectID}
	searchRequest.StatusKinds = []serverapi.WorkflowTaskStatusKind{serverapi.WorkflowTaskStatusKindQueued}
	searchRequest.PageSize = 1
	var searchedIDs []string
	for {
		page, err := search.Search(context.Background(), searchRequest)
		if err != nil {
			t.Fatalf("TaskSearch.Search: %v", err)
		}
		if len(page.Groups) != 1 || page.Groups[0].Status.Kind != serverapi.WorkflowTaskStatusKindQueued {
			t.Fatalf("queued Task Search page = %+v", page)
		}
		searchedIDs = append(searchedIDs, page.Groups[0].TaskID)
		if page.NextOffset == nil {
			break
		}
		searchRequest.Offset = page.NextOffset
	}

	wantIDs := []string{string(ordinary.task.ID), string(launching.task.ID)}
	sort.Strings(wantIDs)
	for label, got := range map[string][]string{
		"Task List":   listedIDs,
		"Board":       boardIDs,
		"Task Search": searchedIDs,
	} {
		sort.Strings(got)
		if !equalStrings(got, wantIDs) {
			t.Fatalf("%s queued pagination IDs = %v, want %v", label, got, wantIDs)
		}
	}
}

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
		fixture.metadata,
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
