package runtime

import (
	"context"
	"time"
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
