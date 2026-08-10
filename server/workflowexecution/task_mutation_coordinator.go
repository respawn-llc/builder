package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/workflow"
)

type taskMutationContextKey struct{}

type taskMutationDetachedContext struct{}

type TaskMutationTokenErrorKind string

const (
	TaskMutationTokenCrossTask    TaskMutationTokenErrorKind = "cross_task"
	TaskMutationTokenForeign      TaskMutationTokenErrorKind = "foreign"
	TaskMutationTokenForged       TaskMutationTokenErrorKind = "forged"
	TaskMutationTokenStale        TaskMutationTokenErrorKind = "stale"
	TaskMutationTokenNestedFreeze TaskMutationTokenErrorKind = "nested_freeze"
)

type TaskMutationTokenError struct {
	Kind        TaskMutationTokenErrorKind
	TaskID      workflow.TaskID
	TokenTaskID workflow.TaskID
}

func (e *TaskMutationTokenError) Error() string {
	switch e.Kind {
	case TaskMutationTokenCrossTask:
		return fmt.Sprintf("task mutation token for Task %q cannot mutate Task %q", e.TokenTaskID, e.TaskID)
	case TaskMutationTokenForeign:
		return "task mutation token belongs to another coordinator"
	case TaskMutationTokenForged:
		return "task mutation token is forged"
	case TaskMutationTokenStale:
		return "task mutation token is stale"
	case TaskMutationTokenNestedFreeze:
		return "task mutation freeze cannot be entered from coordinated mutation context"
	default:
		return "task mutation token is invalid"
	}
}

type taskMutationTokenKind uint8

const (
	taskMutationWriterToken taskMutationTokenKind = iota
	taskMutationFreezeToken
)

type taskMutationToken struct {
	coordinator *TaskMutationCoordinator
	kind        taskMutationTokenKind
	taskID      workflow.TaskID
	generation  uint64
	issued      bool
	active      bool
}

type taskMutationWaiter struct {
	ctx     context.Context
	ready   chan struct{}
	token   *taskMutationToken
	granted bool
}

type taskMutationGate struct {
	active  *taskMutationToken
	waiters []*taskMutationWaiter
}

type taskMutationFreezeWaiter struct {
	ctx     context.Context
	ready   chan struct{}
	token   *taskMutationToken
	granted bool
}

// TaskMutationCoordinator serializes execution mutations per Task. Freeze is
// the deletion-only global gate: once pending, it prevents new writers and
// waits for active writers to drain.
type TaskMutationCoordinator struct {
	mu sync.Mutex

	nextGeneration uint64
	activeWriters  int
	gates          map[workflow.TaskID]*taskMutationGate
	freezeActive   *taskMutationToken
	freezeWaiters  []*taskMutationFreezeWaiter
}

func NewTaskMutationCoordinator() *TaskMutationCoordinator {
	return &TaskMutationCoordinator{gates: make(map[workflow.TaskID]*taskMutationGate)}
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
	if strings.TrimSpace(string(taskID)) == "" {
		return errors.New("workflow task id is required")
	}
	if operation == nil {
		return errors.New("task mutation operation is required")
	}

	if value := ctx.Value(taskMutationContextKey{}); value != nil {
		if _, detached := value.(taskMutationDetachedContext); !detached {
			token, ok := value.(*taskMutationToken)
			if !ok {
				return &TaskMutationTokenError{Kind: TaskMutationTokenForged, TaskID: taskID}
			}
			c.mu.Lock()
			err := c.validateNestedWriterLocked(token, taskID)
			c.mu.Unlock()
			if err != nil {
				return err
			}
			return operation(ctx)
		}
	}

	token, err := c.acquireWriter(ctx, taskID)
	if err != nil {
		return err
	}
	mutationCtx := context.WithValue(ctx, taskMutationContextKey{}, token)
	defer c.releaseWriter(token)
	return operation(mutationCtx)
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

func (c *TaskMutationCoordinator) Freeze(ctx context.Context, operation func(context.Context) error) error {
	if c == nil {
		return errors.New("task mutation coordinator is required")
	}
	if ctx == nil {
		return errors.New("task mutation context is required")
	}
	if operation == nil {
		return errors.New("task mutation freeze operation is required")
	}
	if value := ctx.Value(taskMutationContextKey{}); value != nil {
		if _, detached := value.(taskMutationDetachedContext); !detached {
			token, ok := value.(*taskMutationToken)
			if !ok {
				return &TaskMutationTokenError{Kind: TaskMutationTokenForged}
			}
			c.mu.Lock()
			err := c.validateNestedFreezeLocked(token)
			c.mu.Unlock()
			return err
		}
	}

	token, err := c.acquireFreeze(ctx)
	if err != nil {
		return err
	}
	freezeCtx := context.WithValue(ctx, taskMutationContextKey{}, token)
	defer c.releaseFreeze(token)
	return operation(freezeCtx)
}

// WithoutTaskMutationToken preserves the parent context while ensuring
// detached work cannot inherit Task writer or deletion-freeze authority.
func WithoutTaskMutationToken(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, taskMutationContextKey{}, taskMutationDetachedContext{})
}

