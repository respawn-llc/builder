package workflowview

import (
	"core/internal/testharness/workflowtest"
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
	if card.TaskID != string(started.task.ID) ||
		len(card.ActiveNodeIDs) != 1 ||
		card.ActiveNodeIDs[0] != string(fixture.agentNodeID) ||
		card.Status.Kind != serverapi.WorkflowTaskStatusKindActive ||
		card.Actions.CanStart {
		t.Fatalf("board card = %+v, want started Current Node projection", card)
	}
}

func TestBoardMembershipUsesPublishedSuccessorCurrentNode(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Published successor")
	definition, _, err := fixture.store.GetDefinition(t.Context(), fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	successorNodeID := terminalNodeID(t, definition)
	if _, err := workflowtest.ManualMoveTask(fixture.store, t.Context(), workflowstore.ManualMoveRequest{
		TaskID:       started.task.ID,
		TargetNodeID: successorNodeID,
	}); err != nil {
		t.Fatalf("ManualMoveTask successor: %v", err)
	}
	board := fixture.board
	request := serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(successorNodeID),
		PageSize:   20,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	}
	successorPage, err := board.ListNodeCards(t.Context(), request)
	if err != nil {
		t.Fatalf("ListNodeCards successor: %v", err)
	}
	if len(successorPage.Cards) != 1 ||
		successorPage.Cards[0].TaskID != string(started.task.ID) ||
		!equalStrings(successorPage.Cards[0].ActiveNodeIDs, []string{string(successorNodeID)}) {
		t.Fatalf("successor page = %+v, want the complete pinned successor publication", successorPage)
	}

	request.NodeID = string(fixture.agentNodeID)
	predecessorPage, err := board.ListNodeCards(t.Context(), request)
	if err != nil {
		t.Fatalf("ListNodeCards predecessor: %v", err)
	}
	if len(predecessorPage.Cards) != 0 {
		t.Fatalf("predecessor page = %+v, want no card after successor publication", predecessorPage)
	}
}

func TestBoardColumnCountsUsePublishedSuccessorCurrentNode(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "Published successor count")
	definition, _, err := fixture.store.GetDefinition(t.Context(), fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	successorNodeID := terminalNodeID(t, definition)
	if _, err := workflowtest.ManualMoveTask(fixture.store, t.Context(), workflowstore.ManualMoveRequest{
		TaskID:       started.task.ID,
		TargetNodeID: successorNodeID,
	}); err != nil {
		t.Fatalf("ManualMoveTask successor: %v", err)
	}
	board, err := fixture.board.Get(t.Context(), serverapi.WorkflowBoardRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: &fixture.workflowID,
		LabelFilter: serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNone,
		},
	})
	if err != nil {
		t.Fatalf("Board.Get: %v", err)
	}
	predecessorColumn := workflowViewBoardColumn(t, board, fixture.agentNodeID)
	successorColumn := workflowViewBoardColumn(t, board, successorNodeID)
	if predecessorColumn.TaskCount != 0 || successorColumn.TaskCount != 1 {
		t.Fatalf(
			"published column counts = predecessor:%d successor:%d, want 0/1",
			predecessorColumn.TaskCount,
			successorColumn.TaskCount,
		)
	}
}

func TestBoardDependencyFilterUsesPinnedQueuedBlockerBeforePagination(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	blocked := fixture.startTask(t, "Pinned blocked task")
	blocker := fixture.startTask(t, "Durably done blocker")
	if _, err := fixture.store.AddTaskDependency(t.Context(), workflowstore.TaskDependencyAddRequest{
		BlockerTaskID: blocker.task.ID,
		BlockedTaskID: blocked.task.ID,
	}); err != nil {
		t.Fatalf("AddTaskDependency: %v", err)
	}
	definition, _, err := fixture.store.GetDefinition(t.Context(), fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := workflowtest.ManualMoveTask(fixture.store, t.Context(), workflowstore.ManualMoveRequest{
		TaskID:       blocker.task.ID,
		TargetNodeID: terminalNodeID(t, definition),
	}); err != nil {
		t.Fatalf("ManualMoveTask blocker: %v", err)
	}
	board := boardWithStaticLifecycleObservation(t, fixture, workflowexecution.WorkflowTaskExecutionObservation{
		Lifecycle: map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot{
			blocker.task.ID: {
				CurrentNodes:       []workflow.CurrentNode{{Reference: blocker.currentNode}},
				QueuedCurrentNodes: []workflow.CurrentNodeReference{blocker.currentNode},
			},
		},
		Quiescence: map[workflow.TaskID]bool{blocker.task.ID: false},
	})
	page, err := board.ListNodeCards(t.Context(), serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:        fixture.binding.ProjectID,
		WorkflowID:       fixture.workflowID,
		NodeID:           string(fixture.agentNodeID),
		PageSize:         1,
		DependencyFilter: boolPointerForTest(false),
		LabelFilter:      serverapi.WorkflowTaskLabelFilterNone(),
	})
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	if len(page.Cards) != 1 ||
		page.Cards[0].TaskID != string(blocked.task.ID) ||
		page.Cards[0].DependencyProgress == nil ||
		page.Cards[0].DependencyProgress.SatisfiedCount != 0 ||
		page.Cards[0].DependencyProgress.TotalCount != 1 {
		t.Fatalf("blocked page = %+v, want pinned queued blocker with 0/1 progress", page)
	}
}

