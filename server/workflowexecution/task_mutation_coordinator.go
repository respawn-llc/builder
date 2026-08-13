package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"core/server/workflow"
)

type taskMutationContextKey struct{}

type activeTaskMutation struct {
	coordinator *TaskMutationCoordinator
	taskID      workflow.TaskID
	parent      *activeTaskMutation
}

type taskMutationEntry struct {
	token chan struct{}
	refs  int
}

// TaskMutationCoordinator serializes lifecycle mutations for one Task while
// allowing unrelated Tasks to proceed independently.
type TaskMutationCoordinator struct {
	mu      sync.Mutex
	entries map[workflow.TaskID]*taskMutationEntry
}

func NewTaskMutationCoordinator() *TaskMutationCoordinator {
	return &TaskMutationCoordinator{entries: make(map[workflow.TaskID]*taskMutationEntry)}
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
	entry := c.retain(taskID)
	defer c.release(taskID, entry)
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-entry.token:
	}
	defer func() { entry.token <- struct{}{} }()
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

func (c *TaskMutationCoordinator) retain(taskID workflow.TaskID) *taskMutationEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[taskID]
	if entry == nil {
		entry = &taskMutationEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		c.entries[taskID] = entry
	}
	entry.refs++
	return entry
}

func (c *TaskMutationCoordinator) release(taskID workflow.TaskID, entry *taskMutationEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.refs--
	if entry.refs == 0 {
		delete(c.entries, taskID)
	}
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
