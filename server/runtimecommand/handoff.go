package runtimecommand

import (
	"context"
	"errors"
)

type HandoffOwner interface {
	Join(context.Context) error
}

type HandoffOwnerRegistration func(*StartGate) (HandoffOwner, error)

type handoffContinuationResult struct {
	continuation *Continuation
	err          error
}

type Handoff[T any] struct {
	Result       Future[T]
	continuation <-chan handoffContinuationResult
}

func (h Handoff[T]) Await(ctx context.Context) (T, *Continuation, error) {
	value, err := h.Result.Await(ctx)
	if err != nil {
		var zero T
		return zero, nil, err
	}
	select {
	case continuation := <-h.continuation:
		return value, continuation.continuation, continuation.err
	case <-ctx.Done():
		var zero T
		return zero, nil, context.Cause(ctx)
	}
}

func BeginHandoff[T any](
	ctx context.Context,
	authority *Authority,
	target Target,
	register HandoffOwnerRegistration,
	apply func(Turn) (T, error),
) (Handoff[T], error) {
	if register == nil {
		return Handoff[T]{}, errors.New("runtime command handoff owner registration is required")
	}
	if apply == nil {
		return Handoff[T]{}, ErrCommandHandlerNeeded
	}
	if err := context.Cause(ctx); err != nil {
		return Handoff[T]{}, err
	}
	gate := NewStartGate()
	owner, err := register(gate)
	if err != nil {
		return Handoff[T]{}, err
	}
	if owner == nil {
		return Handoff[T]{}, errors.New("runtime command handoff owner is required")
	}

	continuationResult := make(chan handoffContinuationResult, 1)
	future, err := Enqueue(ctx, authority, target, func(turn Turn) (T, error) {
		continuation, retainErr := turn.Retain()
		if retainErr != nil {
			_ = gate.Abort(retainErr)
			joinErr := owner.Join(context.Background())
			return zeroValue[T](), errors.Join(retainErr, joinErr)
		}
		turn.queue.lifecycleMu.Lock()
		defer turn.queue.lifecycleMu.Unlock()
		turn.queue.mu.Lock()
		closed := turn.queue.closed
		turn.queue.mu.Unlock()
		if closed {
			_ = gate.Abort(ErrResourceUnavailable)
			joinErr := owner.Join(context.Background())
			_ = continuation.Release()
			err := errors.Join(ErrResourceUnavailable, joinErr)
			continuationResult <- handoffContinuationResult{err: err}
			return zeroValue[T](), err
		}
		value, applyErr := apply(turn)
		if applyErr != nil {
			_ = gate.Abort(applyErr)
			joinErr := owner.Join(context.Background())
			_ = continuation.Release()
			continuationResult <- handoffContinuationResult{err: errors.Join(applyErr, joinErr)}
			return value, errors.Join(applyErr, joinErr)
		}
		if commitErr := gate.Commit(); commitErr != nil {
			_ = gate.Abort(commitErr)
			joinErr := owner.Join(context.Background())
			_ = continuation.Release()
			continuationResult <- handoffContinuationResult{err: errors.Join(commitErr, joinErr)}
			return value, errors.Join(commitErr, joinErr)
		}
		continuationResult <- handoffContinuationResult{continuation: continuation}
		return value, nil
	})
	if err != nil {
		_ = gate.Abort(err)
		joinErr := owner.Join(context.Background())
		return Handoff[T]{}, errors.Join(err, joinErr)
	}
	return Handoff[T]{Result: future, continuation: continuationResult}, nil
}

func zeroValue[T any]() T {
	var zero T
	return zero
}
