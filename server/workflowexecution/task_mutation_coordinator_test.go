package workflowexecution

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/workflow"
)

func TestTaskMutationCoordinatorSerializesSameTaskWriters(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	taskID := workflow.TaskID("task-serialization")
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.Run(context.Background(), taskID, func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- coordinator.Run(context.Background(), taskID, func(context.Context) error {
			close(secondEntered)
			return nil
		})
	}()

	select {
	case <-secondEntered:
		t.Fatal("second same-Task writer entered while the first writer was active")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first writer: %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("second same-Task writer did not enter after release")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second writer: %v", err)
	}
}

func TestTaskMutationCoordinatorAllowsDifferentTaskWritersIndependently(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.Run(context.Background(), "task-one", func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- coordinator.Run(context.Background(), "task-two", func(context.Context) error {
			return nil
		})
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("different-Task writer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("different-Task writer waited for unrelated Task")
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first writer: %v", err)
	}
}

func TestTaskMutationCoordinatorAllowsOnlyActiveSameTaskNesting(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	var captured context.Context
	err := coordinator.Run(context.Background(), "task-one", func(ctx context.Context) error {
		captured = ctx
		return coordinator.Run(ctx, "task-one", func(context.Context) error { return nil })
	})
	if err != nil {
		t.Fatalf("active same-Task nesting: %v", err)
	}

	called := false
	err = coordinator.Run(captured, "task-one", func(context.Context) error {
		called = true
		return nil
	})
	requireTaskMutationTokenError(t, err, TaskMutationTokenStale)
	if called {
		t.Fatal("escaped stale token invoked nested callback")
	}
}

func TestTaskMutationCoordinatorRejectsCrossTaskForeignAndForgedTokens(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	foreignCoordinator := NewTaskMutationCoordinator()

	err := coordinator.Run(context.Background(), "task-one", func(ctx context.Context) error {
		called := false
		err := coordinator.Run(ctx, "task-two", func(context.Context) error {
			called = true
			return nil
		})
		requireTaskMutationTokenError(t, err, TaskMutationTokenCrossTask)
		if called {
			t.Fatal("cross-Task token invoked nested callback")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer writer: %v", err)
	}

	err = foreignCoordinator.Run(context.Background(), "task-one", func(ctx context.Context) error {
		called := false
		err := coordinator.Run(ctx, "task-one", func(context.Context) error {
			called = true
			return nil
		})
		requireTaskMutationTokenError(t, err, TaskMutationTokenForeign)
		if called {
			t.Fatal("foreign token invoked nested callback")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("foreign outer writer: %v", err)
	}

	forged := context.WithValue(context.Background(), taskMutationContextKey{}, &taskMutationToken{
		coordinator: coordinator,
		kind:        taskMutationWriterToken,
		taskID:      "task-one",
		generation:  1,
	})
	called := false
	err = coordinator.Run(forged, "task-one", func(context.Context) error {
		called = true
		return nil
	})
	requireTaskMutationTokenError(t, err, TaskMutationTokenForged)
	if called {
		t.Fatal("forged token invoked nested callback")
	}
}

func TestTaskMutationCoordinatorFreezeBlocksNewWritersAndDrainsActiveWritersFairly(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	activeEntered := make(chan struct{})
	releaseActive := make(chan struct{})
	activeDone := make(chan error, 1)
	go func() {
		activeDone <- coordinator.Run(context.Background(), "task-active", func(context.Context) error {
			close(activeEntered)
			<-releaseActive
			return nil
		})
	}()
	<-activeEntered

	freezeEntered := make(chan struct{})
	releaseFreeze := make(chan struct{})
	freezeDone := make(chan error, 1)
	freezePending := newCoordinatorContextCheckBarrier()
	go func() {
		freezeDone <- coordinator.Freeze(freezePending, func(context.Context) error {
			close(freezeEntered)
			<-releaseFreeze
			return nil
		})
	}()
	<-freezePending.checked

	blockedWriterEntered := make(chan struct{})
	blockedWriterDone := make(chan error, 1)
	go func() {
		blockedWriterDone <- coordinator.Run(context.Background(), "task-new", func(context.Context) error {
			close(blockedWriterEntered)
			return nil
		})
	}()
	close(freezePending.release)

	select {
	case <-freezeEntered:
		t.Fatal("freeze entered before the active writer drained")
	case <-blockedWriterEntered:
		t.Fatal("new writer bypassed a pending freeze")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseActive)
	if err := <-activeDone; err != nil {
		t.Fatalf("active writer: %v", err)
	}
	select {
	case <-freezeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("freeze did not enter after active writers drained")
	}
	select {
	case <-blockedWriterEntered:
		t.Fatal("new writer entered during active freeze")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFreeze)
	if err := <-freezeDone; err != nil {
		t.Fatalf("freeze: %v", err)
	}
	select {
	case <-blockedWriterEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("blocked writer did not enter after freeze")
	}
	if err := <-blockedWriterDone; err != nil {
		t.Fatalf("blocked writer: %v", err)
	}
}

type coordinatorContextCheckBarrier struct {
	context.Context
	checked chan struct{}
	release chan struct{}
	once    sync.Once
}

func newCoordinatorContextCheckBarrier() *coordinatorContextCheckBarrier {
	return &coordinatorContextCheckBarrier{
		Context: context.Background(),
		checked: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *coordinatorContextCheckBarrier) Err() error {
	c.once.Do(func() {
		close(c.checked)
		<-c.release
	})
	return nil
}

func TestTaskMutationCoordinatorCanceledFreezeUnblocksWriters(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	activeEntered := make(chan struct{})
	releaseActive := make(chan struct{})
	activeDone := make(chan error, 1)
	go func() {
		activeDone <- coordinator.Run(context.Background(), "task-active", func(context.Context) error {
			close(activeEntered)
			<-releaseActive
			return nil
		})
	}()
	<-activeEntered

	freezeCtx, cancelFreeze := context.WithCancel(context.Background())
	freezeDone := make(chan error, 1)
	go func() {
		freezeDone <- coordinator.Freeze(freezeCtx, func(context.Context) error {
			t.Error("canceled pending freeze invoked its callback")
			return nil
		})
	}()
	cancelFreeze()
	if err := <-freezeDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled freeze = %v, want context cancellation", err)
	}

	writerDone := make(chan error, 1)
	go func() {
		writerDone <- coordinator.Run(context.Background(), "task-new", func(context.Context) error {
			return nil
		})
	}()
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatalf("writer after canceled freeze: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled freeze continued blocking unrelated writer")
	}
	close(releaseActive)
	if err := <-activeDone; err != nil {
		t.Fatalf("active writer: %v", err)
	}
}

func TestTaskMutationCoordinatorFailedFreezeUnblocksWriters(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	failure := errors.New("delete failed")
	if err := coordinator.Freeze(context.Background(), func(context.Context) error {
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("failed freeze = %v, want %v", err, failure)
	}
	if err := coordinator.Run(context.Background(), "task-after-failure", func(context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("writer after failed freeze: %v", err)
	}
}

func TestTaskMutationCoordinatorRejectsNestedFreeze(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	err := coordinator.Run(context.Background(), "task-one", func(ctx context.Context) error {
		return coordinator.Freeze(ctx, func(context.Context) error {
			t.Fatal("nested freeze callback invoked")
			return nil
		})
	})
	requireTaskMutationTokenError(t, err, TaskMutationTokenNestedFreeze)

	err = coordinator.Freeze(context.Background(), func(ctx context.Context) error {
		return coordinator.Freeze(ctx, func(context.Context) error {
			t.Fatal("nested freeze callback invoked")
			return nil
		})
	})
	requireTaskMutationTokenError(t, err, TaskMutationTokenNestedFreeze)
}

func TestTaskMutationCoordinatorEscapedContextCannotBypassLaterWriterOrFreeze(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	taskID := workflow.TaskID("task-escaped")
	var escaped context.Context
	if err := coordinator.Run(context.Background(), taskID, func(ctx context.Context) error {
		escaped = ctx
		return nil
	}); err != nil {
		t.Fatalf("capture writer context: %v", err)
	}

	laterEntered := make(chan struct{})
	releaseLater := make(chan struct{})
	laterDone := make(chan error, 1)
	go func() {
		laterDone <- coordinator.Run(context.Background(), taskID, func(context.Context) error {
			close(laterEntered)
			<-releaseLater
			return nil
		})
	}()
	<-laterEntered
	assertEscapedContextRejected(t, coordinator, escaped, taskID)

	freezeEntered := make(chan struct{})
	releaseFreeze := make(chan struct{})
	freezeDone := make(chan error, 1)
	go func() {
		freezeDone <- coordinator.Freeze(context.Background(), func(context.Context) error {
			close(freezeEntered)
			<-releaseFreeze
			return nil
		})
	}()
	assertEscapedContextRejected(t, coordinator, escaped, taskID)
	close(releaseLater)
	if err := <-laterDone; err != nil {
		t.Fatalf("later writer: %v", err)
	}
	<-freezeEntered
	assertEscapedContextRejected(t, coordinator, escaped, taskID)
	close(releaseFreeze)
	if err := <-freezeDone; err != nil {
		t.Fatalf("freeze: %v", err)
	}
}

func TestTaskMutationCoordinatorDetachedContextCarriesNoToken(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	taskID := workflow.TaskID("task-detached")
	var detached context.Context
	if err := coordinator.Run(context.Background(), taskID, func(ctx context.Context) error {
		detached = WithoutTaskMutationToken(ctx)
		return nil
	}); err != nil {
		t.Fatalf("outer writer: %v", err)
	}
	if err := coordinator.Run(detached, taskID, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("detached writer: %v", err)
	}
}

func assertEscapedContextRejected(
	t testing.TB,
	coordinator *TaskMutationCoordinator,
	ctx context.Context,
	taskID workflow.TaskID,
) {
	t.Helper()
	called := false
	err := coordinator.Run(ctx, taskID, func(context.Context) error {
		called = true
		return nil
	})
	requireTaskMutationTokenError(t, err, TaskMutationTokenStale)
	if called {
		t.Fatal("escaped stale token invoked nested callback")
	}
}

func requireTaskMutationTokenError(t testing.TB, err error, kind TaskMutationTokenErrorKind) {
	t.Helper()
	var tokenErr *TaskMutationTokenError
	if !errors.As(err, &tokenErr) || tokenErr.Kind != kind {
		t.Fatalf("error = %v, want TaskMutationTokenError kind %q", err, kind)
	}
}
