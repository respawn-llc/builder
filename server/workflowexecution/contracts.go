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
	ErrNoInterruptibleExecution    = errors.New("task has no actively executing workflow scope to interrupt")
	ErrTaskExecutionNotQuiescent   = errors.New("workflow task execution is not quiescent")
)

type SchedulerStore interface {
	GetRun(context.Context, workflow.RunID) (workflowstore.RunRecord, error)
	AdmitRun(context.Context, workflowstore.RunAdmission) (workflowstore.RunnableRunRecord, error)
	InterruptRun(context.Context, workflow.RunID, string, string) error
	InterruptRunGeneration(context.Context, workflow.RunID, int64, string, string) error
	ReconcileStartedRuns(context.Context, string) ([]workflowstore.RunRecord, error)
	ReconcileUnstartedRuns(context.Context, string) ([]workflowstore.RunRecord, error)
	ListWaitingAskRuns(context.Context) ([]workflowstore.RunRecord, error)
}

type SchedulerRuntimeStarter interface {
	PrepareWorkflowRun(context.Context, SchedulerPrepareRunRequest) (PreparedWorkflowRun, error)
}

type PreparedWorkflowRun interface {
	Admission() RunAdmission
	Commit() error
	Activate()
	Abort(context.Context) error
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

type SchedulerPrepareRunRequest struct {
	RunID            workflow.RunID
	TaskID           workflow.TaskID
	PlacementID      workflow.PlacementID
	NodeID           workflow.NodeID
	SourceGeneration int64
	Generation       int64
}

type RunAdmission struct {
	SessionID               *string
	EffectiveCompletionMode *string
}

type SchedulerConfig struct {
	Concurrency int
}
