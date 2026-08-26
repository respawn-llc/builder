package workflowsvc

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/worktree"
	"core/shared/workflowcontract"
	"core/shared/worktreecontract"
)

type taskSetupObservation struct {
	setupOperationID worktreecontract.SetupOperationID
	executionTarget  workflowcontract.ExecutionTargetSelection
	publisher        workflowTaskSetupEventPublisher

	mu             sync.Mutex
	prepared       preparedInitiatingActionTarget
	preparationErr error
	finalized      bool
}

func newTaskSetupObservation(setupOperationID worktreecontract.SetupOperationID, executionTarget workflowcontract.ExecutionTargetSelection, publisher workflowTaskSetupEventPublisher) (*taskSetupObservation, error) {
	if err := setupOperationID.Validate(); err != nil {
		return nil, err
	}
	if publisher == nil {
		return nil, errors.New("Workflow Task setup event publisher is required")
	}
	if err := executionTarget.Validate(); err != nil {
		return nil, err
	}
	return &taskSetupObservation{
		setupOperationID: setupOperationID,
		executionTarget:  executionTarget,
		publisher:        publisher,
	}, nil
}

func (o *taskSetupObservation) record(prepared preparedInitiatingActionTarget, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.prepared = prepared
	o.preparationErr = err
}

func (o *taskSetupObservation) finalize(finalization workflowexecution.TaskPreparationFinalization) {
	o.mu.Lock()
	if o.finalized {
		o.mu.Unlock()
		panic("Workflow Task setup observation was finalized more than once")
	}
	o.finalized = true
	prepared := o.prepared
	preparationErr := o.preparationErr
	o.mu.Unlock()

	event := worktreecontract.SetupEvent{SetupOperationID: o.setupOperationID}
	switch finalization.Kind {
	case workflowexecution.TaskPreparationHandedOff:
		switch {
		case prepared.setupResult == nil:
			event.Phase = worktreecontract.SetupPhaseNotRequired
			event.NotRequired = &worktreecontract.SetupNotRequired{
				Reason:                   worktreecontract.SetupNotRequiredNoTargetPreparation,
				RetainedPreviousWorktree: prepared.retainedPreviousWorktree,
			}
		case prepared.setupResult.Completed != nil:
			completed := *prepared.setupResult.Completed
			if prepared.retainedPreviousWorktree != nil {
				completed.RetainedPreviousWorktree = prepared.retainedPreviousWorktree
			}
			event.Phase = worktreecontract.SetupPhaseCompleted
			event.Completed = &completed
		case prepared.setupResult.NotRequired != nil:
			notRequired := *prepared.setupResult.NotRequired
			if prepared.retainedPreviousWorktree != nil {
				notRequired.RetainedPreviousWorktree = prepared.retainedPreviousWorktree
			}
			event.Phase = worktreecontract.SetupPhaseNotRequired
			event.NotRequired = &notRequired
		default:
			panic("successful Task preparation has a failed or invalid setup result")
		}
	case workflowexecution.TaskPreparationFailed:
		event.Phase = worktreecontract.SetupPhaseFailed
		event.Failed = preparationFailurePayload(prepared.setupResult, prepared.retainedWorktree, prepared.retainedPreviousWorktree, errors.Join(preparationErr, finalization.Cause))
		event.Failed.ExecutionTarget = &o.executionTarget
	case workflowexecution.TaskPreparationInterruptionFailed:
		event.Phase = worktreecontract.SetupPhaseFailed
		event.Failed = interruptionPersistenceFailurePayload(
			prepared.setupResult,
			prepared.retainedWorktree,
			prepared.retainedPreviousWorktree,
			preparationErr,
			finalization.Cause,
		)
	case workflowexecution.TaskPreparationCanceled:
		event.Phase = worktreecontract.SetupPhaseFailed
		event.Failed = nonRetryablePreparationFailure(
			worktreecontract.SetupFailureCanceled,
			finalization.Cause,
		)
	case workflowexecution.TaskPreparationControllerShutDown:
		event.Phase = worktreecontract.SetupPhaseFailed
		event.Failed = nonRetryablePreparationFailure(
			worktreecontract.SetupFailureControllerShutdown,
			finalization.Cause,
		)
	default:
		panic(fmt.Sprintf("unknown Task preparation finalization kind %q", finalization.Kind))
	}
	o.publisher.PublishWorkflowTaskSetupEvent(event)
}

func preparationFailurePayload(result *worktree.WorktreeSetupResult, retainedWorktree *worktreecontract.TopologyEntry, retainedPreviousWorktree *worktreecontract.RetainedPreviousWorktree, cause error) *worktreecontract.SetupFailed {
	if result != nil && result.Failed != nil {
		failed := *result.Failed
		if failed.RetainedWorktree == nil && retainedWorktree != nil {
			failed.RetainedWorktree = retainedWorktree
		}
		if retainedPreviousWorktree != nil {
			failed.RetainedPreviousWorktree = retainedPreviousWorktree
		}
		return &failed
	}
	return &worktreecontract.SetupFailed{
		RetryReadiness: worktreecontract.SetupRetryReady,
		Cause: worktreecontract.SetupFailureCause{
			Kind:        worktreecontract.SetupFailureTargetPreparation,
			Preparation: &worktreecontract.SetupPreparationFailure{},
		},
		Diagnostic:               preparationDiagnostic(cause),
		RetainedWorktree:         retainedWorktree,
		RetainedPreviousWorktree: retainedPreviousWorktree,
	}
}

