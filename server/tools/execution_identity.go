package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type ExecutionIdentity struct {
	RunID      string
	StepID     string
	ToolCallID clientui.ToolCallID
}

type executionIdentityContextKey struct{}
type approvalLifecycleContextKey struct{}

type ApprovalLifecycle struct {
	mu       sync.Mutex
	consumed bool
}

func NewApprovalLifecycle() *ApprovalLifecycle {
	return &ApprovalLifecycle{}
}

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
	if err := identity.ToolCallID.Validate(); err != nil {
		return ExecutionIdentity{}, fmt.Errorf("tool execution identity: %w", err)
	}
	return identity, nil
}

func WithApprovalLifecycle(ctx context.Context, lifecycle *ApprovalLifecycle) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, approvalLifecycleContextKey{}, lifecycle)
}

func approvalLifecycleFromContext(ctx context.Context) (*ApprovalLifecycle, error) {
	if ctx == nil {
		return nil, errors.New("tool Approval admission is required")
	}
	lifecycle, ok := ctx.Value(approvalLifecycleContextKey{}).(*ApprovalLifecycle)
	if !ok || lifecycle == nil {
		return nil, errors.New("tool Approval admission is required")
	}
	return lifecycle, nil
}

func ConsumeApprovalPresentation(ctx context.Context) error {
	lifecycle, err := approvalLifecycleFromContext(ctx)
	if err != nil {
		return err
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.consumed {
		return errors.New("tool call already presented its internal Approval")
	}
	lifecycle.consumed = true
	return nil
}
