package workflowview

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestNewTaskDependenciesRequiresTaskStatusProjection(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	if _, err := NewTaskDependencies(fixture.metadata, nil); err == nil {
		t.Fatal("NewTaskDependencies accepted a nil TaskStatusProjection")
	}
}

func TestTaskDependenciesProjectsCompleteDirectionsOrderingAndAvailability(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	dependencies, err := NewTaskDependencies(fixture.metadata, fixture.projection)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	blocked := createViewTask(t, fixture, "Blocked")
	doneBlocker := createViewTask(t, fixture, "Done blocker")
	openBlocker := createViewTask(t, fixture, "Open blocker")
	directlyBlocked := createViewTask(t, fixture, "Directly blocked")
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{BlockerTaskID: doneBlocker.ID, BlockedTaskID: blocked.ID}); err != nil {
		t.Fatalf("add done blocker dependency: %v", err)
	}
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{BlockerTaskID: openBlocker.ID, BlockedTaskID: blocked.ID}); err != nil {
		t.Fatalf("add open blocker dependency: %v", err)
	}
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{BlockerTaskID: blocked.ID, BlockedTaskID: directlyBlocked.ID}); err != nil {
		t.Fatalf("add blocks dependency: %v", err)
	}
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := fixture.store.ManualMoveTask(fixture.ctx, workflowstore.ManualMoveRequest{
		TaskID:       doneBlocker.ID,
		TargetNodeID: terminalNodeID(t, definition),
	}); err != nil {
		t.Fatalf("move blocker to terminal: %v", err)
	}

	projected, err := dependencies.GetTaskDependencies(fixture.ctx, string(blocked.ID))
	if err != nil {
		t.Fatalf("GetTaskDependencies: %v", err)
	}
	if projected.BlockerCount != 2 || projected.UnsatisfiedBlockerCount != 1 || projected.DirectlyBlockedTaskCount != 1 {
		t.Fatalf("summary = %+v, want 2 blockers, 1 unsatisfied, 1 directly blocked", projected)
	}
	blockedBy := dependencyDirection(t, projected, serverapi.WorkflowTaskDependencyDirectionBlockedBy)
	if len(blockedBy.Items) != 2 || blockedBy.Items[0].TaskID != string(openBlocker.ID) || blockedBy.Items[1].TaskID != string(doneBlocker.ID) {
		t.Fatalf("blocked-by items = %+v, want unfinished first then short-id order", blockedBy.Items)
	}
	if blockedBy.Items[0].Satisfaction == nil || *blockedBy.Items[0].Satisfaction != serverapi.WorkflowTaskDependencyUnsatisfied {
		t.Fatalf("open blocker satisfaction = %+v, want unsatisfied", blockedBy.Items[0].Satisfaction)
	}
	if blockedBy.Items[1].Satisfaction == nil || *blockedBy.Items[1].Satisfaction != serverapi.WorkflowTaskDependencySatisfied {
		t.Fatalf("done blocker satisfaction = %+v, want satisfied", blockedBy.Items[1].Satisfaction)
	}
	blocks := dependencyDirection(t, projected, serverapi.WorkflowTaskDependencyDirectionBlocks)
	if len(blocks.Items) != 1 || blocks.Items[0].TaskID != string(directlyBlocked.ID) {
		t.Fatalf("blocks items = %+v, want directly blocked task", blocks.Items)
	}
	if blocks.Items[0].Satisfaction != nil {
		t.Fatalf("blocks item satisfaction = %+v, want absent", blocks.Items[0].Satisfaction)
	}
	if blockedBy.AddAvailability == nil || blockedBy.AddAvailability.Available == nil || blockedBy.AddAvailability.Available.RemainingCapacity != workflow.MaxTaskDependencies-2 {
		t.Fatalf("blocked-by availability = %+v, want remaining capacity %d", blockedBy.AddAvailability, workflow.MaxTaskDependencies-2)
	}
	if blocks.AddAvailability == nil || blocks.AddAvailability.Available == nil || blocks.AddAvailability.Available.RemainingCapacity != workflow.MaxTaskDependencies-1 {
		t.Fatalf("blocks availability = %+v, want remaining capacity %d", blocks.AddAvailability, workflow.MaxTaskDependencies-1)
	}
}

