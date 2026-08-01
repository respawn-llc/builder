package runtimecommand

import (
	"context"
	"errors"
	"sync"
)

var ErrMilestoneCompleted = errors.New("runtime command milestone is already completed")

type SubmittedTurnResult[T any] struct {
	done   chan struct{}
	result futureResult[T]
	once   sync.Once
}

func NewSubmittedTurnResult[T any]() *SubmittedTurnResult[T] {
	return &SubmittedTurnResult[T]{done: make(chan struct{})}
}

func (m *SubmittedTurnResult[T]) Complete(value T, err error) error {
	if m == nil {
		return errors.New("runtime command submitted-turn result is required")
	}
	completed := false
	m.once.Do(func() {
		completed = true
		m.result = futureResult[T]{value: value, err: err}
		close(m.done)
	})
	if !completed {
		return ErrMilestoneCompleted
	}
	return nil
}

func (m *SubmittedTurnResult[T]) Await(ctx context.Context) (T, error) {
	if m == nil {
		var zero T
		return zero, errors.New("runtime command submitted-turn result is required")
	}
	select {
	case <-m.done:
		return m.result.value, m.result.err
	case <-ctx.Done():
		var zero T
		return zero, context.Cause(ctx)
	}
}

func EnqueueSubmittedTurnMilestone[T any](
	ctx context.Context,
	continuation *Continuation,
	milestone *SubmittedTurnResult[T],
	apply func(Turn) (T, error),
) (Future[struct{}], error) {
	if milestone == nil {
		return Future[struct{}]{}, errors.New("runtime command submitted-turn result is required")
	}
	if apply == nil {
		return Future[struct{}]{}, ErrCommandHandlerNeeded
	}
	return Reenter(ctx, continuation, func(turn Turn) (struct{}, error) {
		value, applyErr := apply(turn)
		completeErr := milestone.Complete(value, applyErr)
		return struct{}{}, errors.Join(applyErr, completeErr)
	})
}
