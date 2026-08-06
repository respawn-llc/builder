package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
	"core/shared/runtimeids"
)

func TestLifecyclePublicationCompleteEntryCreateAndRemove(t *testing.T) {
	ctx, store, _, _ := newTestStoreWithConfigContext(t)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	reference, err := workflow.NewCurrentNodeReference(
		workflow.TaskID("task-complete-entry"),
		workflow.NodeID("node-agent"),
		nil,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	addRun, err := NewTaskLifecycleDelta(reference.TaskID, []LifecycleRunDelta{{
		CurrentNode: reference,
		Expect:      LifecycleFieldAbsent,
		Next:        LifecycleFieldPresent,
	}}, nil)
	if err != nil {
		t.Fatalf("NewTaskLifecycleDelta add Run: %v", err)
	}
	if err := publication.Publish(ctx, addRun); err != nil {
		t.Fatalf("publish Run creation: %v", err)
	}
	scopeID := runtimeids.NewExecutionScopeID()
	addExact, err := NewTaskLifecycleDelta(reference.TaskID, nil, []LifecycleExactDelta{{
		CurrentNode: reference,
		Next:        lifecycleExactDeltaExecution(reference, scopeID),
	}})
	if err != nil {
		t.Fatalf("NewTaskLifecycleDelta add Exact: %v", err)
	}
	if err := publication.Publish(ctx, addExact); err != nil {
		t.Fatalf("publish Exact creation: %v", err)
	}
	removeRun, err := NewTaskLifecycleDelta(reference.TaskID, []LifecycleRunDelta{{
		CurrentNode: reference,
		Expect:      LifecycleFieldPresent,
		Next:        LifecycleFieldAbsent,
	}}, nil)
	if err != nil {
		t.Fatalf("NewTaskLifecycleDelta remove Run: %v", err)
	}
	if err := publication.Publish(ctx, removeRun); err != nil {
		t.Fatalf("publish Run removal: %v", err)
	}
	if exact := publication.root[reference.TaskID].exact; len(exact) != 1 {
		t.Fatalf("entry after lifecycle-only removal has %d Exact fields, want 1", len(exact))
	}
	removeExact, err := NewTaskLifecycleDelta(reference.TaskID, nil, []LifecycleExactDelta{{
		CurrentNode: reference,
		ExpectScope: &scopeID,
	}})
	if err != nil {
		t.Fatalf("NewTaskLifecycleDelta remove Exact: %v", err)
	}
	if err := publication.Publish(ctx, removeExact); err != nil {
		t.Fatalf("publish Exact removal: %v", err)
	}
	if _, exists := publication.root[reference.TaskID]; exists {
		t.Fatal("complete lifecycle entry remained after its lifecycle and Exact fields were removed")
	}
}

func TestLifecyclePublicationExpectedScopeConflictRollsBackBeforeCommit(t *testing.T) {
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	started, err := publication.PublishTaskStart(
		ctx,
		task.ID,
		func(result StartTaskResult) (TaskLifecycleDelta, func(error), error) {
			delta, err := NewTaskStartLifecycleDelta(result)
			return delta, nil, err
		},
	)
	if err != nil {
		t.Fatalf("PublishTaskStart: %v", err)
	}
	reference := started.Mutation.Created[0].Reference
	actualScope := runtimeids.NewExecutionScopeID()
	seed, err := NewTaskLifecycleDelta(reference.TaskID, nil, []LifecycleExactDelta{{
		CurrentNode: reference,
		Next:        lifecycleExactDeltaExecution(reference, actualScope),
	}})
	if err != nil {
		t.Fatalf("NewTaskLifecycleDelta seed: %v", err)
	}
	if err := publication.Publish(ctx, seed); err != nil {
		t.Fatalf("publish seed: %v", err)
	}
	staleScope := runtimeids.NewExecutionScopeID()
	conflict, err := NewTaskLifecycleDelta(reference.TaskID, []LifecycleRunDelta{{
		CurrentNode: reference,
		Expect:      LifecycleFieldPresent,
		Next:        LifecycleFieldAbsent,
	}}, []LifecycleExactDelta{{
		CurrentNode: reference,
		ExpectScope: &staleScope,
	}})
	if err != nil {
		t.Fatalf("NewTaskLifecycleDelta conflict: %v", err)
	}
	prepared, err := store.prepareCurrentNodeAdmission(ctx, reference)
	if err != nil {
		t.Fatalf("prepareCurrentNodeAdmission: %v", err)
	}
	if err := publication.publishPrepared(context.Background(), prepared, conflict); err == nil {
		t.Fatal("stale Exact predecessor publication succeeded")
	}
	currentNodes, err := store.ListCurrentNodes(ctx, reference.TaskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after rollback: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("durable Current Nodes after conflict = %+v, want ready predecessor", currentNodes)
	}
	entry := publication.root[reference.TaskID]
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("Current Node key: %v", err)
	}
	if entry.exact[key].ScopeID != actualScope {
		t.Fatalf("published Exact Scope = %s, want %s", entry.exact[key].ScopeID, actualScope)
	}
}

