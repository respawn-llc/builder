package workflowstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/workflow"
)

func TestAddTaskDependencySameProjectCrossWorkflowTouchesBothTasks(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	firstWorkflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, firstWorkflowID, true)
	secondWorkflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, secondWorkflowID, false)

	blocker := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &firstWorkflowID,
		Title:      "Blocker",
		Body:       "Blocker body",
	})
	blocked := createTask(t, ctx, store, CreateTaskRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &secondWorkflowID,
		Title:      "Blocked",
		Body:       "Blocked body",
	})
	beforeBlocker := taskUpdatedAt(t, store, blocker.ID)
	beforeBlocked := taskUpdatedAt(t, store, blocked.ID)
	updatedAt := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	store.now = func() time.Time { return updatedAt }

	result, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{
		BlockerTaskID: blocker.ID,
		BlockedTaskID: blocked.ID,
	})
	if err != nil {
		t.Fatalf("AddTaskDependency: %v", err)
	}
	if result.Outcome != TaskDependencyAdded {
		t.Fatalf("outcome = %q, want %q", result.Outcome, TaskDependencyAdded)
	}

	afterBlocker := taskUpdatedAt(t, store, blocker.ID)
	afterBlocked := taskUpdatedAt(t, store, blocked.ID)
	if afterBlocker != updatedAt.UnixMilli() || afterBlocked != updatedAt.UnixMilli() {
		t.Fatalf("updated timestamps = %d and %d, want %d", afterBlocker, afterBlocked, updatedAt.UnixMilli())
	}
	if afterBlocker == beforeBlocker || afterBlocked == beforeBlocked {
		t.Fatalf("dependency add did not touch both tasks: before=(%d,%d), after=(%d,%d)", beforeBlocker, beforeBlocked, afterBlocker, afterBlocked)
	}

	var dependencyCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM task_dependencies
		WHERE blocker_task_id = ? AND blocked_task_id = ?
	`, blocker.ID, blocked.ID).Scan(&dependencyCount); err != nil {
		t.Fatalf("count dependency: %v", err)
	}
	if dependencyCount != 1 {
		t.Fatalf("dependency count = %d, want 1", dependencyCount)
	}
}

func TestAddTaskDependencyIsIdempotentBeforeAndAtTheLimit(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	blocker := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocker", Body: "Body"})
	blockedTasks := make([]TaskRecord, 0, workflow.MaxTaskDependencies)
	for index := 0; index < workflow.MaxTaskDependencies; index++ {
		blockedTasks = append(blockedTasks, createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: &workflowID,
			Title:      "Blocked",
			Body:       "Body",
		}))
	}
	for _, blocked := range blockedTasks {
		if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: blocked.ID}); err != nil {
			t.Fatalf("add dependency to %q: %v", blocked.ID, err)
		}
	}
	beforeBlocker := taskUpdatedAt(t, store, blocker.ID)
	beforeBlocked := taskUpdatedAt(t, store, blockedTasks[0].ID)
	store.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	result, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: blockedTasks[0].ID})
	if err != nil {
		t.Fatalf("repeat full dependency add: %v", err)
	}
	if result.Outcome != TaskDependencyAlreadyPresent {
		t.Fatalf("repeat full dependency outcome = %q, want %q", result.Outcome, TaskDependencyAlreadyPresent)
	}
	if got := taskUpdatedAt(t, store, blocker.ID); got != beforeBlocker {
		t.Fatalf("blocker timestamp after idempotent add = %d, want %d", got, beforeBlocker)
	}
	if got := taskUpdatedAt(t, store, blockedTasks[0].ID); got != beforeBlocked {
		t.Fatalf("blocked timestamp after idempotent add = %d, want %d", got, beforeBlocked)
	}
}

func TestRemoveTaskDependencyIsIdempotentAndTouchesOnlyRealChanges(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	blocker := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocker", Body: "Body"})
	blocked := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocked", Body: "Body"})
	req := TaskDependencyRemoveRequest{BlockerTaskID: blocker.ID, BlockedTaskID: blocked.ID}

	beforeAbsentBlocker := taskUpdatedAt(t, store, blocker.ID)
	beforeAbsentBlocked := taskUpdatedAt(t, store, blocked.ID)
	absent, err := store.RemoveTaskDependency(ctx, req)
	if err != nil {
		t.Fatalf("remove absent dependency: %v", err)
	}
	if absent.Outcome != TaskDependencyAlreadyAbsent {
		t.Fatalf("absent outcome = %q, want %q", absent.Outcome, TaskDependencyAlreadyAbsent)
	}
	if got := taskUpdatedAt(t, store, blocker.ID); got != beforeAbsentBlocker {
		t.Fatalf("blocker timestamp after absent remove = %d, want %d", got, beforeAbsentBlocker)
	}
	if got := taskUpdatedAt(t, store, blocked.ID); got != beforeAbsentBlocked {
		t.Fatalf("blocked timestamp after absent remove = %d, want %d", got, beforeAbsentBlocked)
	}

	if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: blocked.ID}); err != nil {
		t.Fatalf("add dependency before remove: %v", err)
	}
	beforeRemoveBlocker := taskUpdatedAt(t, store, blocker.ID)
	beforeRemoveBlocked := taskUpdatedAt(t, store, blocked.ID)
	store.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	removed, err := store.RemoveTaskDependency(ctx, req)
	if err != nil {
		t.Fatalf("remove existing dependency: %v", err)
	}
	if removed.Outcome != TaskDependencyRemoved {
		t.Fatalf("removed outcome = %q, want %q", removed.Outcome, TaskDependencyRemoved)
	}
	assertTaskDependencyCount(t, store, blocker.ID, blocked.ID, 0)
	if got := taskUpdatedAt(t, store, blocker.ID); got == beforeRemoveBlocker {
		t.Fatal("blocker timestamp did not change after real remove")
	}
	if got := taskUpdatedAt(t, store, blocked.ID); got == beforeRemoveBlocked {
		t.Fatal("blocked timestamp did not change after real remove")
	}
}

func TestAddTaskDependencyEnforcesBothDirectionLimits(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	blocker := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocker", Body: "Body"})
	blocked := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocked", Body: "Body"})
	outgoingTargets := make([]TaskRecord, 0, workflow.MaxTaskDependencies)
	for index := 0; index < workflow.MaxTaskDependencies; index++ {
		target := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Target", Body: "Body"})
		outgoingTargets = append(outgoingTargets, target)
		if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: target.ID}); err != nil {
			t.Fatalf("add outgoing dependency %d: %v", index, err)
		}
	}
	outgoingExtra := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Extra", Body: "Body"})
	beforeBlocker := taskUpdatedAt(t, store, blocker.ID)
	beforeExtra := taskUpdatedAt(t, store, outgoingExtra.ID)
	_, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: outgoingExtra.ID})
	assertTaskDependencyLimitError(t, err, workflow.TaskDependencyBlockerLimit)
	assertTaskDependencyCount(t, store, blocker.ID, outgoingExtra.ID, 0)
	if got := taskUpdatedAt(t, store, blocker.ID); got != beforeBlocker {
		t.Fatalf("blocker timestamp after outgoing limit = %d, want %d", got, beforeBlocker)
	}
	if got := taskUpdatedAt(t, store, outgoingExtra.ID); got != beforeExtra {
		t.Fatalf("extra timestamp after outgoing limit = %d, want %d", got, beforeExtra)
	}
	if len(outgoingTargets) != workflow.MaxTaskDependencies {
		t.Fatalf("outgoing targets = %d, want %d", len(outgoingTargets), workflow.MaxTaskDependencies)
	}

	incomingBlockers := make([]TaskRecord, 0, workflow.MaxTaskDependencies)
	for index := 0; index < workflow.MaxTaskDependencies; index++ {
		source := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Source", Body: "Body"})
		incomingBlockers = append(incomingBlockers, source)
		if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: source.ID, BlockedTaskID: blocked.ID}); err != nil {
			t.Fatalf("add incoming dependency %d: %v", index, err)
		}
	}
	incomingExtra := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Incoming extra", Body: "Body"})
	beforeBlocked := taskUpdatedAt(t, store, blocked.ID)
	beforeIncomingExtra := taskUpdatedAt(t, store, incomingExtra.ID)
	_, err = store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: incomingExtra.ID, BlockedTaskID: blocked.ID})
	assertTaskDependencyLimitError(t, err, workflow.TaskDependencyBlockedLimit)
	assertTaskDependencyCount(t, store, incomingExtra.ID, blocked.ID, 0)
	if got := taskUpdatedAt(t, store, blocked.ID); got != beforeBlocked {
		t.Fatalf("blocked timestamp after incoming limit = %d, want %d", got, beforeBlocked)
	}
	if got := taskUpdatedAt(t, store, incomingExtra.ID); got != beforeIncomingExtra {
		t.Fatalf("incoming extra timestamp after limit = %d, want %d", got, beforeIncomingExtra)
	}
	if len(incomingBlockers) != workflow.MaxTaskDependencies {
		t.Fatalf("incoming blockers = %d, want %d", len(incomingBlockers), workflow.MaxTaskDependencies)
	}
}
func taskUpdatedAt(t *testing.T, store *Store, taskID workflow.TaskID) int64 {
	t.Helper()
	var updatedAt int64
	if err := store.db.QueryRow(`SELECT updated_at_unix_ms FROM tasks WHERE id = ?`, taskID).Scan(&updatedAt); err != nil {
		t.Fatalf("read task %q updated timestamp: %v", taskID, err)
	}
	return updatedAt
}

func TestConcurrentTaskDependencyAddsSerializeTheFinalOutgoingAndIncomingSlots(t *testing.T) {
	t.Run("outgoing", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createValidWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		blocker := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocker", Body: "Body"})
		for index := 0; index < workflow.MaxTaskDependencies-1; index++ {
			target := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Target", Body: "Body"})
			if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: target.ID}); err != nil {
				t.Fatalf("seed outgoing dependency %d: %v", index, err)
			}
		}
		first := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "First", Body: "Body"})
		second := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Second", Body: "Body"})
		beforeBlocker := taskUpdatedAt(t, store, blocker.ID)
		beforeFirst := taskUpdatedAt(t, store, first.ID)
		beforeSecond := taskUpdatedAt(t, store, second.ID)
		results := concurrentTaskDependencyAdds(t, store, [2]TaskDependencyAddRequest{
			{BlockerTaskID: blocker.ID, BlockedTaskID: first.ID},
			{BlockerTaskID: blocker.ID, BlockedTaskID: second.ID},
		})
		assertOneAddedAndOneLimitRejected(t, results, workflow.TaskDependencyBlockerLimit)
		assertTaskDependencyCount(t, store, blocker.ID, first.ID, dependencyPresence(results, first.ID))
		assertTaskDependencyCount(t, store, blocker.ID, second.ID, dependencyPresence(results, second.ID))
		if got := taskUpdatedAt(t, store, blocker.ID); got == beforeBlocker {
			t.Fatalf("blocker timestamp did not change after final-slot race")
		}
		assertTouchedByResult(t, store, first.ID, beforeFirst, results, first.ID)
		assertTouchedByResult(t, store, second.ID, beforeSecond, results, second.ID)
	})

	t.Run("incoming", func(t *testing.T) {
		ctx, store, binding := newTestStoreContext(t)
		workflowID := createValidWorkflow(t, ctx, store)
		linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
		blocked := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocked", Body: "Body"})
		for index := 0; index < workflow.MaxTaskDependencies-1; index++ {
			source := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Source", Body: "Body"})
			if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: source.ID, BlockedTaskID: blocked.ID}); err != nil {
				t.Fatalf("seed incoming dependency %d: %v", index, err)
			}
		}
		first := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "First", Body: "Body"})
		second := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Second", Body: "Body"})
		beforeBlocked := taskUpdatedAt(t, store, blocked.ID)
		beforeFirst := taskUpdatedAt(t, store, first.ID)
		beforeSecond := taskUpdatedAt(t, store, second.ID)
		results := concurrentTaskDependencyAdds(t, store, [2]TaskDependencyAddRequest{
			{BlockerTaskID: first.ID, BlockedTaskID: blocked.ID},
			{BlockerTaskID: second.ID, BlockedTaskID: blocked.ID},
		})
		assertOneAddedAndOneLimitRejected(t, results, workflow.TaskDependencyBlockedLimit)
		assertTaskDependencyCount(t, store, first.ID, blocked.ID, dependencyPresence(results, first.ID))
		assertTaskDependencyCount(t, store, second.ID, blocked.ID, dependencyPresence(results, second.ID))
		if got := taskUpdatedAt(t, store, blocked.ID); got == beforeBlocked {
			t.Fatalf("blocked timestamp did not change after final-slot race")
		}
		assertTouchedByResult(t, store, first.ID, beforeFirst, results, first.ID)
		assertTouchedByResult(t, store, second.ID, beforeSecond, results, second.ID)
	})
}

type concurrentTaskDependencyResult struct {
	request TaskDependencyAddRequest
	result  TaskDependencyAddResult
	err     error
}

func concurrentTaskDependencyAdds(t *testing.T, store *Store, requests [2]TaskDependencyAddRequest) [2]concurrentTaskDependencyResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	waitGroup.Add(len(requests))
	results := [2]concurrentTaskDependencyResult{}
	for index, request := range requests {
		go func() {
			defer waitGroup.Done()
			<-start
			result, err := store.AddTaskDependency(ctx, request)
			results[index] = concurrentTaskDependencyResult{request: request, result: result, err: err}
		}()
	}
	close(start)
	waitGroup.Wait()
	return results
}

func assertOneAddedAndOneLimitRejected(t *testing.T, results [2]concurrentTaskDependencyResult, reason workflow.TaskDependencyPolicyErrorReason) {
	t.Helper()
	added := 0
	rejected := 0
	for _, result := range results {
		if result.err == nil {
			if result.result.Outcome != TaskDependencyAdded {
				t.Fatalf("successful outcome = %q, want %q", result.result.Outcome, TaskDependencyAdded)
			}
			added++
			continue
		}
		assertTaskDependencyPolicyError(t, result.err, reason)
		rejected++
	}
	if added != 1 || rejected != 1 {
		t.Fatalf("concurrent results = %+v, want one add and one rejection", results)
	}
}

func dependencyPresence(results [2]concurrentTaskDependencyResult, taskID workflow.TaskID) int {
	for _, result := range results {
		if result.request.BlockedTaskID == taskID || result.request.BlockerTaskID == taskID {
			if result.err == nil {
				return 1
			}
			return 0
		}
	}
	return 0
}

func assertTouchedByResult(t *testing.T, store *Store, taskID workflow.TaskID, before int64, results [2]concurrentTaskDependencyResult, relatedID workflow.TaskID) {
	t.Helper()
	for _, result := range results {
		if result.request.BlockedTaskID != relatedID && result.request.BlockerTaskID != relatedID {
			continue
		}
		after := taskUpdatedAt(t, store, taskID)
		if result.err == nil && after == before {
			t.Fatalf("task %q timestamp did not change for successful add", taskID)
		}
		if result.err != nil && after != before {
			t.Fatalf("task %q timestamp changed for rejected add: before=%d after=%d", taskID, before, after)
		}
		return
	}
	t.Fatalf("no concurrent result for task %q", taskID)
}

func TestAddTaskDependencyRejectsInvalidPairsWithoutMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	blocker := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocker", Body: "Body"})
	blocked := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocked", Body: "Body"})

	beforeBlocker := taskUpdatedAt(t, store, blocker.ID)
	beforeBlocked := taskUpdatedAt(t, store, blocked.ID)
	testCases := []struct {
		name   string
		req    TaskDependencyAddRequest
		reason workflow.TaskDependencyPolicyErrorReason
	}{
		{
			name:   "missing blocker",
			req:    TaskDependencyAddRequest{BlockerTaskID: "task-missing", BlockedTaskID: blocked.ID},
			reason: workflow.TaskDependencyMissingTask,
		},
		{
			name:   "missing blocked",
			req:    TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: "task-missing"},
			reason: workflow.TaskDependencyMissingTask,
		},
		{
			name:   "self dependency",
			req:    TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: blocker.ID},
			reason: workflow.TaskDependencySelf,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := store.AddTaskDependency(ctx, testCase.req)
			assertTaskDependencyPolicyError(t, err, testCase.reason)
			assertTaskDependencyCount(t, store, testCase.req.BlockerTaskID, testCase.req.BlockedTaskID, 0)
			if got := taskUpdatedAt(t, store, blocker.ID); got != beforeBlocker {
				t.Fatalf("blocker updated timestamp = %d, want unchanged %d", got, beforeBlocker)
			}
			if got := taskUpdatedAt(t, store, blocked.ID); got != beforeBlocked {
				t.Fatalf("blocked updated timestamp = %d, want unchanged %d", got, beforeBlocked)
			}
		})
	}
}

func TestAddTaskDependencyRejectsCrossProjectAndReciprocalPairs(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	blocker := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocker", Body: "Body"})
	blocked := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocked", Body: "Body"})

	otherBinding, err := store.metadata.RegisterWorkspaceBinding(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding for second project: %v", err)
	}
	otherWorkflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, otherBinding.ProjectID, otherWorkflowID, true)
	otherTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: otherBinding.ProjectID, WorkflowID: &otherWorkflowID, Title: "Other", Body: "Body"})

	_, err = store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: otherTask.ID})
	assertTaskDependencyPolicyError(t, err, workflow.TaskDependencyProjectMismatch)

	if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: blocked.ID}); err != nil {
		t.Fatalf("add initial dependency: %v", err)
	}
	beforeBlocker := taskUpdatedAt(t, store, blocker.ID)
	beforeBlocked := taskUpdatedAt(t, store, blocked.ID)
	_, err = store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocked.ID, BlockedTaskID: blocker.ID})
	assertTaskDependencyPolicyError(t, err, workflow.TaskDependencyReciprocal)
	assertTaskDependencyCount(t, store, blocker.ID, blocked.ID, 1)
	if got := taskUpdatedAt(t, store, blocker.ID); got != beforeBlocker {
		t.Fatalf("blocker timestamp after reciprocal rejection = %d, want %d", got, beforeBlocker)
	}
	if got := taskUpdatedAt(t, store, blocked.ID); got != beforeBlocked {
		t.Fatalf("blocked timestamp after reciprocal rejection = %d, want %d", got, beforeBlocked)
	}
}

func TestAddTaskDependencyAllowsThreeTaskCycle(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	tasks := []TaskRecord{
		createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "A", Body: "Body"}),
		createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "B", Body: "Body"}),
		createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "C", Body: "Body"}),
	}
	for _, pair := range [][2]int{{0, 1}, {1, 2}, {2, 0}} {
		result, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{
			BlockerTaskID: tasks[pair[0]].ID,
			BlockedTaskID: tasks[pair[1]].ID,
		})
		if err != nil {
			t.Fatalf("add cycle edge %d -> %d: %v", pair[0], pair[1], err)
		}
		if result.Outcome != TaskDependencyAdded {
			t.Fatalf("cycle edge outcome = %q, want %q", result.Outcome, TaskDependencyAdded)
		}
	}
}

func assertTaskDependencyPolicyError(t *testing.T, err error, reason workflow.TaskDependencyPolicyErrorReason) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected policy error %q", reason)
	}
	var policyErr workflow.TaskDependencyPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("error = %T %v, want TaskDependencyPolicyError", err, err)
	}
	if policyErr.Reason != reason {
		t.Fatalf("policy reason = %q, want %q", policyErr.Reason, reason)
	}
}

func assertTaskDependencyLimitError(t *testing.T, err error, reason workflow.TaskDependencyPolicyErrorReason) {
	t.Helper()
	assertTaskDependencyPolicyError(t, err, reason)
	var policyErr workflow.TaskDependencyPolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("error = %T %v, want TaskDependencyPolicyError", err, err)
	}
	if policyErr.CurrentCount == nil || *policyErr.CurrentCount != workflow.MaxTaskDependencies {
		t.Fatalf("current count = %v, want %d", policyErr.CurrentCount, workflow.MaxTaskDependencies)
	}
	if policyErr.Limit == nil || *policyErr.Limit != workflow.MaxTaskDependencies {
		t.Fatalf("limit = %v, want %d", policyErr.Limit, workflow.MaxTaskDependencies)
	}
}

func assertTaskDependencyCount(t *testing.T, store *Store, blockerID workflow.TaskID, blockedID workflow.TaskID, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRow(`
		SELECT COUNT(*)
		FROM task_dependencies
		WHERE blocker_task_id = ? AND blocked_task_id = ?
	`, blockerID, blockedID).Scan(&got); err != nil {
		t.Fatalf("count task dependency: %v", err)
	}
	if got != want {
		t.Fatalf("dependency count = %d, want %d", got, want)
	}
}
