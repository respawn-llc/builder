package workflowexecution

import (
	"context"
	"errors"

	"core/server/workflow"
	"core/server/workflowstore"
)

var (
	ErrSchedulerStopped            = errors.New("workflow scheduler stopped")
	ErrSchedulerClaimFailed        = errors.New("workflow scheduler claim failed")
	ErrSchedulerRuntimeStartFailed = errors.New("workflow runtime start failed")
)

type SchedulerStore interface {
	GetRun(context.Context, workflow.RunID) (workflowstore.RunRecord, error)
	ClaimRun(context.Context, workflow.RunID, int64) (workflowstore.RunnableRunRecord, error)
	InterruptRun(context.Context, workflow.RunID, string, string) error
	InterruptRunGeneration(context.Context, workflow.RunID, int64, string, string) error
	ReconcileStartedRuns(context.Context, string) ([]workflowstore.RunRecord, error)
	ReconcileUnstartedRuns(context.Context, string) ([]workflowstore.RunRecord, error)
	ListWaitingAskRuns(context.Context) ([]workflowstore.RunRecord, error)
}

type SchedulerRuntimeStarter interface {
	StartWorkflowRun(context.Context, SchedulerStartRunRequest) error
}

type SchedulerPendingAskResolver interface {
	CanRehydrate(context.Context, string, workflow.RunID, string) (bool, error)
}

type SchedulerLogger interface {
	Logf(string, ...any)
}

type SchedulerInterruptedRunFinalizer interface {
	PublishPendingInterruptedRun(context.Context, workflow.RunID)
}

type SchedulerStartRunRequest struct {
	RunID       workflow.RunID
	TaskID      workflow.TaskID
	PlacementID workflow.PlacementID
	NodeID      workflow.NodeID
	Generation  int64
}

type SchedulerConfig struct {
	Concurrency int
}