func lifecycleExactDeltaExecution(
	reference workflow.CurrentNodeReference,
	scopeID runtimeids.ExecutionScopeID,
) *LifecycleExactExecution {
	return &LifecycleExactExecution{
		ProjectID:   "project-test",
		WorkflowID:  runtimeids.NewWorkflowID(),
		CurrentNode: reference,
		ScopeID:     scopeID,
		Script:      &LifecycleScriptExecutionTarget{Path: "/test/script"},
		Phase:       LifecycleExactExecutionRunning,
	}
}

func TestLifecyclePublicationSimultaneousDifferentTaskRootOnlyDeltasPreserveBothEntries(t *testing.T) {
	ctx, store, _, _ := newTestStoreWithConfigContext(t)
	publication, err := NewLifecyclePublication(store)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	first := lifecyclePublicationTestReference(t, "task-root-first")
	second := lifecyclePublicationTestReference(t, "task-root-second")
	firstDelta := lifecyclePublicationAddRunDelta(t, first)
	secondDelta := lifecyclePublicationAddRunDelta(t, second)

	publication.mu.Lock()
	started := make(chan struct{}, 2)
	results := make(chan error, 2)
	go func() {
		started <- struct{}{}
		results <- publication.Publish(ctx, firstDelta)
	}()
	go func() {
		started <- struct{}{}
		results <- publication.Publish(ctx, secondDelta)
	}()
	<-started
	<-started
	publication.mu.Unlock()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("publish root-only delta: %v", err)
		}
	}
	requireLifecyclePublicationQueued(t, ctx, publication, first, second)
}

func TestLifecyclePublicationSimultaneousDifferentTaskSQLiteBackedDeltasPreserveBothEntries(t *testing.T) {
	ctx := context.Background()
	_, publicationStore, _, _ := newTestStoreWithConfigContext(t)
	publication, err := NewLifecyclePublication(publicationStore)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	firstStore, first, firstPrepared := prepareLifecyclePublicationAdmission(t)
	secondStore, second, secondPrepared := prepareLifecyclePublicationAdmission(t)
	firstDelta := lifecyclePublicationAddRunDelta(t, first)
	secondDelta := lifecyclePublicationAddRunDelta(t, second)

	publication.mu.Lock()
	started := make(chan struct{}, 2)
	results := make(chan error, 2)
	go func() {
		started <- struct{}{}
		results <- publication.publishPrepared(ctx, firstPrepared, firstDelta)
	}()
	go func() {
		started <- struct{}{}
		results <- publication.publishPrepared(ctx, secondPrepared, secondDelta)
	}()
	<-started
	<-started
	publication.mu.Unlock()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("publish prepared delta: %v", err)
		}
	}
	requireLifecyclePublicationQueued(t, ctx, publication, first, second)
	requireLifecyclePublicationAdmitted(t, ctx, firstStore, first)
	requireLifecyclePublicationAdmitted(t, ctx, secondStore, second)
}

