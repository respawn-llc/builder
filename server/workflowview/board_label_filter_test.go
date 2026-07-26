package workflowview

import (
	"errors"
	"strconv"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func requireBoardCardIDs(t *testing.T, cards []serverapi.WorkflowBoardTaskCard, want ...string) {
	t.Helper()
	got := make(map[string]bool, len(cards))
	for _, card := range cards {
		got[card.TaskID] = true
	}
	wantSet := make(map[string]bool, len(want))
	for _, taskID := range want {
		wantSet[taskID] = true
	}
	if len(got) != len(wantSet) {
		t.Fatalf("filtered card ids = %v, want %v", got, wantSet)
	}
	for taskID := range wantSet {
		if !got[taskID] {
			t.Fatalf("filtered card ids = %v, missing %s", got, taskID)
		}
	}
}

func TestBoardNamedAnyLabelFilterMatchesCountsAndFirstPageCards(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	filter := namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		fixture.alpha.String(),
		fixture.beta.String(),
	)

	board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:   fixture.projectID,
		LabelFilter: filter,
	})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	if backlog.TaskCount != 3 {
		t.Fatalf("filtered backlog count = %d, want 3", backlog.TaskCount)
	}

	page, err := fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:   fixture.projectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      backlog.Node.NodeID,
			LabelFilter: filter,
		},
	)
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	requireBoardCardIDs(
		t,
		page.Cards,
		fixture.taskIDs["alpha"],
		fixture.taskIDs["beta"],
		fixture.taskIDs["both"],
	)
}

func TestBoardNamedAllLabelFilterMatchesCountsAndFirstPageCards(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	filter := namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAll,
		fixture.alpha.String(),
		fixture.beta.String(),
	)

	board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:   fixture.projectID,
		LabelFilter: filter,
	})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	if backlog.TaskCount != 1 {
		t.Fatalf("filtered backlog count = %d, want 1", backlog.TaskCount)
	}

	page, err := fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:   fixture.projectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      backlog.Node.NodeID,
			LabelFilter: filter,
		},
	)
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	requireBoardCardIDs(t, page.Cards, fixture.taskIDs["both"])
}

func TestBoardNamedFilterMatchesIncludedAndExcludedConditions(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	for _, tt := range []struct {
		name   string
		filter serverapi.WorkflowTaskLabelFilter
		want   []string
	}{
		{
			name: "mixed OR",
			filter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAny,
				[]string{fixture.gamma.String()},
				[]string{fixture.alpha.String(), fixture.beta.String()},
			),
			want: []string{
				fixture.taskIDs["alpha"],
				fixture.taskIDs["beta"],
				fixture.taskIDs["gamma"],
				fixture.taskIDs["unlabeled"],
			},
		},
		{
			name: "mixed AND",
			filter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAll,
				[]string{fixture.gamma.String()},
				[]string{fixture.alpha.String(), fixture.beta.String()},
			),
			want: []string{fixture.taskIDs["gamma"]},
		},
		{
			name: "excluded-only OR",
			filter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAny,
				nil,
				[]string{fixture.alpha.String(), fixture.beta.String()},
			),
			want: []string{
				fixture.taskIDs["alpha"],
				fixture.taskIDs["beta"],
				fixture.taskIDs["gamma"],
				fixture.taskIDs["unlabeled"],
			},
		},
		{
			name: "excluded-only AND",
			filter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAll,
				nil,
				[]string{fixture.alpha.String(), fixture.beta.String()},
			),
			want: []string{
				fixture.taskIDs["gamma"],
				fixture.taskIDs["unlabeled"],
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
				ProjectID:   fixture.projectID,
				LabelFilter: tt.filter,
			})
			if err != nil {
				t.Fatalf("Get board: %v", err)
			}
			backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
			if backlog.TaskCount != len(tt.want) {
				t.Fatalf("filtered backlog count = %d, want %d", backlog.TaskCount, len(tt.want))
			}
			page, err := fixture.view.board(t).ListNodeCards(
				fixture.ctx,
				serverapi.WorkflowBoardNodeCardsListRequest{
					ProjectID:   fixture.projectID,
					WorkflowID:  string(fixture.workflowID),
					NodeID:      backlog.Node.NodeID,
					LabelFilter: tt.filter,
				},
			)
			if err != nil {
				t.Fatalf("ListNodeCards: %v", err)
			}
			requireBoardCardIDs(t, page.Cards, tt.want...)
		})
	}
}

