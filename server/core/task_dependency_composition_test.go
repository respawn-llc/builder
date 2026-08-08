package core

import (
	"context"
	"errors"
	"testing"
)

type compositionTaskDependencyCounter func(context.Context, string) (int, error)

func (c compositionTaskDependencyCounter) CountUnsatisfiedBlockers(ctx context.Context, taskID string) (int, error) {
	return c(ctx, taskID)
}

func TestWorkflowTaskDependencyCompositionCellBindsExactlyOnceBeforeReads(t *testing.T) {
	cell := &workflowTaskDependencyCompositionCell{}
	if _, err := cell.CountUnsatisfiedBlockers(t.Context(), "task-before-bind"); err == nil {
		t.Fatal("dependency composition cell allowed a read before binding")
	}
	wantErr := errors.New("dependency failure")
	authority := compositionTaskDependencyCounter(func(_ context.Context, taskID string) (int, error) {
		if taskID != "task-after-bind" {
			t.Fatalf("delegated Task ID = %q", taskID)
		}
		return 7, wantErr
	})
	if err := cell.bindTaskDependencies(authority); err != nil {
		t.Fatalf("bind dependency authority: %v", err)
	}
	if got, err := cell.CountUnsatisfiedBlockers(t.Context(), "task-after-bind"); got != 7 || !errors.Is(err, wantErr) {
		t.Fatalf("delegated count = %d, err = %v; want 7, %v", got, err, wantErr)
	}
	if err := cell.bindTaskDependencies(authority); err == nil {
		t.Fatal("dependency composition cell allowed repeated binding")
	}
}
