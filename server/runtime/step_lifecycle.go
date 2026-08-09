package runtime

import (
	"context"
	"time"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type StepLifecycleTransition string

const (
	StepLifecycleTransitionBegan StepLifecycleTransition = "began"
	StepLifecycleTransitionEnded StepLifecycleTransition = "ended"
)

type StepLifecycleSnapshot struct {
	SessionID   string
	RunID       string
	StepID      string
	ActiveKind  ActiveKind
	Transition  StepLifecycleTransition
	Status      RunStatus
	StartedAt   time.Time
	FinishedAt  time.Time
	PublishedAt time.Time
}

type StepLifecycleSink interface {
	StepBegan(context.Context, StepLifecycleSnapshot) error
	StepEnded(context.Context, StepLifecycleSnapshot) error
}

type AgentStepOriginLifecycleSink interface {
	AgentStepBegan(context.Context, serverapi.RuntimeStepOrigin) (runtimeids.ExecutionScopeID, error)
	AgentStepBoundary(
		context.Context,
		serverapi.RuntimeStepOrigin,
	) (AgentStepBoundaryTransfer, error)
}

type RuntimeBoundHumanExecutionLauncher interface {
	RegisterRuntimeBoundHumanExecution(context.Context) (RuntimeBoundHumanExecution, error)
}

type RuntimeBoundHumanExecution interface {
	Launch(context.Context) error
}

type RuntimeBoundLongExecutionLauncher interface {
	RegisterRuntimeBoundLongExecution(context.Context) (RuntimeBoundLongExecution, error)
}

type RuntimeBoundLongExecution interface {
	Launch(
		context.Context,
		func(context.Context, *Engine) error,
	) (runtimeids.ExecutionScopeID, error)
	Cancel(context.Context) error
}

type AgentStepScopeLifecycle interface {
	AgentStepScopeLive(context.Context, runtimeids.ExecutionScopeID) bool
	CurrentAgentExecutionScope(context.Context) (runtimeids.ExecutionScopeID, bool)
}

type IdleBoundaryReducerLifecycle interface {
	TryAcquireIdleBoundary(context.Context) (IdleBoundaryReducerGrant, bool, error)
}

type IdleBoundaryReducerGrant interface {
	Release() (bool, error)
}

type AgentStepReducerGrant interface {
	RegisterNext(context.Context, serverapi.RuntimeStepOrigin) (runtimeids.ExecutionScopeID, error)
	Release() error
}

type AgentStepWorktreeWait interface {
	Await(context.Context) (AgentStepReducerGrant, error)
}

type AgentStepBoundaryTransfer interface {
	agentStepBoundaryTransfer()
}

type AgentStepReducerBoundary struct {
	Grant AgentStepReducerGrant
}

func (AgentStepReducerBoundary) agentStepBoundaryTransfer() {}

type AgentStepWorktreeBoundary struct {
	Wait AgentStepWorktreeWait
}

func (AgentStepWorktreeBoundary) agentStepBoundaryTransfer() {}