func TestLifecyclePublicationSimultaneousDifferentTaskRootOnlyAndSQLiteBackedDeltasPreserveBothEntries(t *testing.T) {
	ctx := context.Background()
	_, publicationStore, _, _ := newTestStoreWithConfigContext(t)
	publication, err := NewLifecyclePublication(publicationStore)
	if err != nil {
		t.Fatalf("NewLifecyclePublication: %v", err)
	}
	rootOnly := lifecyclePublicationTestReference(t, "task-mixed-root")
	preparedStore, preparedReference, prepared := prepareLifecyclePublicationAdmission(t)
	rootOnlyDelta := lifecyclePublicationAddRunDelta(t, rootOnly)
	preparedDelta := lifecyclePublicationAddRunDelta(t, preparedReference)

	publication.mu.Lock()
	started := make(chan struct{}, 2)
	results := make(chan error, 2)
	go func() {
		started <- struct{}{}
		results <- publication.Publish(ctx, rootOnlyDelta)
	}()
	go func() {
		started <- struct{}{}
		results <- publication.publishPrepared(ctx, prepared, preparedDelta)
	}()
	<-started
	<-started
	publication.mu.Unlock()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("publish mixed delta: %v", err)
		}
	}
	requireLifecyclePublicationQueued(t, ctx, publication, rootOnly, preparedReference)
	requireLifecyclePublicationAdmitted(t, ctx, preparedStore, preparedReference)
}

func prepareLifecyclePublicationAdmission(
	t *testing.T,
) (*Store, workflow.CurrentNodeReference, *preparedSQLLifecycleMutation) {
	t.Helper()
	ctx, store, binding, _ := newTestStoreWithConfigContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started, err := publishTaskStartForTest(ctx, store, task.ID)
	if err != nil {
		t.Fatalf("publishTaskStartForTest: %v", err)
	}
	reference := started.Mutation.Created[0].Reference
	prepared, err := store.prepareCurrentNodeAdmission(ctx, reference)
	if err != nil {
		t.Fatalf("prepareCurrentNodeAdmission: %v", err)
	}
	return store, reference, prepared
}

func requireLifecyclePublicationAdmitted(
	t *testing.T,
	ctx context.Context,
	store *Store,
	reference workflow.CurrentNodeReference,
) {
	t.Helper()
	currentNodes, err := store.ListCurrentNodes(ctx, reference.TaskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingAdmitted {
		t.Fatalf("durable Current Nodes = %+v, want admitted %v", currentNodes, reference)
	}
}

func lifecyclePublicationTestReference(t *testing.T, taskID string) workflow.CurrentNodeReference {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(
		workflow.TaskID(taskID),
		workflow.NodeID("node-agent"),
		nil,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	return reference
}

func lifecyclePublicationAddRunDelta(
	t *testing.T,
	reference workflow.CurrentNodeReference,
) TaskLifecycleDelta {
	t.Helper()
	delta, err := NewTaskLifecycleDelta(reference.TaskID, []LifecycleRunDelta{{
		CurrentNode: reference,
		Expect:      LifecycleFieldAbsent,
		Next:        LifecycleFieldPresent,
	}}, nil)
	if err != nil {
		t.Fatalf("NewTaskLifecycleDelta: %v", err)
	}
	return delta
}

func requireLifecyclePublicationQueued(
	t *testing.T,
	ctx context.Context,
	publication *LifecyclePublication,
	references ...workflow.CurrentNodeReference,
) {
	t.Helper()
	capture, err := publication.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	defer func() {
		if err := capture.Close(); err != nil {
			t.Errorf("close capture: %v", err)
		}
	}()
	for _, reference := range references {
		queued := capture.QueuedCurrentNodes(reference.TaskID)
		if len(queued) != 1 || !queued[0].Equal(reference) {
			t.Fatalf("queued Current Nodes for %s = %+v, want %v", reference.TaskID, queued, reference)
		}
	}
}
