package workflowexecution

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/workflow"
)

func TestTaskLifecycleCoordinatorSerializesSameTaskWriters(t *testing.T) {
	coordinator := NewTaskLifecycleCoordinator()
	taskID := workflow.TaskID("task-serialized")
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
		t.Fatal("same-Task writer entered while the first writer still owned the Task")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Task writer: %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("same-Task writer did not enter after the first writer released the Task")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Task writer: %v", err)
	}
}

func TestTaskLifecycleCoordinatorDoesNotBlockUnrelatedTaskWriters(t *testing.T) {
	coordinator := NewTaskLifecycleCoordinator()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.Run(context.Background(), workflow.TaskID("task-slow"), func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	unrelatedDone := make(chan error, 1)
	go func() {
		unrelatedDone <- coordinator.Run(context.Background(), workflow.TaskID("task-unrelated"), func(context.Context) error {
			return nil
		})
	}()
	select {
	case err := <-unrelatedDone:
		if err != nil {
			t.Fatalf("unrelated Task writer: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unrelated Task writer waited for a slow Task")
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("slow Task writer: %v", err)
	}
}

func TestTaskLifecycleCoordinatorCancelsWaitingWriter(t *testing.T) {
	coordinator := NewTaskLifecycleCoordinator()
	taskID := workflow.TaskID("task-cancelled-writer")
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

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	entered := false
	err := coordinator.Run(ctx, taskID, func(context.Context) error {
		entered = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Task writer error = %v, want context cancellation", err)
	}
	if entered {
		t.Fatal("cancelled Task writer entered its operation")
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Task writer: %v", err)
	}
}

func TestTaskLifecycleCoordinatorBroadcastsReleaseToEveryObserver(t *testing.T) {
	coordinator := NewTaskLifecycleCoordinator()
	first := coordinator.releasedSignal()
	second := coordinator.releasedSignal()

	if err := coordinator.Run(
		context.Background(),
		workflow.TaskID("task-release-broadcast"),
		func(context.Context) error { return nil },
	); err != nil {
		t.Fatalf("run Task lifecycle writer: %v", err)
	}

	for index, signal := range []<-chan struct{}{first, second} {
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatalf("release observer %d did not receive the Task lifecycle release", index+1)
		}
	}

	next := coordinator.releasedSignal()
	select {
	case <-next:
		t.Fatal("next release generation was already closed")
	default:
	}
	if err := coordinator.Run(
		context.Background(),
		workflow.TaskID("task-next-release-broadcast"),
		func(context.Context) error { return nil },
	); err != nil {
		t.Fatalf("run next Task lifecycle writer: %v", err)
	}
	select {
	case <-next:
	case <-time.After(time.Second):
		t.Fatal("next release observer did not receive the next Task lifecycle release")
	}
}

func TestTaskLifecycleCoordinatorReusesSameTaskWriterFromContext(t *testing.T) {
	coordinator := NewTaskLifecycleCoordinator()
	taskID := workflow.TaskID("task-reentrant-writer")
	err := coordinator.Run(context.Background(), taskID, func(ctx context.Context) error {
		return coordinator.Run(ctx, taskID, func(context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("nested same-Task writer: %v", err)
	}
}

func TestTaskLifecycleCoordinatorAcquiresMultipleTasksInCanonicalOrder(t *testing.T) {
	coordinator := NewTaskLifecycleCoordinator()
	taskA := workflow.TaskID("task-a")
	taskB := workflow.TaskID("task-b")
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.RunTasks(
			context.Background(),
			[]workflow.TaskID{taskB, taskA},
			func(context.Context) error {
				close(firstEntered)
				<-releaseFirst
				return nil
			},
		)
	}()
	<-firstEntered

	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- coordinator.RunTasks(
			context.Background(),
			[]workflow.TaskID{taskA, taskB},
			func(context.Context) error {
				close(secondEntered)
				return nil
			},
		)
	}()
	select {
	case <-secondEntered:
		t.Fatal("opposite-order multi-Task writer entered before the canonical owner released")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first multi-Task writer: %v", err)
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("opposite-order multi-Task writer did not enter after release")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second multi-Task writer: %v", err)
	}
}
