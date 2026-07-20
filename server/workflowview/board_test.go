package workflowview

import (
	"reflect"
	"slices"
	"sort"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/server/worktree"
	"core/shared/serverapi"
)

func TestBoardProjectsSelectionPickerValidationColumnsGroupsAndCountsThroughFocusedInterface(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	projector := NewTaskProjector()
	boardView, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"), projector)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}

	empty, err := boardView.Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("Get empty board: %v", err)
	}
	if empty.ProjectID != binding.ProjectID ||
		empty.Project.ProjectKey != "WOR" ||
		empty.SelectedWorkflow != nil ||
		len(empty.WorkflowPicker) != 0 ||
		len(empty.Groups) != 0 ||
		len(empty.Columns) != 0 {
		t.Fatalf("empty board = %+v", empty)
	}

	defaultWorkflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, defaultWorkflowID, true); err != nil {
		t.Fatalf("LinkWorkflow default: %v", err)
	}
	selectedWorkflowID := createWorkflowViewFanoutWorkflow(t, ctx, workflowStore)
	group, _, err := workflowStore.AddNodeGroup(ctx, workflowstore.NodeGroupRecord{
		WorkflowID:  selectedWorkflowID,
		Key:         "implementation",
		DisplayName: "Implementation",
		SortOrder:   10,
	})
	if err != nil {
		t.Fatalf("AddNodeGroup selected: %v", err)
	}
	selectedDefinition, _, err := workflowStore.GetDefinition(ctx, selectedWorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition selected: %v", err)
	}
	groupedNodeIDs := []any{group.ID}
	for _, key := range []string{"impl_a", "impl_b", "impl_c", "join"} {
		groupedNodeIDs = append(groupedNodeIDs, string(workflow.NodeIDOf(workflowViewNodeByKey(t, selectedDefinition, key))))
	}
	if _, err := metadataStore.DB().ExecContext(ctx, `UPDATE workflow_nodes SET group_id = ? WHERE id IN (?, ?, ?, ?)`, groupedNodeIDs...); err != nil {
		t.Fatalf("assign selected node group: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, selectedWorkflowID, false); err != nil {
		t.Fatalf("LinkWorkflow selected: %v", err)
	}
	invalidWorkflow, err := workflowStore.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Invalid picker workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow invalid: %v", err)
	}
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, invalidWorkflow.ID, false); err != nil {
		t.Fatalf("LinkWorkflow invalid: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowIDPointerForTest(selectedWorkflowID),
		Title:      "Selected backlog task",
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask selected: %v", err)
	}

	defaultBoard, err := boardView.Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("Get default board: %v", err)
	}
	if defaultBoard.SelectedWorkflow == nil || defaultBoard.SelectedWorkflow.WorkflowID != string(defaultWorkflowID) {
		t.Fatalf("default selection = %+v, want %s", defaultBoard.SelectedWorkflow, defaultWorkflowID)
	}

	selectedID := string(selectedWorkflowID)
	selectedBoard, err := boardView.Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   binding.ProjectID,
		WorkflowID:  &selectedID,
	})
	if err != nil {
		t.Fatalf("Get explicitly selected board: %v", err)
	}
	if selectedBoard.SelectedWorkflow == nil || selectedBoard.SelectedWorkflow.WorkflowID != selectedID {
		t.Fatalf("explicit selection = %+v, want %s", selectedBoard.SelectedWorkflow, selectedID)
	}
	wantPickerOrder := []string{string(defaultWorkflowID), selectedID, string(invalidWorkflow.ID)}
	pickerOrder := make([]string, 0, len(selectedBoard.WorkflowPicker))
	for _, item := range selectedBoard.WorkflowPicker {
		pickerOrder = append(pickerOrder, item.WorkflowID)
	}
	if !slices.Equal(pickerOrder, wantPickerOrder) {
		t.Fatalf("picker order = %v, want %v", pickerOrder, wantPickerOrder)
	}
	if !selectedBoard.WorkflowPicker[0].IsProjectDefault {
		t.Fatalf("default picker item = %+v", selectedBoard.WorkflowPicker[0])
	}
	selectedPicker := requireWorkflowPickerItem(t, selectedBoard.WorkflowPicker, selectedID)
	if !selectedPicker.ValidForTaskCreation || len(selectedPicker.ValidationErrors) != 0 {
		t.Fatalf("selected picker validation = %+v", selectedPicker)
	}
	invalidPicker := requireWorkflowPickerItem(t, selectedBoard.WorkflowPicker, string(invalidWorkflow.ID))
	if invalidPicker.ValidForTaskCreation || len(invalidPicker.ValidationErrors) == 0 {
		t.Fatalf("invalid picker validation = %+v", invalidPicker)
	}
	if len(selectedBoard.Columns) != 7 {
		t.Fatalf("visible board columns = %+v, want start, plan, three branches, synth, and terminal", selectedBoard.Columns)
	}
	var backlogCount int
	for _, column := range selectedBoard.Columns {
		if column.Node.Kind == string(workflow.NodeKindJoin) {
			t.Fatalf("join node leaked into visible columns: %+v", selectedBoard.Columns)
		}
		if column.IsBacklog {
			backlogCount = column.TaskCount
		}
	}
	if backlogCount != 1 {
		t.Fatalf("backlog task count = %d, want task %s", backlogCount, task.ID)
	}
	if len(selectedBoard.Groups) != 1 ||
		selectedBoard.Groups[0].Key != "implementation" ||
		len(selectedBoard.Groups[0].NodeIDs) != 3 {
		t.Fatalf("board groups = %+v", selectedBoard.Groups)
	}
}