func TestBoardUnlabeledFilterMatchesCountsAndFirstPageCards(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	filter := serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindUnlabeled,
	}

	board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:   fixture.projectID,
		LabelFilter: filter,
	})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	if backlog.TaskCount != 1 {
		t.Fatalf("filtered backlog count = %d, want 1", backlog.TaskCount)
	}

	page, err := fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:   fixture.projectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      backlog.Node.NodeID,
			LabelFilter: filter,
		},
	)
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	requireBoardCardIDs(t, page.Cards, fixture.taskIDs["unlabeled"])
}

func TestBoardNodeCardsRejectPageTokenFromAnotherLabelExpression(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	firstFilter := namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		fixture.alpha.String(),
		fixture.beta.String(),
	)
	board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:   fixture.projectID,
		LabelFilter: firstFilter,
	})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	firstPage, err := fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:   fixture.projectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      backlog.Node.NodeID,
			PageSize:    1,
			LabelFilter: firstFilter,
		},
	)
	if err != nil {
		t.Fatalf("ListNodeCards first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("first page did not produce an older-page token")
	}

	_, err = fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:  fixture.projectID,
			WorkflowID: string(fixture.workflowID),
			NodeID:     backlog.Node.NodeID,
			PageSize:   1,
			PageToken:  firstPage.NextPageToken,
			LabelFilter: namedTaskLabelFilter(
				serverapi.WorkflowTaskNamedLabelFilterModeAny,
				fixture.gamma.String(),
			),
		},
	)
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("changed label expression error = %v, want ErrInvalidPageToken", err)
	}
}

func TestBoardNodeCardsRejectPageTokenFromChangedExcludedConditions(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	firstFilter := namedTaskLabelFilterWithExclusions(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		nil,
		[]string{fixture.alpha.String()},
	)
	board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:   fixture.projectID,
		LabelFilter: firstFilter,
	})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	firstPage, err := fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:   fixture.projectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      backlog.Node.NodeID,
			PageSize:    1,
			LabelFilter: firstFilter,
		},
	)
	if err != nil {
		t.Fatalf("ListNodeCards first page: %v", err)
	}
	if firstPage.NextPageToken == nil {
		t.Fatal("first page did not produce an older-page token")
	}

	_, err = fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:  fixture.projectID,
			WorkflowID: string(fixture.workflowID),
			NodeID:     backlog.Node.NodeID,
			PageSize:   1,
			PageToken:  firstPage.NextPageToken,
			LabelFilter: namedTaskLabelFilterWithExclusions(
				serverapi.WorkflowTaskNamedLabelFilterModeAny,
				nil,
				[]string{fixture.beta.String()},
			),
		},
	)
	if !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("changed excluded conditions error = %v, want ErrInvalidPageToken", err)
	}
}

func TestBoardNodeCardsKeepCanonicalFilterIdentityInBothDirections(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	firstFilter := namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		fixture.beta.String(),
		fixture.alpha.String(),
	)
	board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:   fixture.projectID,
		LabelFilter: firstFilter,
	})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	list := func(pageToken *string, filter serverapi.WorkflowTaskLabelFilter) serverapi.WorkflowBoardNodeCardsListResponse {
		t.Helper()
		page, err := fixture.view.board(t).ListNodeCards(
			fixture.ctx,
			serverapi.WorkflowBoardNodeCardsListRequest{
				ProjectID:   fixture.projectID,
				WorkflowID:  string(fixture.workflowID),
				NodeID:      backlog.Node.NodeID,
				PageSize:    1,
				PageToken:   pageToken,
				LabelFilter: filter,
			},
		)
		if err != nil {
			t.Fatalf("ListNodeCards: %v", err)
		}
		if len(page.Cards) != 1 {
			t.Fatalf("page cards = %+v, want one", page.Cards)
		}
		return page
	}

	canonicalFilter := namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		fixture.alpha.String(),
		fixture.beta.String(),
	)
	first := list(nil, firstFilter)
	second := list(first.NextPageToken, canonicalFilter)
	third := list(second.NextPageToken, canonicalFilter)
	if first.PreviousPageToken != nil ||
		first.NextPageToken == nil ||
		second.PreviousPageToken == nil ||
		second.NextPageToken == nil ||
		third.PreviousPageToken == nil ||
		third.NextPageToken != nil {
		t.Fatalf("filtered cursors = first:%+v second:%+v third:%+v", first, second, third)
	}
	seen := map[string]bool{
		first.Cards[0].TaskID:  true,
		second.Cards[0].TaskID: true,
		third.Cards[0].TaskID:  true,
	}
	want := map[string]bool{
		fixture.taskIDs["alpha"]: true,
		fixture.taskIDs["beta"]:  true,
		fixture.taskIDs["both"]:  true,
	}
	if len(seen) != len(want) {
		t.Fatalf("filtered page ids = %v, want %v", seen, want)
	}
	for taskID := range want {
		if !seen[taskID] {
			t.Fatalf("filtered page ids = %v, missing %s", seen, taskID)
		}
	}

	newer := list(third.PreviousPageToken, canonicalFilter)
	if newer.Cards[0].TaskID != second.Cards[0].TaskID {
		t.Fatalf("newer page task = %s, want %s", newer.Cards[0].TaskID, second.Cards[0].TaskID)
	}
}