func TestBoardPaginationUsesCompleteRunningOrSuccessorPublicationInBothDirections(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := []startedCurrentNodeViewTask{
		fixture.startTask(t, "Boundary A"),
		fixture.startTask(t, "Boundary B"),
		fixture.startTask(t, "Boundary C"),
		fixture.startTask(t, "Boundary D"),
	}
	for index, task := range started {
		fixture.setTaskUpdatedAt(t, task.task.ID, int64(index+1)*1_000)
	}
	transitioning := started[1]
	scopeID := runtimeids.NewExecutionScopeID()
	priorBoard := boardWithStaticLifecycleObservation(t, fixture, workflowexecution.WorkflowTaskExecutionObservation{
		Lifecycle: map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot{
			transitioning.task.ID: {
				CurrentNodes: []workflow.CurrentNode{{Reference: transitioning.currentNode}},
				ExactExecutions: []workflowstore.LifecycleExactExecution{{
					CurrentNode: transitioning.currentNode,
					ScopeID:     scopeID,
				}},
			},
		},
		Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
			transitioning.task.ID: {
				Executions: []sessionruntime.TaskExecution{{
					Ref: sessionruntime.WorkflowExecutionRef{
						ProjectID:   fixture.binding.ProjectID,
						WorkflowID:  fixture.workflowID,
						CurrentNode: transitioning.currentNode,
					},
					ScopeID: scopeID,
					Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
				}},
			},
		},
		Quiescence: map[workflow.TaskID]bool{transitioning.task.ID: false},
	})
	definition, _, err := fixture.store.GetDefinition(t.Context(), fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	successorNodeID := terminalNodeID(t, definition)
	priorAscending := []string{
		string(started[0].task.ID),
		string(started[1].task.ID),
		string(started[2].task.ID),
		string(started[3].task.ID),
	}
	nextAscending := []string{
		string(started[0].task.ID),
		string(started[2].task.ID),
		string(started[3].task.ID),
	}
	testCases := []struct {
		name      string
		direction serverapi.WorkflowTaskListSortDirection
		priorWant []string
		nextWant  []string
	}{
		{
			name:      "forward",
			direction: serverapi.WorkflowTaskListSortDirectionAsc,
			priorWant: priorAscending,
			nextWant:  nextAscending,
		},
		{
			name:      "backward",
			direction: serverapi.WorkflowTaskListSortDirectionDesc,
			priorWant: reversedStrings(priorAscending),
			nextWant:  reversedStrings(nextAscending),
		},
	}
	priorByName := make(map[string][]string, len(testCases))
	for _, testCase := range testCases {
		priorByName[testCase.name] = collectBoardNodeCardIDs(
			t,
			priorBoard,
			fixture,
			fixture.agentNodeID,
			testCase.direction,
		)
	}
	if _, err := workflowtest.ManualMoveTask(fixture.store, t.Context(), workflowstore.ManualMoveRequest{
		TaskID:       transitioning.task.ID,
		TargetNodeID: successorNodeID,
	}); err != nil {
		t.Fatalf("ManualMoveTask successor: %v", err)
	}
	nextBoard := fixture.board
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			prior := priorByName[testCase.name]
			if !equalStrings(prior, testCase.priorWant) {
				t.Fatalf("prior page traversal = %v, want %v", prior, testCase.priorWant)
			}
			next := collectBoardNodeCardIDs(
				t,
				nextBoard,
				fixture,
				fixture.agentNodeID,
				testCase.direction,
			)
			if !equalStrings(next, testCase.nextWant) {
				t.Fatalf("next page traversal = %v, want %v", next, testCase.nextWant)
			}
		})
	}
	successorCards := collectBoardNodeCardIDs(
		t,
		nextBoard,
		fixture,
		successorNodeID,
		serverapi.WorkflowTaskListSortDirectionAsc,
	)
	if !equalStrings(successorCards, []string{string(transitioning.task.ID)}) {
		t.Fatalf("successor page traversal = %v, want transitioning Task once", successorCards)
	}
}

