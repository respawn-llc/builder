package runtimecommand

import (
	"context"
	"errors"
	"sync"

	runtimepkg "core/server/runtime"
	"core/server/sessionruntime"
)

type OrderedMutationTurn = runtimepkg.OrderedMutationTurn

type executionLease struct {
	resource     *resourceQueue
	gate         *StartGate
	continuation *Continuation

	mu       sync.Mutex
	released bool
}

func newExecutionLease(resource *resourceQueue) *executionLease {
	if resource == nil {
		return &executionLease{gate: NewStartGate()}
	}
	return &executionLease{
		resource:     resource,
		gate:         NewStartGate(),
		continuation: newContinuation(resource, SessionTarget(resource.ref)),
	}
}

func newExecutionLeaseFromContinuation(resource *resourceQueue, continuation *Continuation) *executionLease {
	return &executionLease{
		resource:     resource,
		gate:         NewStartGate(),
		continuation: continuation,
	}
}

func (l *executionLease) BindAgentScope(scope sessionruntime.ExecutionScope) error {
	if l == nil || l.continuation == nil {
		return ErrTurnExpired
	}
	target, err := AgentTarget(scope)
	if err != nil {
		return err
	}
	return l.continuation.bindTarget(target)
}

var _ sessionruntime.ResourceExecutionLease = (*executionLease)(nil)

func (l *executionLease) Wait(ctx context.Context) error {
	if l == nil {
		return ErrTurnExpired
	}
	return l.gate.Wait(ctx)
}

func (l *executionLease) Commit() error {
	if l == nil {
		return ErrTurnExpired
	}
	return l.gate.Commit()
}

func (l *executionLease) Abort(cause error) error {
	if l == nil {
		return ErrTurnExpired
	}
	return l.gate.Abort(cause)
}

func (l *executionLease) Release() error {
	if l == nil {
		return errors.New("runtime command execution lease is required")
	}
	l.mu.Lock()
	if l.released {
		l.mu.Unlock()
		return ErrTurnExpired
	}
	l.released = true
	l.mu.Unlock()
	if l.continuation == nil {
		reopenErr := error(nil)
		if l.resource != nil {
			reopenErr = l.resource.completionFence.Reopen()
		}
		l.resource.releasePermit()
		return reopenErr
	}
	releaseErr := l.continuation.Release()
	if l.resource != nil {
		releaseErr = errors.Join(releaseErr, l.resource.completionFence.Reopen())
	}
	return releaseErr
}

func (l *executionLease) OrderedMutation(ctx context.Context, apply func(runtimepkg.OrderedMutationTurn) error) error {
	if l == nil || l.continuation == nil {
		return ErrTurnExpired
	}
	return l.continuation.OrderedMutation(ctx, apply)
}
