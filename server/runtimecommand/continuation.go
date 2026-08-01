package runtimecommand

import (
	"context"
	"errors"
	"sync"

	runtimepkg "core/server/runtime"
	"core/server/sessionruntime"
)

type Continuation struct {
	resource *resourceQueue
	target   Target
	slot     chan struct{}
	done     chan struct{}

	mu       sync.Mutex
	pending  bool
	released bool
}

func newContinuation(resource *resourceQueue, target Target) *Continuation {
	slot := make(chan struct{}, 1)
	slot <- struct{}{}
	return &Continuation{resource: resource, target: target, slot: slot, done: make(chan struct{})}
}

func (c *Continuation) bindTarget(target Target) error {
	if c == nil {
		return ErrTurnExpired
	}
	if err := target.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released || c.pending {
		return ErrTurnExpired
	}
	c.target = target
	return nil
}

func Reenter[T any](
	ctx context.Context,
	continuation *Continuation,
	apply func(Turn) (T, error),
) (Future[T], error) {
	if continuation == nil {
		return Future[T]{}, errors.New("runtime command continuation is required")
	}
	if apply == nil {
		return Future[T]{}, ErrCommandHandlerNeeded
	}
	select {
	case <-continuation.slot:
	case <-continuation.done:
		return Future[T]{}, ErrTurnExpired
	case <-continuation.resource.closedCh:
		return Future[T]{}, ErrResourceUnavailable
	case <-ctx.Done():
		return Future[T]{}, context.Cause(ctx)
	}

	continuation.mu.Lock()
	if continuation.released {
		continuation.mu.Unlock()
		continuation.slot <- struct{}{}
		return Future[T]{}, ErrTurnExpired
	}
	continuation.pending = true
	resource := continuation.resource
	resource.mu.Lock()
	if resource.closed {
		resource.mu.Unlock()
		continuation.pending = false
		continuation.mu.Unlock()
		continuation.slot <- struct{}{}
		return Future[T]{}, ErrResourceUnavailable
	}
	resource.seq++
	sequence := resource.seq
	state := &turnState{retain: continuation}
	state.valid.Store(true)
	result := make(chan futureResult[T], 1)
	resource.stages <- &stage{
		sequence: sequence,
		target:   continuation.target,
		state:    state,
		execute: func(turn Turn) bool {
			if err := continuation.validateTarget(); err != nil {
				state.valid.Store(false)
				result <- futureResult[T]{err: err}
				continuation.stageFinished(false)
				return true
			}
			value, applyErr := apply(turn)
			state.valid.Store(false)
			result <- futureResult[T]{value: value, err: applyErr}
			continuation.stageFinished(false)
			return true
		},
	}
	resource.mu.Unlock()
	continuation.mu.Unlock()
	return Future[T]{done: result}, nil
}

func (c *Continuation) validateTarget() error {
	if c == nil || c.resource == nil {
		return ErrTurnExpired
	}
	c.mu.Lock()
	target := c.target
	authority := c.resource.scopeAuthority
	c.mu.Unlock()
	if target.kind != targetAgent {
		return nil
	}
	if authority == nil {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	scope, ok := authority.CurrentExecutionScope(target.scopeID)
	if !ok {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	current, err := AgentTarget(scope)
	if err != nil {
		return err
	}
	if !target.same(current) {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	return nil
}

func EnqueueTerminal[T any](
	ctx context.Context,
	continuation *Continuation,
	apply func(Turn) (T, error),
) (Future[T], error) {
	if continuation == nil {
		return Future[T]{}, errors.New("runtime command continuation is required")
	}
	if apply == nil {
		return Future[T]{}, ErrCommandHandlerNeeded
	}
	select {
	case <-continuation.slot:
	case <-continuation.done:
		return Future[T]{}, ErrTurnExpired
	case <-continuation.resource.closedCh:
		return Future[T]{}, ErrResourceUnavailable
	case <-ctx.Done():
		return Future[T]{}, context.Cause(ctx)
	}

	continuation.mu.Lock()
	if continuation.released {
		continuation.mu.Unlock()
		continuation.slot <- struct{}{}
		return Future[T]{}, ErrTurnExpired
	}
	continuation.pending = true
	resource := continuation.resource
	resource.mu.Lock()
	if resource.closed {
		resource.mu.Unlock()
		continuation.pending = false
		continuation.mu.Unlock()
		continuation.slot <- struct{}{}
		return Future[T]{}, ErrResourceUnavailable
	}
	resource.seq++
	sequence := resource.seq
	state := &turnState{retain: continuation}
	state.valid.Store(true)
	result := make(chan futureResult[T], 1)
	resource.stages <- &stage{
		sequence: sequence,
		target:   continuation.target,
		state:    state,
		execute: func(turn Turn) bool {
			if err := continuation.validateTarget(); err != nil {
				state.valid.Store(false)
				result <- futureResult[T]{err: err}
				continuation.stageFinished(true)
				return false
			}
			value, applyErr := apply(turn)
			state.valid.Store(false)
			result <- futureResult[T]{value: value, err: applyErr}
			continuation.stageFinished(true)
			return false
		},
	}
	resource.mu.Unlock()
	continuation.mu.Unlock()
	return Future[T]{done: result}, nil
}

func (c *Continuation) Release() error {
	if c == nil {
		return errors.New("runtime command continuation is required")
	}
	c.mu.Lock()
	if c.released {
		c.mu.Unlock()
		return ErrTurnExpired
	}
	if c.pending {
		c.mu.Unlock()
		return errors.New("runtime command continuation has a pending stage")
	}
	c.released = true
	close(c.done)
	c.mu.Unlock()
	c.resource.releasePermit()
	return nil
}

func (c *Continuation) OrderedMutation(
	ctx context.Context,
	apply func(runtimepkg.OrderedMutationTurn) error,
) error {
	if c == nil {
		return ErrTurnExpired
	}
	future, err := Reenter(ctx, c, func(turn Turn) (struct{}, error) {
		if apply == nil {
			return struct{}{}, ErrCommandHandlerNeeded
		}
		return struct{}{}, apply(turn)
	})
	if err != nil {
		return err
	}
	_, err = future.Await(ctx)
	return err
}

func (c *Continuation) stageFinished(terminal bool) {
	c.mu.Lock()
	if !c.released {
		c.pending = false
		if terminal {
			c.released = true
			close(c.done)
		} else {
			c.slot <- struct{}{}
		}
	}
	c.mu.Unlock()
}
