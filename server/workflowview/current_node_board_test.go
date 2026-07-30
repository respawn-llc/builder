package workflowview

import (
	"errors"
	"sort"
	"testing"

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