func TestTaskDependenciesEmptyProjectionAndFocusedCountFollowSatisfactionWithoutTouchingBlockedTask(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	dependencies, err := NewTaskDependencies(fixture.metadata, fixture.projection)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	emptyTask := createViewTask(t, fixture, "Empty")
	empty, err := dependencies.GetTaskDependencies(fixture.ctx, string(emptyTask.ID))
	if err != nil {
		t.Fatalf("GetTaskDependencies empty: %v", err)
	}
	if empty.Directions == nil || len(empty.Directions) != 2 {
		t.Fatalf("empty directions = %+v, want both directions", empty.Directions)
	}
	for _, direction := range empty.Directions {
		if direction.Items == nil || len(direction.Items) != 0 {
			t.Fatalf("empty direction items = %+v, want non-nil empty array", direction.Items)
		}
		if direction.AddAvailability == nil || direction.AddAvailability.Available == nil {
			t.Fatalf("empty direction availability = %+v, want available", direction.AddAvailability)
		}
	}
	blocker := createViewTask(t, fixture, "Blocker")
	if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: emptyTask.ID}); err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	beforeUpdatedAt := viewTaskUpdatedAt(t, fixture, emptyTask.ID)
	focused, err := dependencies.CountUnsatisfiedBlockers(fixture.ctx, string(emptyTask.ID))
	if err != nil {
		t.Fatalf("CountUnsatisfiedBlockers: %v", err)
	}
	if focused != 1 {
		t.Fatalf("focused unsatisfied count = %d, want 1", focused)
	}
	projected, err := dependencies.GetTaskDependencies(fixture.ctx, string(emptyTask.ID))
	if err != nil {
		t.Fatalf("GetTaskDependencies after add: %v", err)
	}
	if projected.UnsatisfiedBlockerCount != focused {
		t.Fatalf("summary unsatisfied count = %d, focused = %d", projected.UnsatisfiedBlockerCount, focused)
	}
	doneDefinition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if _, err := fixture.store.ManualMoveTask(fixture.ctx, workflowstore.ManualMoveRequest{
		TaskID:       blocker.ID,
		TargetNodeID: terminalNodeID(t, doneDefinition),
	}); err != nil {
		t.Fatalf("complete blocker: %v", err)
	}
	focused, err = dependencies.CountUnsatisfiedBlockers(fixture.ctx, string(emptyTask.ID))
	if err != nil {
		t.Fatalf("CountUnsatisfiedBlockers after done: %v", err)
	}
	if focused != 0 {
		t.Fatalf("focused count after done = %d, want 0", focused)
	}
	projected, err = dependencies.GetTaskDependencies(fixture.ctx, string(emptyTask.ID))
	if err != nil {
		t.Fatalf("GetTaskDependencies after done: %v", err)
	}
	if projected.UnsatisfiedBlockerCount != 0 {
		t.Fatalf("summary after done = %d, want 0", projected.UnsatisfiedBlockerCount)
	}
	if got := viewTaskUpdatedAt(t, fixture, emptyTask.ID); got != beforeUpdatedAt {
		t.Fatalf("blocked task timestamp = %d, want unchanged %d", got, beforeUpdatedAt)
	}
	if _, err := fixture.store.ManualMoveTask(fixture.ctx, workflowstore.ManualMoveRequest{
		TaskID:       blocker.ID,
		TargetNodeID: backlogNodeID(t, doneDefinition),
	}); err != nil {
		t.Fatalf("reopen blocker: %v", err)
	}
	focused, err = dependencies.CountUnsatisfiedBlockers(fixture.ctx, string(emptyTask.ID))
	if err != nil {
		t.Fatalf("CountUnsatisfiedBlockers after reopen: %v", err)
	}
	if focused != 1 {
		t.Fatalf("focused count after reopen = %d, want 1", focused)
	}
}

func TestListTaskDependenciesOmitsEmptyDirections(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	dependencies, err := NewTaskDependencies(fixture.metadata, fixture.projection)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	task := createViewTask(t, fixture, "Empty")

	listed, err := dependencies.ListTaskDependencies(fixture.ctx, string(task.ID), nil)
	if err != nil {
		t.Fatalf("ListTaskDependencies: %v", err)
	}
	if listed.Directions == nil || len(listed.Directions) != 0 {
		t.Fatalf("directions = %+v, want empty list", listed.Directions)
	}
	direction := serverapi.WorkflowTaskDependencyDirectionBlocks
	filtered, err := dependencies.ListTaskDependencies(
		fixture.ctx,
		string(task.ID),
		&direction,
	)
	if err != nil {
		t.Fatalf("ListTaskDependencies filtered: %v", err)
	}
	if filtered.Directions == nil || len(filtered.Directions) != 0 {
		t.Fatalf("filtered directions = %+v, want empty list", filtered.Directions)
	}
}

