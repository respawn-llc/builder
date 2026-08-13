package workflowview

import (
	"testing"

	"core/server/workflow/label"
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
	startTask := func(title string, labelIDs ...label.ID) string {
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
			"alpha":     startTask("alpha", alpha.ID),
			"beta":      startTask("beta", beta.ID),
			"both":      startTask("both", alpha.ID, beta.ID),
			"gamma":     startTask("gamma", gamma.ID),
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

func TestCurrentNodeBoardDependencyFilterCountsAndCombinesWithLabels(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	alpha, err := fixture.store.CreateProjectLabel(fixture.ctx, fixture.binding.ProjectID, "alpha")
	if err != nil {
		t.Fatalf("CreateProjectLabel: %v", err)
	}
	started := func(title string, labelIDs ...label.ID) workflowstore.TaskRecord {
		t.Helper()
		task, createErr := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
			ProjectID:  fixture.binding.ProjectID,
			WorkflowID: &fixture.workflowID,
			Title:      title,
			LabelIDs:   labelIDs,
		})
		if createErr != nil {
			t.Fatalf("CreateTask %q: %v", title, createErr)
		}
		if _, startErr := fixture.store.StartTask(fixture.ctx, task.ID); startErr != nil {
			t.Fatalf("StartTask %q: %v", title, startErr)
		}
		return task
	}
	noDependencies := started("No dependencies", alpha.ID)
	satisfied := started("Satisfied", alpha.ID)
	unsatisfied := started("Unsatisfied", alpha.ID)
	otherLabel := started("Other label")
	satisfiedBlocker := createViewTask(t, fixture, "Satisfied blocker")
	unsatisfiedBlocker := createViewTask(t, fixture, "Unsatisfied blocker")
	for _, dependency := range []workflowstore.TaskDependencyAddRequest{
		{BlockerTaskID: satisfiedBlocker.ID, BlockedTaskID: satisfied.ID},
		{BlockerTaskID: unsatisfiedBlocker.ID, BlockedTaskID: unsatisfied.ID},
	} {
		if _, err := fixture.store.AddTaskDependency(fixture.ctx, dependency); err != nil {
			t.Fatalf("AddTaskDependency: %v", err)
		}
	}
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := fixture.store.ManualMoveTask(fixture.ctx, workflowstore.ManualMoveRequest{
		TaskID:       satisfiedBlocker.ID,
		TargetNodeID: terminalNodeID(t, definition),
	}); err != nil {
		t.Fatalf("ManualMoveTask: %v", err)
	}

	labelFilter := serverapi.WorkflowTaskLabelFilter{
		Kind: serverapi.WorkflowTaskLabelFilterKindNamed,
		Named: &serverapi.WorkflowTaskNamedLabelFilter{
			Mode:     serverapi.WorkflowTaskNamedLabelFilterModeAny,
			LabelIDs: []string{alpha.ID.String()},
		},
	}
	tests := []struct {
		name             string
		dependencyFilter *bool
		labelFilter      serverapi.WorkflowTaskLabelFilter
		wantCount        int
	}{
		{name: "all", wantCount: 4},
		{name: "unblocked", dependencyFilter: boolPointerForTest(true), wantCount: 3},
		{name: "blocked", dependencyFilter: boolPointerForTest(false), wantCount: 1},
		{name: "unblocked and labels", dependencyFilter: boolPointerForTest(true), labelFilter: labelFilter, wantCount: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter := tt.labelFilter
			if filter.Kind == "" {
				filter = serverapi.WorkflowTaskLabelFilterNone()
			}
			board, err := fixture.board.Get(fixture.ctx, serverapi.WorkflowBoardRequest{
				ProjectID:        fixture.binding.ProjectID,
				WorkflowID:       &fixture.workflowID,
				LabelFilter:      filter,
				DependencyFilter: tt.dependencyFilter,
			})
			if err != nil {
				t.Fatalf("Board.Get: %v", err)
			}
			column := workflowViewBoardColumn(t, board, fixture.agentNodeID)
			if column.TaskCount != tt.wantCount {
				t.Fatalf("agent column count = %d, want %d", column.TaskCount, tt.wantCount)
			}
		})
	}
	_ = noDependencies
	_ = unsatisfied
	_ = otherLabel
}

func boolPointerForTest(value bool) *bool {
	return &value
}
