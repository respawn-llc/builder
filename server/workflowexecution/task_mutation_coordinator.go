package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/server/metadata"
	"core/server/workflow"
)

type taskMutationContextKey struct{}

type activeTaskMutation struct {
	coordinator *TaskMutationCoordinator
	taskID      workflow.TaskID
	parent      *activeTaskMutation
}

// TaskMutationCoordinator serializes lifecycle mutations for one Task while
// allowing unrelated Tasks to proceed independently.
type TaskMutationCoordinator struct {
	lanes *metadata.MutationLaneRegistry[workflow.TaskID]
}

func NewTaskMutationCoordinator() *TaskMutationCoordinator {
	return &TaskMutationCoordinator{
		lanes: metadata.NewMutationLaneRegistry[workflow.TaskID](),
	}
}

func (c *TaskMutationCoordinator) Run(
	ctx context.Context,
	taskID workflow.TaskID,
	operation func(context.Context) error,
) error {
	if c == nil {
		return errors.New("task mutation coordinator is required")
	}
	if ctx == nil {
		return errors.New("task mutation context is required")
	}
	if operation == nil {
		return errors.New("task mutation operation is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("workflow task id is required")
	}
	active, _ := ctx.Value(taskMutationContextKey{}).(*activeTaskMutation)
	for current := active; current != nil; current = current.parent {
		if current.coordinator == c && current.taskID == taskID {
			return operation(ctx)
		}
	}
	if c.lanes == nil {
		return errors.New("task mutation coordinator is uninitialized")
	}
	lease, err := c.lanes.Acquire(ctx, taskID)
	if err != nil {
		return err
	}
	defer lease.Release()
	return operation(context.WithValue(ctx, taskMutationContextKey{}, &activeTaskMutation{
		coordinator: c,
		taskID:      taskID,
		parent:      active,
	}))
}

func (c *TaskMutationCoordinator) RunMany(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	operation func(context.Context) error,
) error {
	if operation == nil {
		return errors.New("task mutation operation is required")
	}
	ordered := append([]workflow.TaskID(nil), taskIDs...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for index, taskID := range ordered {
		if strings.TrimSpace(string(taskID)) == "" {
			return fmt.Errorf("workflow task id at index %d is required", index)
		}
		if index > 0 && taskID == ordered[index-1] {
			return fmt.Errorf("workflow task id %q is duplicated", taskID)
		}
	}
	var run func(context.Context, int) error
	run = func(ctx context.Context, index int) error {
		if index == len(ordered) {
			return operation(ctx)
		}
		return c.Run(ctx, ordered[index], func(ctx context.Context) error {
			return run(ctx, index+1)
		})
	}
	return run(ctx, 0)
}
func RunTaskMutation[T any](
	ctx context.Context,
	coordinator *TaskMutationCoordinator,
	taskID workflow.TaskID,
	operation func(context.Context) (T, error),
) (T, error) {
	var result T
	if operation == nil {
		return result, errors.New("task mutation operation is required")
	}
	err := coordinator.Run(ctx, taskID, func(ctx context.Context) error {
		var err error
		result, err = operation(ctx)
		return err
	})
	return result, err
}