func TestListTaskDependenciesSortsBothDirectionsUnfinishedFirstThenShortID(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	dependencies, err := NewTaskDependencies(fixture.metadata, fixture.projection)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	subject := createViewTask(t, fixture, "Subject")
	blockerDone := createViewTask(t, fixture, "Blocker done")
	blockerOpenFirst := createViewTask(t, fixture, "Blocker open first")
	blockerOpenSecond := createViewTask(t, fixture, "Blocker open second")
	blockedDone := createViewTask(t, fixture, "Blocked done")
	blockedOpenFirst := createViewTask(t, fixture, "Blocked open first")
	blockedOpenSecond := createViewTask(t, fixture, "Blocked open second")
	for _, blocker := range []workflowstore.TaskRecord{blockerDone, blockerOpenFirst, blockerOpenSecond} {
		if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: subject.ID}); err != nil {
			t.Fatalf("add blocked-by dependency: %v", err)
		}
	}
	for _, blocked := range []workflowstore.TaskRecord{blockedDone, blockedOpenFirst, blockedOpenSecond} {
		if _, err := fixture.store.AddTaskDependency(fixture.ctx, workflowstore.TaskDependencyAddRequest{BlockerTaskID: subject.ID, BlockedTaskID: blocked.ID}); err != nil {
			t.Fatalf("add blocks dependency: %v", err)
		}
	}
	definition, _, err := fixture.store.GetDefinition(fixture.ctx, fixture.workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	for _, task := range []workflowstore.TaskRecord{blockerDone, blockedDone} {
		if _, err := fixture.store.ManualMoveTask(fixture.ctx, workflowstore.ManualMoveRequest{
			TaskID:       task.ID,
			TargetNodeID: terminalNodeID(t, definition),
		}); err != nil {
			t.Fatalf("move %q to terminal: %v", task.ShortID, err)
		}
	}

	listed, err := dependencies.ListTaskDependencies(fixture.ctx, string(subject.ID), nil)
	if err != nil {
		t.Fatalf("ListTaskDependencies: %v", err)
	}
	assertDependencyItemOrder(t,
		listedDependencyDirection(t, listed, serverapi.WorkflowTaskDependencyDirectionBlockedBy).Items,
		blockerOpenFirst, blockerOpenSecond, blockerDone,
	)
	assertDependencyItemOrder(t,
		listedDependencyDirection(t, listed, serverapi.WorkflowTaskDependencyDirectionBlocks).Items,
		blockedOpenFirst, blockedOpenSecond, blockedDone,
	)
}

func createViewTask(t *testing.T, fixture currentNodeViewFixture, title string) workflowstore.TaskRecord {
	t.Helper()
	task, err := fixture.store.CreateTask(fixture.ctx, workflowstore.CreateTaskRequest{
		ProjectID:  fixture.binding.ProjectID,
		WorkflowID: &fixture.workflowID,
		Title:      title,
		Body:       "Body",
	})
	if err != nil {
		t.Fatalf("CreateTask %q: %v", title, err)
	}
	return task
}

func terminalNodeID(t *testing.T, definition workflow.Definition) workflow.NodeID {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindTerminal {
			return workflow.NodeIDOf(node)
		}
	}
	t.Fatal("terminal node missing")
	return workflow.NodeID("unreachable")
}

func backlogNodeID(t *testing.T, definition workflow.Definition) workflow.NodeID {
	t.Helper()
	for _, node := range definition.Nodes {
		if node.Kind() == workflow.NodeKindStart {
			return workflow.NodeIDOf(node)
		}
	}
	t.Fatal("start node missing")
	return workflow.NodeID("unreachable")
}

func dependencyDirection(t *testing.T, dependencies serverapi.WorkflowTaskDependencies, direction serverapi.WorkflowTaskDependencyDirection) serverapi.WorkflowTaskDependencyDirectionProjection {
	t.Helper()
	for _, candidate := range dependencies.Directions {
		if candidate.Direction == direction {
			return candidate
		}
	}
	t.Fatalf("dependency direction %q missing from %+v", direction, dependencies.Directions)
	return serverapi.WorkflowTaskDependencyDirectionProjection{}
}

func listedDependencyDirection(t *testing.T, dependencies serverapi.WorkflowTaskDependencyListResponse, direction serverapi.WorkflowTaskDependencyDirection) serverapi.WorkflowTaskDependencyListDirectionProjection {
	t.Helper()
	for _, candidate := range dependencies.Directions {
		if candidate.Direction == direction {
			return candidate
		}
	}
	t.Fatalf("dependency direction %q missing from %+v", direction, dependencies.Directions)
	return serverapi.WorkflowTaskDependencyListDirectionProjection{}
}

func assertDependencyItemOrder(t *testing.T, items []serverapi.WorkflowTaskDependencyItem, expected ...workflowstore.TaskRecord) {
	t.Helper()
	if len(items) != len(expected) {
		t.Fatalf("items = %+v, want %d items", items, len(expected))
	}
	for index, task := range expected {
		if items[index].TaskID != string(task.ID) {
			t.Fatalf("items = %+v, want task %q at index %d", items, task.ShortID, index)
		}
	}
}

func viewTaskUpdatedAt(t *testing.T, fixture currentNodeViewFixture, taskID workflow.TaskID) int64 {
	t.Helper()
	var updatedAt int64
	if err := fixture.metadata.DB().QueryRow(`SELECT updated_at_unix_ms FROM tasks WHERE id = ?`, taskID).Scan(&updatedAt); err != nil {
		t.Fatalf("read task timestamp: %v", err)
	}
	return updatedAt
}