func (c *TaskMutationCoordinator) validateNestedWriterLocked(token *taskMutationToken, taskID workflow.TaskID) error {
	if token == nil || !token.issued {
		return &TaskMutationTokenError{Kind: TaskMutationTokenForged, TaskID: taskID}
	}
	if token.coordinator != c {
		return &TaskMutationTokenError{Kind: TaskMutationTokenForeign, TaskID: taskID, TokenTaskID: token.taskID}
	}
	if token.kind == taskMutationFreezeToken {
		return &TaskMutationTokenError{Kind: TaskMutationTokenNestedFreeze, TaskID: taskID}
	}
	if token.taskID != taskID {
		return &TaskMutationTokenError{Kind: TaskMutationTokenCrossTask, TaskID: taskID, TokenTaskID: token.taskID}
	}
	gate := c.gates[taskID]
	if !token.active || gate == nil || gate.active != token || gate.active.generation != token.generation {
		return &TaskMutationTokenError{Kind: TaskMutationTokenStale, TaskID: taskID, TokenTaskID: token.taskID}
	}
	return nil
}

func (c *TaskMutationCoordinator) validateNestedFreezeLocked(token *taskMutationToken) error {
	if token == nil || !token.issued {
		return &TaskMutationTokenError{Kind: TaskMutationTokenForged}
	}
	if token.coordinator != c {
		return &TaskMutationTokenError{Kind: TaskMutationTokenForeign, TokenTaskID: token.taskID}
	}
	if !token.active {
		return &TaskMutationTokenError{Kind: TaskMutationTokenStale, TokenTaskID: token.taskID}
	}
	switch token.kind {
	case taskMutationWriterToken:
		gate := c.gates[token.taskID]
		if gate == nil || gate.active != token || gate.active.generation != token.generation {
			return &TaskMutationTokenError{Kind: TaskMutationTokenStale, TokenTaskID: token.taskID}
		}
	case taskMutationFreezeToken:
		if c.freezeActive != token || c.freezeActive.generation != token.generation {
			return &TaskMutationTokenError{Kind: TaskMutationTokenStale}
		}
	default:
		return &TaskMutationTokenError{Kind: TaskMutationTokenForged, TokenTaskID: token.taskID}
	}
	return &TaskMutationTokenError{Kind: TaskMutationTokenNestedFreeze, TokenTaskID: token.taskID}
}

func (c *TaskMutationCoordinator) acquireWriter(ctx context.Context, taskID workflow.TaskID) (*taskMutationToken, error) {
	waiter := &taskMutationWaiter{ctx: ctx, ready: make(chan struct{})}
	c.mu.Lock()
	gate := c.gates[taskID]
	if gate == nil {
		gate = &taskMutationGate{}
		c.gates[taskID] = gate
	}
	gate.waiters = append(gate.waiters, waiter)
	c.scheduleLocked()
	c.mu.Unlock()

	select {
	case <-waiter.ready:
		if cause := context.Cause(ctx); cause != nil {
			c.releaseWriter(waiter.token)
			return nil, cause
		}
		return waiter.token, nil
	case <-ctx.Done():
		c.mu.Lock()
		if waiter.granted {
			token := waiter.token
			c.mu.Unlock()
			c.releaseWriter(token)
		} else {
			c.removeWriterWaiterLocked(taskID, waiter)
			c.scheduleLocked()
			c.mu.Unlock()
		}
		return nil, context.Cause(ctx)
	}
}

