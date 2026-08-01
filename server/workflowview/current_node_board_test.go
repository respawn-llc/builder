package workflowview

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"core/server/workflow"
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

func (f currentNodeViewFixture) setTaskCreatedAt(t *testing.T, taskID workflow.TaskID, unixMs int64) {
	t.Helper()
	if _, err := f.metadata.DB().ExecContext(
		f.ctx,
		`UPDATE tasks SET created_at_unix_ms = ? WHERE id = ?`,
		unixMs,
		string(taskID),
	); err != nil {
		t.Fatalf("set task created at: %v", err)
	}
}

func TestBoardListNodeCardsSupportsScalarSortsAndBidirectionalPagination(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := []startedCurrentNodeViewTask{
		fixture.startTask(t, "beta"),
		fixture.startTask(t, "Alpha"),
		fixture.startTask(t, "alpha"),
		fixture.startTask(t, "gamma"),
	}
	for index, task := range started {
		fixture.setTaskUpdatedAt(t, task.task.ID, []int64{10, 20, 10, 30}[index])
		fixture.setTaskCreatedAt(t, task.task.ID, []int64{30, 10, 10, 20}[index])
	}
	id := func(index int) string { return string(started[index].task.ID) }
	tests := []struct {
		name string
		sort serverapi.WorkflowBoardNodeCardsSort
		want []string
	}{
		{
			name: "updated asc",
			sort: serverapi.WorkflowBoardNodeCardsSort{
				Field:     serverapi.WorkflowBoardNodeCardsSortFieldUpdated,
				Direction: serverapi.WorkflowTaskListSortDirectionAsc,
			},
			want: []string{id(0), id(2), id(1), id(3)},
		},
		{
			name: "updated desc",
			sort: serverapi.WorkflowBoardNodeCardsSort{
				Field:     serverapi.WorkflowBoardNodeCardsSortFieldUpdated,
				Direction: serverapi.WorkflowTaskListSortDirectionDesc,
			},
			want: []string{id(3), id(1), id(2), id(0)},
		},
		{
			name: "created asc",
			sort: serverapi.WorkflowBoardNodeCardsSort{
				Field:     serverapi.WorkflowBoardNodeCardsSortFieldCreated,
				Direction: serverapi.WorkflowTaskListSortDirectionAsc,
			},
			want: []string{id(1), id(2), id(3), id(0)},
		},
		{
			name: "created desc",
			sort: serverapi.WorkflowBoardNodeCardsSort{
				Field:     serverapi.WorkflowBoardNodeCardsSortFieldCreated,
				Direction: serverapi.WorkflowTaskListSortDirectionDesc,
			},
			want: []string{id(0), id(3), id(2), id(1)},
		},
		{
			name: "title asc",
			sort: serverapi.WorkflowBoardNodeCardsSort{
				Field:     serverapi.WorkflowBoardNodeCardsSortFieldTitle,
				Direction: serverapi.WorkflowTaskListSortDirectionAsc,
			},
			want: []string{id(1), id(2), id(0), id(3)},
		},
		{
			name: "title desc",
			sort: serverapi.WorkflowBoardNodeCardsSort{
				Field:     serverapi.WorkflowBoardNodeCardsSortFieldTitle,
				Direction: serverapi.WorkflowTaskListSortDirectionDesc,
			},
			want: []string{id(3), id(0), id(2), id(1)},
		},
		{
			name: "short id asc",
			sort: serverapi.WorkflowBoardNodeCardsSort{
				Field:     serverapi.WorkflowBoardNodeCardsSortFieldShortID,
				Direction: serverapi.WorkflowTaskListSortDirectionAsc,
			},
			want: []string{id(0), id(1), id(2), id(3)},
		},
		{
			name: "short id desc",
			sort: serverapi.WorkflowBoardNodeCardsSort{
				Field:     serverapi.WorkflowBoardNodeCardsSortFieldShortID,
				Direction: serverapi.WorkflowTaskListSortDirectionDesc,
			},
			want: []string{id(3), id(2), id(1), id(0)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := serverapi.WorkflowBoardNodeCardsListRequest{
				ProjectID:  fixture.binding.ProjectID,
				WorkflowID: fixture.workflowID,
				NodeID:     string(fixture.agentNodeID),
				PageSize:   2,
				Sort:       &tt.sort,
				LabelFilter: serverapi.WorkflowTaskLabelFilter{
					Kind: serverapi.WorkflowTaskLabelFilterKindNone,
				},
			}
			var got []string
			var first, second serverapi.WorkflowBoardNodeCardsListResponse
			for pageIndex := 0; ; pageIndex++ {
				page, err := fixture.board.ListNodeCards(fixture.ctx, request)
				if err != nil {
					t.Fatalf("ListNodeCards page %d: %v", pageIndex, err)
				}
				for _, card := range page.Cards {
					got = append(got, card.TaskID)
				}
				switch pageIndex {
				case 0:
					first = page
					if page.PreviousPageToken != nil || page.NextPageToken == nil {
						t.Fatalf("first page tokens = previous %v next %v", page.PreviousPageToken, page.NextPageToken)
					}
				case 1:
					second = page
					if page.PreviousPageToken == nil || page.NextPageToken != nil {
						t.Fatalf("second page tokens = previous %v next %v", page.PreviousPageToken, page.NextPageToken)
					}
				}
				if page.NextPageToken == nil {
					break
				}
				request.PageToken = page.NextPageToken
			}
			if !equalStrings(got, tt.want) {
				t.Fatalf("sorted cards = %v, want %v", got, tt.want)
			}

			request.PageToken = second.PreviousPageToken
			previous, err := fixture.board.ListNodeCards(fixture.ctx, request)
			if err != nil {
				t.Fatalf("ListNodeCards previous: %v", err)
			}
			if len(previous.Cards) != len(first.Cards) ||
				previous.Cards[0].TaskID != first.Cards[0].TaskID ||
				previous.Cards[1].TaskID != first.Cards[1].TaskID {
				t.Fatalf("previous page cards = %+v, want first page %+v", previous.Cards, first.Cards)
			}

			otherSort := tt.sort
			otherSort.Direction = serverapi.WorkflowTaskListSortDirectionAsc
			if otherSort == tt.sort {
				otherSort.Direction = serverapi.WorkflowTaskListSortDirectionDesc
			}
			request.PageToken = first.NextPageToken
			request.Sort = &otherSort
			if _, err := fixture.board.ListNodeCards(fixture.ctx, request); !errors.Is(err, ErrInvalidPageToken) {
				t.Fatalf("board token replay with changed sort error = %v, want invalid page token", err)
			}
		})
	}
}

