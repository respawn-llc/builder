package workflowrunner

import "core/server/workflowexecution"

type SchedulerStore = workflowexecution.SchedulerStore
type SchedulerRuntimeStarter = workflowexecution.SchedulerRuntimeStarter
type SchedulerStartRunRequest = workflowexecution.SchedulerStartRunRequest
type SchedulerPrepareRunRequest = workflowexecution.SchedulerPrepareRunRequest
type PreparedWorkflowRun = workflowexecution.PreparedWorkflowRun
type RunAdmission = workflowexecution.RunAdmission
type SchedulerConfig = workflowexecution.SchedulerConfig
type SchedulerOption = workflowexecution.SchedulerOption
type SchedulerService = workflowexecution.SchedulerService

const (
	ReasonSchedulerRuntimeStartFailed    = workflowexecution.ReasonSchedulerRuntimeStartFailed
	ReasonSchedulerPendingAskUnavailable = workflowexecution.ReasonSchedulerPendingAskUnavailable
	ReasonSchedulerStartupOrphanedRun    = workflowexecution.ReasonSchedulerStartupOrphanedRun
	ReasonSchedulerStartupUnstartedRun   = workflowexecution.ReasonSchedulerStartupUnstartedRun
)

var (
	ErrSchedulerStopped             = workflowexecution.ErrSchedulerStopped
	ErrSchedulerClaimFailed         = workflowexecution.ErrSchedulerClaimFailed
	ErrSchedulerRuntimeStartFailed  = workflowexecution.ErrSchedulerRuntimeStartFailed
	NewSchedulerService             = workflowexecution.NewSchedulerService
	WithAutomaticIntents            = workflowexecution.WithAutomaticIntents
	WithSchedulerPendingAskResolver = workflowexecution.WithSchedulerPendingAskResolver
	WithSchedulerAttentionFinalizer = workflowexecution.WithSchedulerAttentionFinalizer
	WithSchedulerProcessInterval    = workflowexecution.WithSchedulerProcessInterval
)