func TestBoardCardsPageBidirectionallyAndMatchTaskFactsThroughFocusedInterface(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	sourceWorkspace, err := metadataStore.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	workflowID := createWorkflowViewValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	projector := NewTaskProjector()
	boardView, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"), projector)
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}
	detailView, err := NewTaskDetail(metadataStore, definitions, projector, worktree.NewGitInspector(nil))
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}

	type orderedTask struct {
		id              string
		updatedAtUnixMs int64
	}
	expectedBacklog := make([]orderedTask, 0, 4)
	for index, updatedAtUnixMs := range []int64{100, 200, 300, 400} {
		task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowIDPointerForTest(workflowID),
			Title:      "Paged backlog",
			Body:       "Body",
		})
		if err != nil {
			t.Fatalf("CreateTask backlog %d: %v", index, err)
		}
		if _, err := metadataStore.DB().ExecContext(ctx, `UPDATE tasks SET updated_at_unix_ms = ? WHERE id = ?`, updatedAtUnixMs, string(task.ID)); err != nil {
			t.Fatalf("set backlog timestamp %d: %v", index, err)
		}
		expectedBacklog = append(expectedBacklog, orderedTask{id: string(task.ID), updatedAtUnixMs: updatedAtUnixMs})
	}
	sort.Slice(expectedBacklog, func(i, j int) bool {
		if expectedBacklog[i].updatedAtUnixMs != expectedBacklog[j].updatedAtUnixMs {
			return expectedBacklog[i].updatedAtUnixMs > expectedBacklog[j].updatedAtUnixMs
		}
		return expectedBacklog[i].id > expectedBacklog[j].id
	})

	board, err := boardView.Get(ctx, serverapi.WorkflowBoardRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("Get board: %v", err)
	}
	backlog := workflowViewColumnByKind(t, board, workflow.NodeKindStart)
	firstPage, err := boardView.ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      backlog.Node.NodeID,
		PageSize:    2,
	})
	if err != nil {
		t.Fatalf("ListNodeCards first: %v", err)
	}
	if firstPage.PreviousPageToken != nil || firstPage.NextPageToken == nil {
		t.Fatalf("first page cursors = previous:%v next:%v", firstPage.PreviousPageToken, firstPage.NextPageToken)
	}
	secondPage, err := boardView.ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      backlog.Node.NodeID,
		PageSize:    2,
		PageToken:   firstPage.NextPageToken,
	})
	if err != nil {
		t.Fatalf("ListNodeCards second: %v", err)
	}
	if secondPage.PreviousPageToken == nil || secondPage.NextPageToken != nil {
		t.Fatalf("second page cursors = previous:%v next:%v", secondPage.PreviousPageToken, secondPage.NextPageToken)
	}
	wantFirstIDs := []string{expectedBacklog[0].id, expectedBacklog[1].id}
	wantSecondIDs := []string{expectedBacklog[2].id, expectedBacklog[3].id}
	if got := workflowViewBoardCardIDs(firstPage.Cards); !slices.Equal(got, wantFirstIDs) {
		t.Fatalf("first page ids = %v, want %v", got, wantFirstIDs)
	}
	if got := workflowViewBoardCardIDs(secondPage.Cards); !slices.Equal(got, wantSecondIDs) {
		t.Fatalf("second page ids = %v, want %v", got, wantSecondIDs)
	}
	newerPage, err := boardView.ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      backlog.Node.NodeID,
		PageSize:    2,
		PageToken:   secondPage.PreviousPageToken,
	})
	if err != nil {
		t.Fatalf("ListNodeCards newer: %v", err)
	}
	if got := workflowViewBoardCardIDs(newerPage.Cards); !slices.Equal(got, wantFirstIDs) {
		t.Fatalf("newer page ids = %v, want %v", got, wantFirstIDs)
	}

	queuedTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		WorkflowID:        workflowIDPointerForTest(workflowID),
		Title:             "Queued",
		Body:              "Body",
		SourceWorkspaceID: sourceWorkspace.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask queued: %v", err)
	}
	if _, err := workflowStore.StartTask(ctx, queuedTask.ID); err != nil {
		t.Fatalf("StartTask queued: %v", err)
	}
	runningTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:         binding.ProjectID,
		WorkflowID:        workflowIDPointerForTest(workflowID),
		Title:             "Running",
		Body:              "Body",
		SourceWorkspaceID: sourceWorkspace.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateTask running: %v", err)
	}
	runningStarted, err := workflowStore.StartTask(ctx, runningTask.ID)
	if err != nil {
		t.Fatalf("StartTask running: %v", err)
	}
	if _, err := workflowStore.ClaimRun(ctx, runningStarted.RunID, 0); err != nil {
		t.Fatalf("ClaimRun running: %v", err)
	}
	agentColumn := workflowViewColumnByKey(t, board, "agent")
	activePage, err := boardView.ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      agentColumn.Node.NodeID,
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("ListNodeCards active: %v", err)
	}
	for _, taskID := range []string{string(queuedTask.ID), string(runningTask.ID)} {
		card := requireBoardCard(t, activePage.Cards, taskID)
		detail, err := detailView.GetTask(ctx, taskID)
		if err != nil {
			t.Fatalf("GetTask %s: %v", taskID, err)
		}
		if !reflect.DeepEqual(card.Status, detail.Status) || !reflect.DeepEqual(card.Actions, detail.Actions) {
			t.Fatalf("card facts for %s = %+v/%+v, want detail %+v/%+v", taskID, card.Status, card.Actions, detail.Status, detail.Actions)
		}
		if card.SourceWorkspace.WorkspaceID != sourceWorkspace.WorkspaceID || !reflect.DeepEqual(card.SourceWorkspace, detail.SourceWorkspace) {
			t.Fatalf("card source workspace for %s = %+v, want detail %+v", taskID, card.SourceWorkspace, detail.SourceWorkspace)
		}
	}

	canceledTask, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: workflowIDPointerForTest(workflowID),
		Title:      "Canceled",
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask canceled: %v", err)
	}
	if _, err := workflowStore.CancelTask(ctx, canceledTask.ID, "stop"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	forceCanceledBacklogPlacementWithoutTerminal(t, ctx, metadataStore, canceledTask.ID, workflowID)
	doneColumn := workflowViewColumnByKind(t, board, workflow.NodeKindTerminal)
	donePage, err := boardView.ListNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   binding.ProjectID,
		WorkflowID:  string(workflowID),
		NodeID:      doneColumn.Node.NodeID,
		PageSize:    10,
	})
	if err != nil {
		t.Fatalf("ListNodeCards done: %v", err)
	}
	canceledCard := requireBoardCard(t, donePage.Cards, string(canceledTask.ID))
	canceledDetail, err := detailView.GetTask(ctx, string(canceledTask.ID))
	if err != nil {
		t.Fatalf("GetTask canceled: %v", err)
	}
	if !reflect.DeepEqual(canceledCard.Status, canceledDetail.Status) ||
		!reflect.DeepEqual(canceledCard.Actions, canceledDetail.Actions) ||
		!reflect.DeepEqual(canceledCard.ActiveNodeIDs, canceledDetail.Summary.ActiveNodeIDs) {
		t.Fatalf("canceled card facts = %+v, want detail %+v", canceledCard, canceledDetail)
	}
}

func requireBoardCard(t *testing.T, cards []serverapi.WorkflowBoardTaskCard, taskID string) serverapi.WorkflowBoardTaskCard {
	t.Helper()
	for _, card := range cards {
		if card.TaskID == taskID {
			return card
		}
	}
	t.Fatalf("board card %q not found in %+v", taskID, cards)
	return serverapi.WorkflowBoardTaskCard{}
}

func requireWorkflowPickerItem(t *testing.T, items []serverapi.WorkflowPickerItem, workflowID string) serverapi.WorkflowPickerItem {
	t.Helper()
	for _, item := range items {
		if item.WorkflowID == workflowID {
			return item
		}
	}
	t.Fatalf("workflow picker item %q not found in %+v", workflowID, items)
	return serverapi.WorkflowPickerItem{}
}
