package workflowview

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

type countingTaskLabelAssignmentReader struct {
	calls   int
	taskIDs []string
	rows    []sqlitegen.ListTaskAssignedLabelIDsByTasksRow
}

func (r *countingTaskLabelAssignmentReader) ListTaskAssignedLabelIDsByTasks(_ context.Context, taskIDs []string) ([]sqlitegen.ListTaskAssignedLabelIDsByTasksRow, error) {
	r.calls++
	r.taskIDs = append([]string(nil), taskIDs...)
	return append([]sqlitegen.ListTaskAssignedLabelIDsByTasksRow(nil), r.rows...), nil
}

func TestLoadTaskLabelIDsByTaskReadsAssignmentsOnceForBoundedSelection(t *testing.T) {
	reader := &countingTaskLabelAssignmentReader{
		rows: []sqlitegen.ListTaskAssignedLabelIDsByTasksRow{
			{TaskID: "task-a", LabelID: "label-alpha"},
			{TaskID: "task-a", LabelID: "label-zulu"},
			{TaskID: "task-c", LabelID: "label-beta"},
		},
	}
	taskIDs := []string{"task-a", "task-b", "task-c"}

	got, err := loadTaskLabelIDsByTask(t.Context(), reader, taskIDs)
	if err != nil {
		t.Fatalf("loadTaskLabelIDsByTask: %v", err)
	}
	if reader.calls != 1 {
		t.Fatalf("assignment reads = %d, want one batch read", reader.calls)
	}
	if !reflect.DeepEqual(reader.taskIDs, taskIDs) {
		t.Fatalf("selected task ids = %v, want %v", reader.taskIDs, taskIDs)
	}
	want := map[string][]string{
		"task-a": {"label-alpha", "label-zulu"},
		"task-b": {},
		"task-c": {"label-beta"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("labels by task = %#v, want %#v", got, want)
	}
}

func TestLoadTaskLabelIDsByTaskRejectsUnboundedSelectionBeforeReading(t *testing.T) {
	reader := &countingTaskLabelAssignmentReader{}
	taskIDs := make([]string, serverapi.WorkflowTaskListMaxPageSize+1)

	if _, err := loadTaskLabelIDsByTask(t.Context(), reader, taskIDs); err == nil {
		t.Fatal("loadTaskLabelIDsByTask accepted an unbounded selection")
	}
	if reader.calls != 0 {
		t.Fatalf("assignment reads = %d, want none after bound rejection", reader.calls)
	}
}

func TestTaskLabelProjectionMatchesAcrossDetailListAndBoardCards(t *testing.T) {
	ctx, _, workflowStore, binding, view := newWorkflowViewTestContextFixture(t)
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	zulu, err := workflowStore.CreateProjectLabel(ctx, binding.ProjectID, "Zulu")
	if err != nil {
		t.Fatalf("CreateProjectLabel Zulu: %v", err)
	}
	alpha, err := workflowStore.CreateProjectLabel(ctx, binding.ProjectID, "alpha")
	if err != nil {
		t.Fatalf("CreateProjectLabel alpha: %v", err)
	}
	labeled, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Alpha labeled",
		LabelIDs:  []string{zulu.ID.String(), alpha.ID.String()},
	})
	if err != nil {
		t.Fatalf("CreateTask labeled: %v", err)
	}
	unlabeled, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID: binding.ProjectID,
		Title:     "Zulu unlabeled",
	})
	if err != nil {
		t.Fatalf("CreateTask unlabeled: %v", err)
	}
	wantByTaskID := map[string][]string{
		string(labeled.ID):   {alpha.ID.String(), zulu.ID.String()},
		string(unlabeled.ID): {},
	}

	detailLabelIDsByTask := make(map[string][]string, len(wantByTaskID))
	for taskID := range wantByTaskID {
		detail, err := view.detail(t).GetTask(ctx, taskID)
		if err != nil {
			t.Fatalf("GetTask %s: %v", taskID, err)
		}
		detailLabelIDsByTask[taskID] = detail.LabelIDs
	}

	projectID := binding.ProjectID
	listResponse, err := view.tasks(t).List(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		ProjectID:   &projectID,
	})
	if err != nil {
		t.Fatalf("List tasks: %v", err)
	}
	listLabelIDsByTask := make(map[string][]string, len(listResponse.Tasks))
	for _, task := range listResponse.Tasks {
		listLabelIDsByTask[task.TaskID] = task.LabelIDs
	}

	board, err := view.board(t).Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		ProjectID:   binding.ProjectID,
	})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	cardResponse, err := view.board(t).ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilterNone(),
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      backlog.Node.NodeID,
	})
	if err != nil {
		t.Fatalf("ListNodeCards: %v", err)
	}
	cardLabelIDsByTask := make(map[string][]string, len(cardResponse.Cards))
	for _, card := range cardResponse.Cards {
		cardLabelIDsByTask[card.TaskID] = card.LabelIDs
	}

	for taskID, want := range wantByTaskID {
		detailLabelIDs := detailLabelIDsByTask[taskID]
		listLabelIDs := listLabelIDsByTask[taskID]
		cardLabelIDs := cardLabelIDsByTask[taskID]
		if !slices.Equal(detailLabelIDs, want) ||
			!slices.Equal(listLabelIDs, want) ||
			!slices.Equal(cardLabelIDs, want) {
			t.Fatalf(
				"task %s label ids diverged: detail=%v list=%v card=%v want=%v",
				taskID,
				detailLabelIDs,
				listLabelIDs,
				cardLabelIDs,
				want,
			)
		}
		if want != nil &&
			(detailLabelIDs == nil || listLabelIDs == nil || cardLabelIDs == nil) {
			t.Fatalf("task %s projected a nil label set", taskID)
		}
	}
}