func TestBoardLabelFilterFindsRareMatchesBeforePaginationAndKeepsEmptyColumnsEmpty(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	for index := 0; index < serverapi.WorkflowBoardNodeCardsMaxPageSize+5; index++ {
		if _, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
			ProjectID: fixture.projectID,
			Title:     "unmatched-" + strconv.Itoa(index),
		}); err != nil {
			t.Fatalf("CreateTask unmatched %d: %v", index, err)
		}
	}
	filter := namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		fixture.gamma.String(),
	)

	board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
		ProjectID:   fixture.projectID,
		LabelFilter: filter,
	})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	agent := workflowViewColumnByKey(t, board, "agent")
	if backlog.TaskCount != 1 || agent.TaskCount != 0 {
		t.Fatalf("filtered counts = backlog:%d agent:%d, want 1/0", backlog.TaskCount, agent.TaskCount)
	}

	backlogPage, err := fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:   fixture.projectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      backlog.Node.NodeID,
			PageSize:    1,
			LabelFilter: filter,
		},
	)
	if err != nil {
		t.Fatalf("ListNodeCards backlog: %v", err)
	}
	requireBoardCardIDs(t, backlogPage.Cards, fixture.taskIDs["gamma"])
	if backlogPage.PreviousPageToken != nil || backlogPage.NextPageToken != nil {
		t.Fatalf("rare-match cursors = previous:%v next:%v, want none", backlogPage.PreviousPageToken, backlogPage.NextPageToken)
	}
	agentPage, err := fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:   fixture.projectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      agent.Node.NodeID,
			PageSize:    1,
			LabelFilter: filter,
		},
	)
	if err != nil {
		t.Fatalf("ListNodeCards agent: %v", err)
	}
	if len(agentPage.Cards) != 0 {
		t.Fatalf("empty filtered column cards = %+v, want none", agentPage.Cards)
	}
}

