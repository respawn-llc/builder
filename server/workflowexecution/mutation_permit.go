package workflowexecution

import (
	"context"
	"errors"
)

type mutationPermitContextKey struct{}

// MutationPermit serializes process-wide Workflow mutations such as startup
// recovery and destructive cross-Task operations. Ordinary Task execution is
// owned by TaskLifecycleCoordinator. The permit remains context-aware for
// nested process-wide operations.
type MutationPermit struct {
	token chan struct{}
}

func NewMutationPermit() *MutationPermit {
	permit := &MutationPermit{token: make(chan struct{}, 1)}
	permit.token <- struct{}{}
	return permit
}

func (p *MutationPermit) Run(ctx context.Context, operation func(context.Context) error) error {
	if p == nil {
		return errors.New("workflow mutation permit is required")
	}
	if ctx == nil {
		return errors.New("workflow mutation context is required")
	}
	if operation == nil {
		return errors.New("workflow mutation operation is required")
	}
	if active, ok := ctx.Value(mutationPermitContextKey{}).(*MutationPermit); ok && active == p {
		return operation(ctx)
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-p.token:
	}
	defer func() { p.token <- struct{}{} }()
	return operation(context.WithValue(ctx, mutationPermitContextKey{}, p))
}

func RunMutation[T any](ctx context.Context, permit *MutationPermit, operation func(context.Context) (T, error)) (T, error) {
	var result T
	if operation == nil {
		return result, errors.New("workflow mutation operation is required")
	}
	err := permit.Run(ctx, func(ctx context.Context) error {
		var err error
		result, err = operation(ctx)
		return err
	})
	return result, err
}