func (c *TaskMutationCoordinator) releaseWriter(token *taskMutationToken) {
	if token == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	gate := c.gates[token.taskID]
	if !token.active || gate == nil || gate.active != token || gate.active.generation != token.generation {
		panic("release invalid Task mutation writer token")
	}
	token.active = false
	gate.active = nil
	c.activeWriters--
	if len(gate.waiters) == 0 {
		delete(c.gates, token.taskID)
	}
	c.scheduleLocked()
}

func (c *TaskMutationCoordinator) acquireFreeze(ctx context.Context) (*taskMutationToken, error) {
	waiter := &taskMutationFreezeWaiter{ctx: ctx, ready: make(chan struct{})}
	c.mu.Lock()
	c.freezeWaiters = append(c.freezeWaiters, waiter)
	c.scheduleLocked()
	c.mu.Unlock()

	select {
	case <-waiter.ready:
		if cause := context.Cause(ctx); cause != nil {
			c.releaseFreeze(waiter.token)
			return nil, cause
		}
		return waiter.token, nil
	case <-ctx.Done():
		c.mu.Lock()
		if waiter.granted {
			token := waiter.token
			c.mu.Unlock()
			c.releaseFreeze(token)
		} else {
			c.removeFreezeWaiterLocked(waiter)
			c.scheduleLocked()
			c.mu.Unlock()
		}
		return nil, context.Cause(ctx)
	}
}

func (c *TaskMutationCoordinator) releaseFreeze(token *taskMutationToken) {
	if token == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !token.active || c.freezeActive != token || c.freezeActive.generation != token.generation {
		panic("release invalid Task mutation freeze token")
	}
	token.active = false
	c.freezeActive = nil
	c.scheduleLocked()
}

func (c *TaskMutationCoordinator) scheduleLocked() {
	c.pruneCanceledFreezeWaitersLocked()
	if c.freezeActive != nil {
		return
	}
	if len(c.freezeWaiters) > 0 {
		if c.activeWriters != 0 {
			return
		}
		waiter := c.freezeWaiters[0]
		c.freezeWaiters = c.freezeWaiters[1:]
		token := c.newTokenLocked(taskMutationFreezeToken, "")
		c.freezeActive = token
		waiter.token = token
		waiter.granted = true
		close(waiter.ready)
		return
	}
	for taskID, gate := range c.gates {
		if gate.active != nil {
			continue
		}
		for len(gate.waiters) > 0 {
			waiter := gate.waiters[0]
			gate.waiters = gate.waiters[1:]
			if context.Cause(waiter.ctx) != nil {
				continue
			}
			token := c.newTokenLocked(taskMutationWriterToken, taskID)
			gate.active = token
			c.activeWriters++
			waiter.token = token
			waiter.granted = true
			close(waiter.ready)
			break
		}
		if gate.active == nil && len(gate.waiters) == 0 {
			delete(c.gates, taskID)
		}
	}
}

func (c *TaskMutationCoordinator) newTokenLocked(kind taskMutationTokenKind, taskID workflow.TaskID) *taskMutationToken {
	c.nextGeneration++
	return &taskMutationToken{
		coordinator: c,
		kind:        kind,
		taskID:      taskID,
		generation:  c.nextGeneration,
		issued:      true,
		active:      true,
	}
}

func (c *TaskMutationCoordinator) pruneCanceledFreezeWaitersLocked() {
	pending := c.freezeWaiters[:0]
	for _, waiter := range c.freezeWaiters {
		if context.Cause(waiter.ctx) == nil {
			pending = append(pending, waiter)
		}
	}
	c.freezeWaiters = pending
}

func (c *TaskMutationCoordinator) removeWriterWaiterLocked(taskID workflow.TaskID, target *taskMutationWaiter) {
	gate := c.gates[taskID]
	if gate == nil {
		return
	}
	waiters := gate.waiters[:0]
	for _, waiter := range gate.waiters {
		if waiter != target {
			waiters = append(waiters, waiter)
		}
	}
	gate.waiters = waiters
	if gate.active == nil && len(gate.waiters) == 0 {
		delete(c.gates, taskID)
	}
}

func (c *TaskMutationCoordinator) removeFreezeWaiterLocked(target *taskMutationFreezeWaiter) {
	waiters := c.freezeWaiters[:0]
	for _, waiter := range c.freezeWaiters {
		if waiter != target {
			waiters = append(waiters, waiter)
		}
	}
	c.freezeWaiters = waiters
}
