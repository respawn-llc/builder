package workflowexecution

import (
	"errors"
	"fmt"

	"core/server/workflow"
	"core/shared/runtimeids"
)

type LifecycleFatalOperation string

const (
	LifecycleFatalOperationReadyPreparationFailure LifecycleFatalOperation = "ready_preparation_failure"
	LifecycleFatalOperationAdmittedLaunchFailure   LifecycleFatalOperation = "admitted_launch_failure"
	LifecycleFatalOperationExactRuntimeFailure     LifecycleFatalOperation = "exact_runtime_failure"
	LifecycleFatalOperationOutcomeLessFinalization LifecycleFatalOperation = "outcome_less_finalization"
	LifecycleFatalOperationSuccessorDisposition    LifecycleFatalOperation = "successor_disposition"
	LifecycleFatalOperationInterruptCleanup        LifecycleFatalOperation = "interrupt_cleanup"
	LifecycleFatalOperationControllerClose         LifecycleFatalOperation = "controller_close"
)

type LifecycleFatalRunPhase string

const (
	LifecycleFatalRunPhaseStaged    LifecycleFatalRunPhase = "staged"
	LifecycleFatalRunPhaseHeld      LifecycleFatalRunPhase = "held"
	LifecycleFatalRunPhaseQueued    LifecycleFatalRunPhase = "queued"
	LifecycleFatalRunPhaseLaunching LifecycleFatalRunPhase = "launching"
	LifecycleFatalRunPhaseExact     LifecycleFatalRunPhase = "exact"
	LifecycleFatalRunPhaseRetiring  LifecycleFatalRunPhase = "retiring"
)

type LifecycleFatalDiagnostic struct {
	Operation          LifecycleFatalOperation
	TaskID             workflow.TaskID
	CurrentNode        workflow.CurrentNodeReference
	RunID              uint64
	RunPhase           LifecycleFatalRunPhase
	ExpectedScheduling workflow.CurrentNodeSchedulingState
	ScopeID            *runtimeids.ExecutionScopeID
	OriginalOutcome    error
	PersistenceFailure error
	CleanupFailure     error
}

func (d LifecycleFatalDiagnostic) Error() string {
	scope := "absent"
	if d.ScopeID != nil {
		scope = d.ScopeID.String()
	}
	return fmt.Sprintf(
		"fatal workflow lifecycle failure: operation=%s task_id=%s current_node=%v run_id=%d run_phase=%s expected_scheduling=%s exact_scope=%s original_outcome=%v persistence_failure=%v cleanup_failure=%v",
		d.Operation,
		d.TaskID,
		d.CurrentNode,
		d.RunID,
		d.RunPhase,
		d.ExpectedScheduling,
		scope,
		d.OriginalOutcome,
		d.PersistenceFailure,
		d.CleanupFailure,
	)
}

func (d LifecycleFatalDiagnostic) Unwrap() error {
	return errors.Join(d.OriginalOutcome, d.PersistenceFailure, d.CleanupFailure)
}

type LifecycleFatalReportResult struct {
	ShutdownAccepted bool
}

type LifecycleFatalReporter interface {
	ReportFatal(LifecycleFatalDiagnostic) LifecycleFatalReportResult
	Available() error
}

type LifecycleUnavailableError struct {
	Cause error
}

func (e LifecycleUnavailableError) Error() string {
	return fmt.Sprintf("workflow execution lifecycle is unavailable: %v", e.Cause)
}

func (e LifecycleUnavailableError) Unwrap() error {
	return e.Cause
}

func (c *CurrentNodeController) recordLifecycleFatalLocked(
	run *currentNodeRun,
	operation LifecycleFatalOperation,
	originalOutcome error,
	persistenceFailure error,
) {
	if run == nil || persistenceFailure == nil {
		return
	}
	diagnostic := lifecycleFatalDiagnosticForRun(run, operation, originalOutcome, persistenceFailure)
	result := c.lifecycleFatalReporter.ReportFatal(diagnostic)
	run.recordCallbackError(fmt.Errorf(
		"%s shutdown_accepted=%t: %w",
		diagnostic.Error(),
		result.ShutdownAccepted,
		persistenceFailure,
	))
}

func lifecycleFatalDiagnosticForRun(
	run *currentNodeRun,
	operation LifecycleFatalOperation,
	originalOutcome error,
	persistenceFailure error,
) LifecycleFatalDiagnostic {
	var scopeID *runtimeids.ExecutionScopeID
	if run.exactScopeID != nil {
		scope := *run.exactScopeID
		scopeID = &scope
	}
	return LifecycleFatalDiagnostic{
		Operation:          operation,
		TaskID:             run.reference.TaskID,
		CurrentNode:        run.reference,
		RunID:              run.id.sequence,
		RunPhase:           lifecycleFatalRunPhase(run.phase),
		ExpectedScheduling: run.expectedScheduling,
		ScopeID:            scopeID,
		OriginalOutcome:    originalOutcome,
		PersistenceFailure: persistenceFailure,
	}
}

func lifecycleFatalRunPhase(phase currentNodeRunPhase) LifecycleFatalRunPhase {
	switch phase {
	case currentNodeRunStaged:
		return LifecycleFatalRunPhaseStaged
	case currentNodeRunHeld:
		return LifecycleFatalRunPhaseHeld
	case currentNodeRunQueued:
		return LifecycleFatalRunPhaseQueued
	case currentNodeRunLaunching:
		return LifecycleFatalRunPhaseLaunching
	case currentNodeRunExact:
		return LifecycleFatalRunPhaseExact
	case currentNodeRunRetiring:
		return LifecycleFatalRunPhaseRetiring
	default:
		panic(fmt.Sprintf("unknown current node Run phase %d", phase))
	}
}

func lifecycleFatalOperationForInterruptionReason(reason workflow.CurrentNodeInterruptionReason) LifecycleFatalOperation {
	switch reason {
	case reasonCurrentNodeRuntimeFinalizedWithoutOutcome:
		return LifecycleFatalOperationOutcomeLessFinalization
	default:
		return LifecycleFatalOperationExactRuntimeFailure
	}
}