func TestBoardLabelFilterPreservesTerminalCanceledAndPendingApprovalPlacements(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	doneLabel, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.projectID, "done-only")
	if err != nil {
		t.Fatalf("CreateProjectLabel done-only: %v", err)
	}
	canceledLabel, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.projectID, "canceled-only")
	if err != nil {
		t.Fatalf("CreateProjectLabel canceled-only: %v", err)
	}
	pendingLabel, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.projectID, "pending-only")
	if err != nil {
		t.Fatalf("CreateProjectLabel pending-only: %v", err)
	}
	createTask := func(title string, labelID string) workflow.TaskID {
		t.Helper()
		task, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
			ProjectID: fixture.projectID,
			Title:     title,
			LabelIDs:  []string{labelID},
		})
		if err != nil {
			t.Fatalf("CreateTask %s: %v", title, err)
		}
		return task.ID
	}

	doneTaskID := createTask("done-only", doneLabel.ID.String())
	doneStarted, err := fixture.store.StartTask(fixture.ctx, doneTaskID)
	if err != nil {
		t.Fatalf("StartTask done-only: %v", err)
	}
	if _, err := fixture.store.CompleteRun(fixture.ctx, workflowstore.CompleteRunRequest{
		RunID:        doneStarted.RunID,
		TransitionID: "done",
	}); err != nil {
		t.Fatalf("CompleteRun done-only: %v", err)
	}

	canceledTaskID := createTask("canceled-only", canceledLabel.ID.String())
	if _, err := fixture.store.CancelTask(fixture.ctx, canceledTaskID, "stop"); err != nil {
		t.Fatalf("CancelTask canceled-only: %v", err)
	}
	forceCanceledBacklogPlacementWithoutTerminal(
		t,
		fixture.ctx,
		fixture.metadata,
		canceledTaskID,
		fixture.workflowID,
	)

	requireDoneTransitionApproval(t, fixture.ctx, fixture.metadata, fixture.workflowID)
	pendingTaskID := createTask("pending-only", pendingLabel.ID.String())
	pendingStarted, err := fixture.store.StartTask(fixture.ctx, pendingTaskID)
	if err != nil {
		t.Fatalf("StartTask pending-only: %v", err)
	}
	if _, err := fixture.store.CompleteRun(fixture.ctx, workflowstore.CompleteRunRequest{
		RunID:        pendingStarted.RunID,
		TransitionID: "done",
	}); err != nil {
		t.Fatalf("CompleteRun pending-only: %v", err)
	}

	tests := []struct {
		name      string
		labelID   string
		columnKey string
		taskID    workflow.TaskID
	}{
		{name: "terminal", labelID: doneLabel.ID.String(), columnKey: "done", taskID: doneTaskID},
		{name: "canceled", labelID: canceledLabel.ID.String(), columnKey: "done", taskID: canceledTaskID},
		{name: "pending approval", labelID: pendingLabel.ID.String(), columnKey: "agent", taskID: pendingTaskID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter := namedTaskLabelFilter(
				serverapi.WorkflowTaskNamedLabelFilterModeAny,
				test.labelID,
			)
			board, err := fixture.view.board(t).Get(fixture.ctx, serverapi.WorkflowBoardRequest{
				ProjectID:   fixture.projectID,
				LabelFilter: filter,
			})
			if err != nil {
				t.Fatalf("Get board: %v", err)
			}
			column := workflowViewColumnByKey(t, board, test.columnKey)
			if column.TaskCount != 1 {
				t.Fatalf("%s count = %d, want 1", test.columnKey, column.TaskCount)
			}
			page, err := fixture.view.board(t).ListNodeCards(
				fixture.ctx,
				serverapi.WorkflowBoardNodeCardsListRequest{
					ProjectID:   fixture.projectID,
					WorkflowID:  string(fixture.workflowID),
					NodeID:      column.Node.NodeID,
					LabelFilter: filter,
				},
			)
			if err != nil {
				t.Fatalf("ListNodeCards: %v", err)
			}
			requireBoardCardIDs(t, page.Cards, string(test.taskID))
		})
	}
}

func TestBoardLabelFilterRejectsMoreThanOneHundredIDs(t *testing.T) {
	fixture := newTaskListLabelFilterFixture(t)
	filter := namedTaskLabelFilter(
		serverapi.WorkflowTaskNamedLabelFilterModeAny,
		make([]string, serverapi.WorkflowLabelMaxIDs+1)...,
	)
	boardRequest := serverapi.WorkflowBoardRequest{
		ProjectID:   fixture.projectID,
		LabelFilter: filter,
	}
	_, boardErr := fixture.view.board(t).Get(fixture.ctx, boardRequest)
	var boardValidationErr *serverapi.WorkflowLabelError
	if !errors.As(boardErr, &boardValidationErr) ||
		boardValidationErr.Reason != serverapi.WorkflowLabelErrorReasonInvalidFilter ||
		boardValidationErr.Field == nil ||
		*boardValidationErr.Field != "label_filter.label_ids" {
		t.Fatalf("board 101-ID error = %+v", boardErr)
	}

	_, cardsErr := fixture.view.board(t).ListNodeCards(
		fixture.ctx,
		serverapi.WorkflowBoardNodeCardsListRequest{
			ProjectID:   fixture.projectID,
			WorkflowID:  string(fixture.workflowID),
			NodeID:      "node-not-reached",
			LabelFilter: filter,
		},
	)
	var cardsValidationErr *serverapi.WorkflowLabelError
	if !errors.As(cardsErr, &cardsValidationErr) ||
		cardsValidationErr.Reason != serverapi.WorkflowLabelErrorReasonInvalidFilter ||
		cardsValidationErr.Field == nil ||
		*cardsValidationErr.Field != "label_filter.label_ids" {
		t.Fatalf("board cards 101-ID error = %+v", cardsErr)
	}
}
