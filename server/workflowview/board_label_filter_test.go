package workflowview

import (
	"testing"

	"core/server/workflowstore"
	"core/shared/serverapi"
)

type currentNodeLabelFilterFixture struct {
	currentNodeViewFixture
	alpha   string
	beta    string
	gamma   string
	taskIDs map[string]string
}

func newCurrentNodeLabelFilterFixture(t *testing.T) currentNodeLabelFilterFixture {
	t.Helper()
	current := newCurrentNodeViewFixture(t, false)
	alpha, err := current.store.CreateProjectLabel(current.ctx, current.binding.ProjectID, "alpha")
	if err != nil {
		t.Fatalf("CreateProjectLabel alpha: %v", err)
	}
	beta, err := current.store.CreateProjectLabel(current.ctx, current.binding.ProjectID, "beta")
	if err != nil {
		t.Fatalf("CreateProjectLabel beta: %v", err)
	}
	gamma, err := current.store.CreateProjectLabel(current.ctx, current.binding.ProjectID, "gamma")
	if err != nil {
		t.Fatalf("CreateProjectLabel gamma: %v", err)
	}
	startTask := func(title string, labelIDs ...string) string {
		t.Helper()
		workflowID := current.workflowID
		task, createErr := current.store.CreateTask(current.ctx, workflowstore.CreateTaskRequest{
			ProjectID:  current.binding.ProjectID,
			WorkflowID: &workflowID,
			Title:      title,
			LabelIDs:   labelIDs,
		})
		if createErr != nil {
			t.Fatalf("CreateTask %s: %v", title, createErr)
		}
		if _, startErr := current.store.StartTask(current.ctx, task.ID); startErr != nil {
			t.Fatalf("StartTask %s: %v", title, startErr)
		}
		return string(task.ID)
	}
	return currentNodeLabelFilterFixture{
		currentNodeViewFixture: current,
		alpha:                  alpha.ID.String(),
		beta:                   beta.ID.String(),
		gamma:                  gamma.ID.String(),
		taskIDs: map[string]string{
			"alpha":     startTask("alpha", alpha.ID.String()),
			"beta":      startTask("beta", beta.ID.String()),
			"both":      startTask("both", alpha.ID.String(), beta.ID.String()),
			"gamma":     startTask("gamma", gamma.ID.String()),
			"unlabeled": startTask("unlabeled"),
		},
	}
}

type currentNodeLabelFilterCase struct {
	name   string
	filter serverapi.WorkflowTaskLabelFilter
	want   []string
}

func (f currentNodeLabelFilterFixture) exclusionCases() []currentNodeLabelFilterCase {
	named := func(mode serverapi.WorkflowTaskNamedLabelFilterMode, included []string, excluded []string) serverapi.WorkflowTaskLabelFilter {
		return serverapi.WorkflowTaskLabelFilter{
			Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
			Named: &serverapi.WorkflowTaskNamedLabelFilter{
				Mode:             mode,
				LabelIDs:         included,
				ExcludedLabelIDs: excluded,
			},
		}
	}
	return []currentNodeLabelFilterCase{
		{
			name:   "mixed OR",
			filter: named(serverapi.WorkflowTaskNamedLabelFilterModeAny, []string{f.gamma}, []string{f.alpha, f.beta}),
			want:   []string{f.taskIDs["alpha"], f.taskIDs["beta"], f.taskIDs["gamma"], f.taskIDs["unlabeled"]},
		},
		{
			name:   "mixed AND",
			filter: named(serverapi.WorkflowTaskNamedLabelFilterModeAll, []string{f.gamma}, []string{f.alpha, f.beta}),
			want:   []string{f.taskIDs["gamma"]},
		},
		{
			name:   "excluded-only OR",
			filter: named(serverapi.WorkflowTaskNamedLabelFilterModeAny, nil, []string{f.alpha, f.beta}),
			want:   []string{f.taskIDs["alpha"], f.taskIDs["beta"], f.taskIDs["gamma"], f.taskIDs["unlabeled"]},
		},
		{
			name:   "excluded-only AND",
			filter: named(serverapi.WorkflowTaskNamedLabelFilterModeAll, nil, []string{f.alpha, f.beta}),
			want:   []string{f.taskIDs["gamma"], f.taskIDs["unlabeled"]},
		},
	}
}

func requireCurrentNodeBoardCardIDs(t *testing.T, cards []serverapi.WorkflowBoardTaskCard, want []string) {
	t.Helper()
	got := make(map[string]bool, len(cards))
	for _, card := range cards {
		got[card.TaskID] = true
	}
	if len(got) != len(want) {
		t.Fatalf("board card IDs = %v, want %v", got, want)
	}
	for _, taskID := range want {
		if !got[taskID] {
			t.Fatalf("board card IDs = %v, missing %s", got, taskID)
		}
	}
}

func TestCurrentNodeBoardLabelExclusionsFilterCountsAndCards(t *testing.T) {
	fixture := newCurrentNodeLabelFilterFixture(t)
	for _, tt := range fixture.exclusionCases() {
		t.Run(tt.name, func(t *testing.T) {
			board, err := fixture.board.Get(fixture.ctx, serverapi.WorkflowBoardRequest{
				ProjectID:   fixture.binding.ProjectID,
				WorkflowID:  &fixture.workflowID,
				LabelFilter: tt.filter,
			})
			if err != nil {
				t.Fatalf("Board.Get: %v", err)
			}
			column := workflowViewBoardColumn(t, board, fixture.agentNodeID)
			if column.TaskCount != len(tt.want) {
				t.Fatalf("agent column count = %d, want %d", column.TaskCount, len(tt.want))
			}
			page, err := fixture.board.ListNodeCards(fixture.ctx, serverapi.WorkflowBoardNodeCardsListRequest{
				ProjectID:   fixture.binding.ProjectID,
				WorkflowID:  fixture.workflowID,
				NodeID:      string(fixture.agentNodeID),
				LabelFilter: tt.filter,
			})
			if err != nil {
				t.Fatalf("Board.ListNodeCards: %v", err)
			}
			requireCurrentNodeBoardCardIDs(t, page.Cards, tt.want)
		})
	}
}
