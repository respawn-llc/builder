package sessionruntime

import (
	"context"

	"core/server/runtimeops"
	"core/shared/clientui"
	"core/shared/runtimeids"
)

type WorkflowSessionInterruptOutcome uint8

const (
	WorkflowSessionInterruptUnhandled WorkflowSessionInterruptOutcome = iota
	WorkflowSessionInterruptNotRetained
	WorkflowSessionInterruptNoLongerLive
	WorkflowSessionInterruptCommitted
)

type WorkflowCommittedInterruptCleanup func(func(context.Context) error) error

type WorkflowSessionInterruptRequest struct {
	SessionID          runtimeids.SessionID
	TargetOperationRef *clientui.RuntimeOperationRef
	Target             runtimeops.CancellationTarget
}

// WorkflowSessionInterruptor classifies direct retained ownership and transfers
// cleanup only after durable Workflow interruption commits.
type WorkflowSessionInterruptor interface {
	InterruptWorkflowSession(
		context.Context,
		WorkflowSessionInterruptRequest,
		func(WorkflowCommittedInterruptCleanup) error,
	) (WorkflowSessionInterruptOutcome, error)
}