func TestBoardPaginationAddsStoppedToQueuedTaskOnlyInCompleteNextPublication(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	first := fixture.startTask(t, "Queued boundary A")
	transitioning := fixture.createBacklogTask(t, "Queued boundary B")
	last := fixture.startTask(t, "Queued boundary C")
	fixture.setTaskUpdatedAt(t, first.task.ID, 1_000)
	fixture.setTaskUpdatedAt(t, transitioning.ID, 2_000)
	fixture.setTaskUpdatedAt(t, last.task.ID, 3_000)
	priorBoard := boardWithStaticLifecycleObservation(
		t,
		fixture,
		workflowexecution.WorkflowTaskExecutionObservation{},
	)
	priorAscending := []string{string(first.task.ID), string(last.task.ID)}
	nextAscending := []string{string(first.task.ID), string(transitioning.ID), string(last.task.ID)}
	testCases := []struct {
		name      string
		direction serverapi.WorkflowTaskListSortDirection
		priorWant []string
		nextWant  []string
	}{
		{
			name:      "forward",
			direction: serverapi.WorkflowTaskListSortDirectionAsc,
			priorWant: priorAscending,
			nextWant:  nextAscending,
		},
		{
			name:      "backward",
			direction: serverapi.WorkflowTaskListSortDirectionDesc,
			priorWant: reversedStrings(priorAscending),
			nextWant:  reversedStrings(nextAscending),
		},
	}
	priorByName := make(map[string][]string, len(testCases))
	for _, testCase := range testCases {
		priorByName[testCase.name] = collectBoardNodeCardIDs(
			t,
			priorBoard,
			fixture,
			fixture.agentNodeID,
			testCase.direction,
		)
	}
	fixture.startExistingTask(t, transitioning)
	fixture.setTaskUpdatedAt(t, transitioning.ID, 2_000)
	nextBoard := fixture.board
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			prior := priorByName[testCase.name]
			if !equalStrings(prior, testCase.priorWant) {
				t.Fatalf("prior page traversal = %v, want %v", prior, testCase.priorWant)
			}
			next := collectBoardNodeCardIDs(t, nextBoard, fixture, fixture.agentNodeID, testCase.direction)
			if !equalStrings(next, testCase.nextWant) {
				t.Fatalf("next page traversal = %v, want %v", next, testCase.nextWant)
			}
		})
	}
}

