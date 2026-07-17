package tools

import (
	"context"
	"errors"
	"fmt"

	"core/shared/runtimeids"
)

type ExecutionIdentity struct {
	RunID  string
	StepID string
}

type executionIdentityContextKey struct{}

func WithExecutionIdentity(ctx context.Context, identity ExecutionIdentity) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, executionIdentityContextKey{}, identity)
}

func ExecutionIdentityFromContext(ctx context.Context) (ExecutionIdentity, error) {
	if ctx == nil {
		return ExecutionIdentity{}, errors.New("tool execution identity is required")
	}
	identity, ok := ctx.Value(executionIdentityContextKey{}).(ExecutionIdentity)
	if !ok {
		return ExecutionIdentity{}, errors.New("tool execution identity is required")
	}
	if err := runtimeids.ValidateUUIDv4(identity.RunID, "run_id"); err != nil {
		return ExecutionIdentity{}, fmt.Errorf("tool execution identity: %w", err)
	}
	if err := runtimeids.ValidateUUIDv4(identity.StepID, "step_id"); err != nil {
		return ExecutionIdentity{}, fmt.Errorf("tool execution identity: %w", err)
	}
	return identity, nil
}
