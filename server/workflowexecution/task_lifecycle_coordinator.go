package workflowexecution

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"core/server/workflow"
)

type taskLifecycleContextKey struct{}

type taskLifecycleContext struct {
	coordinator *TaskLifecycleCoordinator
	taskID      workflow.TaskID
	parent      *taskLifecycleContext
}

func (c *taskLifecycleContext) owns(coordinator *TaskLifecycleCoordinator, taskID workflow.TaskID) bool {
	for current := c; current != nil; current = current.parent {
		if current.coordinator == coordinator && current.taskID == taskID {
			return true
		}
	}
	return false
}

type taskLifecycleWriter struct {
	token chan struct{}
	refs  int
}

// TaskLifecycleCoordinator serializes lifecycle writes for one Task while
// allowing unrelated Tasks to proceed independently.
type TaskLifecycleCoordinator struct {
	mu       sync.Mutex
	writers  map[workflow.TaskID]*taskLifecycleWriter
	released chan struct{}
}

func NewTaskLifecycleCoordinator() *TaskLifecycleCoordinator {
	return &TaskLifecycleCoordinator{
		writers:  make(map[workflow.TaskID]*taskLifecycleWriter),
		released: make(chan struct{}),
	}
}

func (c *TaskLifecycleCoordinator) Run(
	ctx context.Context,
	taskID workflow.TaskID,
	operation func(context.Context) error,
) error {
	if err := c.validate(ctx, taskID, operation); err != nil {
		return err
	}
	active, _ := ctx.Value(taskLifecycleContextKey{}).(*taskLifecycleContext)
	if active.owns(c, taskID) {
		return operation(ctx)
	}

	writer := c.referenceWriter(taskID)

	select {
	case <-ctx.Done():
		c.releaseReference(taskID, writer)
		return context.Cause(ctx)
	case <-writer.token:
	}
	defer func() {
		writer.token <- struct{}{}
		c.releaseReference(taskID, writer)
		c.notifyReleased()
	}()
	return operation(context.WithValue(ctx, taskLifecycleContextKey{}, &taskLifecycleContext{
		coordinator: c,
		taskID:      taskID,
		parent:      active,
	}))
}

func (c *TaskLifecycleCoordinator) RunTasks(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	operation func(context.Context) error,
) error {
	if c == nil {
		return errors.New("Task lifecycle coordinator is required")
	}
	if ctx == nil {
		return errors.New("Task lifecycle context is required")
	}
	if operation == nil {
		return errors.New("Task lifecycle operation is required")
	}
	ordered := append([]workflow.TaskID(nil), taskIDs...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i] < ordered[j]
	})
	unique := ordered[:0]
	for _, taskID := range ordered {
		if strings.TrimSpace(string(taskID)) == "" {
			return errors.New("workflow Task id is required")
		}
		if len(unique) == 0 || unique[len(unique)-1] != taskID {
			unique = append(unique, taskID)
		}
	}
	if len(unique) == 0 {
		return operation(ctx)
	}
	var acquire func(context.Context, int) error
	acquire = func(ctx context.Context, index int) error {
		if index == len(unique) {
			return operation(ctx)
		}
		return c.Run(ctx, unique[index], func(ctx context.Context) error {
			return acquire(ctx, index+1)
		})
	}
	return acquire(ctx, 0)
}

func (c *TaskLifecycleCoordinator) tryRun(
	ctx context.Context,
	taskID workflow.TaskID,
	operation func(context.Context) error,
) (bool, error) {
	if err := c.validate(ctx, taskID, operation); err != nil {
		return false, err
	}
	active, _ := ctx.Value(taskLifecycleContextKey{}).(*taskLifecycleContext)
	if active.owns(c, taskID) {
		return true, operation(ctx)
	}
	writer := c.referenceWriter(taskID)
	select {
	case <-ctx.Done():
		c.releaseReference(taskID, writer)
		return false, context.Cause(ctx)
	case <-writer.token:
	default:
		c.releaseReference(taskID, writer)
		return false, nil
	}
	defer func() {
		writer.token <- struct{}{}
		c.releaseReference(taskID, writer)
		c.notifyReleased()
	}()
	return true, operation(context.WithValue(ctx, taskLifecycleContextKey{}, &taskLifecycleContext{
		coordinator: c,
		taskID:      taskID,
		parent:      active,
	}))
}

func (c *TaskLifecycleCoordinator) releasedSignal() <-chan struct{} {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.released
}

func (c *TaskLifecycleCoordinator) validate(
	ctx context.Context,
	taskID workflow.TaskID,
	operation func(context.Context) error,
) error {
	if c == nil {
		return errors.New("Task lifecycle coordinator is required")
	}
	if ctx == nil {
		return errors.New("Task lifecycle context is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("workflow Task id is required")
	}
	if operation == nil {
		return errors.New("Task lifecycle operation is required")
	}
	return nil
}

func (c *TaskLifecycleCoordinator) referenceWriter(taskID workflow.TaskID) *taskLifecycleWriter {
	c.mu.Lock()
	defer c.mu.Unlock()
	writer := c.writers[taskID]
	if writer == nil {
		writer = &taskLifecycleWriter{token: make(chan struct{}, 1)}
		writer.token <- struct{}{}
		c.writers[taskID] = writer
	}
	writer.refs++
	return writer
}

func (c *TaskLifecycleCoordinator) notifyReleased() {
	c.mu.Lock()
	released := c.released
	c.released = make(chan struct{})
	close(released)
	c.mu.Unlock()
}

func runTaskLifecycle[T any](
	ctx context.Context,
	coordinator *TaskLifecycleCoordinator,
	taskID workflow.TaskID,
	operation func(context.Context) (T, error),
) (T, error) {
	var result T
	if operation == nil {
		return result, errors.New("Task lifecycle operation is required")
	}
	err := coordinator.Run(ctx, taskID, func(ctx context.Context) error {
		var err error
		result, err = operation(ctx)
		return err
	})
	return result, err
}

func (c *TaskLifecycleCoordinator) releaseReference(
	taskID workflow.TaskID,
	writer *taskLifecycleWriter,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	writer.refs--
	if writer.refs < 0 {
		panic("Task lifecycle writer reference count became negative")
	}
	if writer.refs == 0 {
		if current := c.writers[taskID]; current != writer {
			panic("Task lifecycle writer ownership changed before release")
		}
		delete(c.writers, taskID)
	}
}