func TestBoardPaginationRetainsTaskAcrossQueuedOrRunningToStoppedPublication(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := []startedCurrentNodeViewTask{
		fixture.startTask(t, "Stopped boundary A"),
		fixture.startTask(t, "Stopped boundary B"),
		fixture.startTask(t, "Stopped boundary C"),
	}
	for index, task := range started {
		fixture.setTaskUpdatedAt(t, task.task.ID, int64(index+1)*1_000)
	}
	transitioning := started[1]
	if err := fixture.store.InterruptCurrentNode(
		t.Context(),
		transitioning.currentNode,
		workflow.CurrentNodeInterruptionReason("server_restart"),
		workflow.CurrentNodeInterruptionDetail{Code: "restart"},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	stoppedBoard := boardWithStaticLifecycleObservation(
		t,
		fixture,
		workflowexecution.WorkflowTaskExecutionObservation{},
	)
	queuedBoard := boardWithStaticLifecycleObservation(t, fixture, workflowexecution.WorkflowTaskExecutionObservation{
		Lifecycle: map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot{
			transitioning.task.ID: {
				CurrentNodes:       []workflow.CurrentNode{{Reference: transitioning.currentNode}},
				QueuedCurrentNodes: []workflow.CurrentNodeReference{transitioning.currentNode},
			},
		},
		Quiescence: map[workflow.TaskID]bool{transitioning.task.ID: false},
	})
	scopeID := runtimeids.NewExecutionScopeID()
	runningBoard := boardWithStaticLifecycleObservation(t, fixture, workflowexecution.WorkflowTaskExecutionObservation{
		Lifecycle: map[workflow.TaskID]workflowexecution.WorkflowTaskLifecycleSnapshot{
			transitioning.task.ID: {
				CurrentNodes: []workflow.CurrentNode{{Reference: transitioning.currentNode}},
				ExactExecutions: []workflowstore.LifecycleExactExecution{{
					CurrentNode: transitioning.currentNode,
					ScopeID:     scopeID,
				}},
			},
		},
		Executions: map[workflow.TaskID]sessionruntime.TaskExecutionSnapshot{
			transitioning.task.ID: {
				Executions: []sessionruntime.TaskExecution{{
					Ref: sessionruntime.WorkflowExecutionRef{
						ProjectID:   fixture.binding.ProjectID,
						WorkflowID:  fixture.workflowID,
						CurrentNode: transitioning.currentNode,
					},
					ScopeID: scopeID,
					Agent:   &sessionruntime.TaskAgentExecutionTarget{SessionID: runtimeids.NewSessionID()},
				}},
			},
		},
		Quiescence: map[workflow.TaskID]bool{transitioning.task.ID: false},
	})
	ascending := []string{
		string(started[0].task.ID),
		string(started[1].task.ID),
		string(started[2].task.ID),
	}
	for _, prior := range []struct {
		name  string
		board *Board
	}{
		{name: "queued", board: queuedBoard},
		{name: "running", board: runningBoard},
	} {
		t.Run(prior.name, func(t *testing.T) {
			for _, direction := range []serverapi.WorkflowTaskListSortDirection{
				serverapi.WorkflowTaskListSortDirectionAsc,
				serverapi.WorkflowTaskListSortDirectionDesc,
			} {
				want := ascending
				if direction == serverapi.WorkflowTaskListSortDirectionDesc {
					want = reversedStrings(ascending)
				}
				before := collectBoardNodeCardIDs(t, prior.board, fixture, fixture.agentNodeID, direction)
				if !equalStrings(before, want) {
					t.Fatalf("%s prior traversal = %v, want %v", direction, before, want)
				}
				after := collectBoardNodeCardIDs(t, stoppedBoard, fixture, fixture.agentNodeID, direction)
				if !equalStrings(after, want) {
					t.Fatalf("%s stopped traversal = %v, want %v", direction, after, want)
				}
			}
		})
	}
}

func collectBoardNodeCardIDs(
	t *testing.T,
	board *Board,
	fixture currentNodeViewFixture,
	nodeID workflow.NodeID,
	direction serverapi.WorkflowTaskListSortDirection,
) []string {
	t.Helper()
	request := serverapi.WorkflowBoardNodeCardsListRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: fixture.workflowID,
		NodeID:     string(nodeID),
		PageSize:   1,
		Sort: &serverapi.WorkflowTaskListSort{
			Field:     serverapi.WorkflowTaskListSortFieldUpdated,
			Direction: direction,
		},
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
	}
	var taskIDs []string
	seen := map[string]bool{}
	for {
		page, err := board.ListNodeCards(t.Context(), request)
		if err != nil {
			t.Fatalf("ListNodeCards offset %v: %v", request.Offset, err)
		}
		for _, card := range page.Cards {
			if seen[card.TaskID] {
				t.Fatalf("page traversal duplicated Task %q", card.TaskID)
			}
			seen[card.TaskID] = true
			taskIDs = append(taskIDs, card.TaskID)
		}
		if page.NextOffset == nil {
			return taskIDs
		}
		request.Offset = page.NextOffset
	}
}

func reversedStrings(values []string) []string {
	reversed := append([]string(nil), values...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func boardWithStaticLifecycleObservation(
	t *testing.T,
	fixture currentNodeViewFixture,
	observation workflowexecution.WorkflowTaskExecutionObservation,
) *Board {
	t.Helper()
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{store: fixture.store, observation: observation},
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
	return board
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
			store: fixture.store,
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
	if _, err := workflowtest.ManualMoveTask(fixture.store, fixture.ctx, workflowstore.ManualMoveRequest{
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