func TestBoardShortIDPageTokenRejectsExtraAnchorFields(t *testing.T) {
	filter := workflowTaskLabelFilterFacts{
		Kind:             serverapi.WorkflowTaskLabelFilterKindNone,
		LabelIDs:         []string{},
		ExcludedLabelIDs: []string{},
	}
	sort := serverapi.WorkflowBoardNodeCardsSort{
		Field:     serverapi.WorkflowBoardNodeCardsSortFieldShortID,
		Direction: serverapi.WorkflowTaskListSortDirectionAsc,
	}
	updatedAt := int64(100)
	payload := boardNodeCardsPageTokenPayload{
		Version:         boardNodeCardsPageTokenVersion,
		ProjectID:       "project-1",
		WorkflowID:      "workflow-1",
		NodeID:          "node-1",
		LabelFilter:     filter,
		Sort:            sort,
		UpdatedAtUnixMs: &updatedAt,
		TaskSeq:         1,
		Direction:       boardNodeCardsPageDirectionOlder,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal malformed short-id token: %v", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := parseBoardNodeCardsPageToken(
		&token,
		payload.ProjectID,
		payload.WorkflowID,
		payload.NodeID,
		filter,
		sort,
	); !errors.Is(err, ErrInvalidPageToken) {
		t.Fatalf("short-id token with timestamp anchor error = %v, want invalid page token", err)
	}
}