func nonRetryablePreparationFailure(kind worktreecontract.SetupFailureKind, cause error) *worktreecontract.SetupFailed {
	failureCause := worktreecontract.SetupFailureCause{Kind: kind}
	switch kind {
	case worktreecontract.SetupFailureCanceled:
		failureCause.Canceled = &worktreecontract.SetupCanceled{}
	case worktreecontract.SetupFailureControllerShutdown:
		failureCause.ControllerShutdown = &worktreecontract.SetupControllerShutdown{}
	default:
		panic(fmt.Sprintf("unsupported non-retryable Task preparation failure kind %q", kind))
	}
	return &worktreecontract.SetupFailed{
		RetryReadiness: worktreecontract.SetupNonRetryable,
		Cause:          failureCause,
		Diagnostic:     preparationDiagnostic(cause),
	}
}

func interruptionPersistenceFailurePayload(result *worktree.WorktreeSetupResult, retainedWorktree *worktreecontract.TopologyEntry, retainedPreviousWorktree *worktreecontract.RetainedPreviousWorktree, preparationErr error, persistenceErr error) *worktreecontract.SetupFailed {
	failed := preparationFailurePayload(
		result,
		retainedWorktree,
		retainedPreviousWorktree,
		errors.Join(preparationErr, persistenceErr),
	)
	failed.RetryReadiness = worktreecontract.SetupNonRetryable
	failed.Cause = worktreecontract.SetupFailureCause{
		Kind:                    worktreecontract.SetupFailureInterruptionPersistence,
		InterruptionPersistence: &worktreecontract.SetupInterruptionPersistenceFailure{},
	}
	failed.Diagnostic = preparationDiagnostic(errors.Join(preparationErr, persistenceErr))
	return failed
}

func preparationDiagnostic(err error) string {
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		return "Workflow Task preparation failed"
	}
	return err.Error()
}

func taskPreparationError(
	setupOperationID worktreecontract.SetupOperationID,
	preflight initiatingActionTargetPreflight,
	result *worktree.WorktreeSetupResult,
	retainedWorktree *worktreecontract.TopologyEntry,
	retainedPreviousWorktree *worktreecontract.RetainedPreviousWorktree,
	err error,
) error {
	err = configuredTargetPreparationError(preflight, err)
	if err == nil {
		return nil
	}
	var typed *workflowexecution.TaskStartPreparationError
	detail := workflow.CurrentNodeInterruptionDetail{
		Code: string(reasonWorkflowTaskSetupFailed),
	}
	if errors.As(err, &typed) {
		detail = typed.InterruptionDetail()
		if detail.SetupRecovery != nil {
			return err
		}
	}
	failed := preparationFailurePayload(result, retainedWorktree, retainedPreviousWorktree, err)
	if failed.RetryReadiness != worktreecontract.SetupRetryReady {
		return err
	}
	var retained *workflow.CurrentNodeRetainedWorktree
	var retainedErr error
	if failed.RetainedWorktree != nil {
		retained, retainedErr = retainedCurrentNodeWorktree(*failed.RetainedWorktree)
		if retainedErr != nil {
			return errors.Join(err, retainedErr)
		}
	}
	var previous *workflow.CurrentNodeRetainedWorktree
	if failed.RetainedPreviousWorktree != nil {
		previous, retainedErr = retainedCurrentNodeWorktree(failed.RetainedPreviousWorktree.Worktree)
		if retainedErr != nil {
			return errors.Join(err, retainedErr)
		}
	}
	delete(detail.Fields, workflow.CurrentNodeInterruptionDiagnosticField)
	detail.SetupRecovery = &workflow.CurrentNodeSetupRecoveryDetail{
		SetupOperationID:         setupOperationID,
		Cause:                    workflow.CurrentNodeSetupRecoveryCause(failed.Cause.Kind),
		Diagnostic:               failed.Diagnostic,
		ScriptPath:               failed.ScriptPath,
		SetupRequirement:         setupRequirementForPreparationFailure(result),
		ExecutionTarget:          preflight.selection,
		RetainedWorktree:         retained,
		RetainedPreviousWorktree: previous,
	}
	if validationErr := detail.Validate(); validationErr != nil {
		return errors.Join(err, validationErr)
	}
	return workflowexecution.NewTaskStartPreparationError(err, detail)
}

func setupRequirementForPreparationFailure(result *worktree.WorktreeSetupResult) worktreecontract.SetupRequirement {
	if result != nil && result.Completed != nil {
		return worktreecontract.SetupRequirementAlreadyCompleted
	}
	return worktreecontract.SetupRequirementRequired
}

const reasonWorkflowTaskSetupFailed workflow.CurrentNodeInterruptionReason = "workflow_task_setup_failed"

func retainedCurrentNodeWorktree(entry worktreecontract.TopologyEntry) (*workflow.CurrentNodeRetainedWorktree, error) {
	if entry.Variant != worktreecontract.TopologyVariantRegistered || entry.Registered == nil {
		return nil, errors.New("setup recovery retained worktree must be registered")
	}
	retained := &workflow.CurrentNodeRetainedWorktree{
		WorktreeID: entry.Registered.Kent.WorktreeID,
		Root:       entry.Registered.Git.CanonicalRoot,
	}
	return retained, retained.Validate()
}
