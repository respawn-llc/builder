package workflowview

import (
	"slices"
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestBoardProjectsSelectionPickerValidationColumnsGroupsAndCountsThroughFocusedInterface(t *testing.T) {
	ctx, metadataStore, workflowStore, binding := newWorkflowViewTestContextStore(t)
	definitions, err := NewDefinitionProjection(workflowStore)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	boardView, err := NewBoard(metadataStore, definitions, testsetup.QuestionsEnabled("coder"))
	if err != nil {
		t.Fatalf("NewBoard: %v", err)
	}

	empty, err := boardView.Get(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID})
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

	defaultBoard, err := boardView.Get(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("Get default board: %v", err)
	}
	if defaultBoard.SelectedWorkflow == nil || defaultBoard.SelectedWorkflow.WorkflowID != string(defaultWorkflowID) {
		t.Fatalf("default selection = %+v, want %s", defaultBoard.SelectedWorkflow, defaultWorkflowID)
	}

	selectedID := string(selectedWorkflowID)
	selectedBoard, err := boardView.Get(ctx, serverapi.WorkflowBoardRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &selectedID,
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
