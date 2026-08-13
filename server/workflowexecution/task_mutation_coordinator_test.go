package workflowexecution

import (
	"context"
	"testing"
	"time"

	"core/server/workflow"
)

func TestTaskMutationCoordinatorSerializesSameTask(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	taskID := workflow.TaskID("task-same")
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
		t.Fatal("second same-Task mutation entered while first mutation held Task ownership")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first mutation: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second mutation: %v", err)
	}
}

func TestTaskMutationCoordinatorDoesNotBlockDifferentTask(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.Run(context.Background(), workflow.TaskID("task-first"), func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- coordinator.Run(context.Background(), workflow.TaskID("task-second"), func(context.Context) error {
			return nil
		})
	}()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("different Task mutation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("different Task waited for unrelated Task mutation")
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Task mutation: %v", err)
	}
}

func TestTaskMutationCoordinatorReusesSameTaskFromContext(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	taskID := workflow.TaskID("task-reentrant")
	err := coordinator.Run(context.Background(), taskID, func(ctx context.Context) error {
		return coordinator.Run(ctx, taskID, func(context.Context) error {
			return nil
		})
	})
	if err != nil {
		t.Fatalf("nested Task mutation: %v", err)
	}
}

func TestTaskMutationCoordinatorRunManyUsesDeterministicTaskOrder(t *testing.T) {
	coordinator := NewTaskMutationCoordinator()
	firstTask := workflow.TaskID("task-a")
	secondTask := workflow.TaskID("task-b")
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- coordinator.Run(context.Background(), firstTask, func(context.Context) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	manyEntered := make(chan struct{})
	manyDone := make(chan error, 1)
	go func() {
		manyDone <- coordinator.RunMany(context.Background(), []workflow.TaskID{secondTask, firstTask}, func(context.Context) error {
			close(manyEntered)
			return nil
		})
	}()
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- coordinator.Run(context.Background(), secondTask, func(context.Context) error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("multi-Task mutation held later Task while waiting for earlier Task")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Task mutation: %v", err)
	}
	select {
	case <-manyEntered:
		t.Fatal("multi-Task mutation entered before first Task released")
	default:
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Task mutation: %v", err)
	}
	if err := <-manyDone; err != nil {
		t.Fatalf("multi-Task mutation: %v", err)
	}
}
