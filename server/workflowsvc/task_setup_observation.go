package workflowsvc

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/worktree"
	"core/shared/serverapi"
	"github.com/google/uuid"
)

type taskSetupObservation struct {
	setupOperationID serverapi.WorktreeSetupOperationID
	publisher        workflowTaskSetupEventPublisher

	mu                       sync.Mutex
	setupResult              *worktree.WorktreeSetupResult
	retainedPreviousWorktree *serverapi.RetainedPreviousWorktree
	preparationErr           error
	finalized                bool
}

func newTaskSetupObservation(
	setupOperationID serverapi.WorktreeSetupOperationID,
	publisher workflowTaskSetupEventPublisher,
) (*taskSetupObservation, error) {
	if err := setupOperationID.Validate(); err != nil {
		return nil, err
	}
	if publisher == nil {
		return nil, errors.New("Workflow Task setup event publisher is required")
	}
	return &taskSetupObservation{
		setupOperationID: setupOperationID,
		publisher:        publisher,
	}, nil
}

func (o *taskSetupObservation) record(
	result *worktree.WorktreeSetupResult,
	retainedPreviousWorktree *serverapi.RetainedPreviousWorktree,
	err error,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.setupResult = result
	o.retainedPreviousWorktree = retainedPreviousWorktree
	o.preparationErr = err
}

func (o *taskSetupObservation) finalize(finalization workflowexecution.TaskPreparationFinalization) {
	o.mu.Lock()
	if o.finalized {
		o.mu.Unlock()
		panic("Workflow Task setup observation was finalized more than once")
	}
	o.finalized = true
	result := o.setupResult
	retainedPreviousWorktree := o.retainedPreviousWorktree
	preparationErr := o.preparationErr
	o.mu.Unlock()

	event := serverapi.WorktreeSetupEvent{SetupOperationID: o.setupOperationID}
	switch finalization.Kind {
	case workflowexecution.TaskPreparationHandedOff:
		switch {
		case result == nil:
			event.Phase = serverapi.WorktreeSetupPhaseNotRequired
			event.NotRequired = &serverapi.WorktreeSetupNotRequired{
				Reason:                   serverapi.WorktreeSetupNotRequiredNoTargetPreparation,
				RetainedPreviousWorktree: retainedPreviousWorktree,
			}
		case result.Completed != nil:
			completed := *result.Completed
			if retainedPreviousWorktree != nil {
				completed.RetainedPreviousWorktree = retainedPreviousWorktree
			}
			event.Phase = serverapi.WorktreeSetupPhaseCompleted
			event.Completed = &completed
		case result.NotRequired != nil:
			notRequired := *result.NotRequired
			if retainedPreviousWorktree != nil {
				notRequired.RetainedPreviousWorktree = retainedPreviousWorktree
			}
			event.Phase = serverapi.WorktreeSetupPhaseNotRequired
			event.NotRequired = &notRequired
		default:
			panic("successful Task preparation has a failed or invalid setup result")
		}
	case workflowexecution.TaskPreparationFailed:
		event.Phase = serverapi.WorktreeSetupPhaseFailed
		event.Failed = preparationFailurePayload(result, retainedPreviousWorktree, errors.Join(preparationErr, finalization.Cause))
	case workflowexecution.TaskPreparationInterruptionFailed:
		event.Phase = serverapi.WorktreeSetupPhaseFailed
		event.Failed = nonRetryablePreparationFailure(
			serverapi.WorktreeSetupFailureInterruptionPersistence,
			errors.Join(preparationErr, finalization.Cause),
		)
	case workflowexecution.TaskPreparationCanceled:
		event.Phase = serverapi.WorktreeSetupPhaseFailed
		event.Failed = nonRetryablePreparationFailure(
			serverapi.WorktreeSetupFailureCanceled,
			finalization.Cause,
		)
	case workflowexecution.TaskPreparationControllerShutDown:
		event.Phase = serverapi.WorktreeSetupPhaseFailed
		event.Failed = nonRetryablePreparationFailure(
			serverapi.WorktreeSetupFailureControllerShutdown,
			finalization.Cause,
		)
	default:
		panic(fmt.Sprintf("unknown Task preparation finalization kind %q", finalization.Kind))
	}
	o.publisher.PublishWorkflowTaskSetupEvent(event)
}

func preparationFailurePayload(
	result *worktree.WorktreeSetupResult,
	retainedPreviousWorktree *serverapi.RetainedPreviousWorktree,
	cause error,
) *serverapi.WorktreeSetupFailed {
	if result != nil && result.Failed != nil {
		failed := *result.Failed
		if retainedPreviousWorktree != nil {
			failed.RetainedPreviousWorktree = retainedPreviousWorktree
		}
		return &failed
	}
	return &serverapi.WorktreeSetupFailed{
		RetryReadiness: serverapi.WorktreeSetupRetryReady,
		Cause: serverapi.WorktreeSetupFailureCause{
			Kind:        serverapi.WorktreeSetupFailureTargetPreparation,
			Preparation: &serverapi.WorktreeSetupPreparationFailure{},
		},
		Diagnostic:               preparationDiagnostic(cause),
		RetainedPreviousWorktree: retainedPreviousWorktree,
	}
}

func nonRetryablePreparationFailure(
	kind serverapi.WorktreeSetupFailureKind,
	cause error,
) *serverapi.WorktreeSetupFailed {
	failureCause := serverapi.WorktreeSetupFailureCause{Kind: kind}
	switch kind {
	case serverapi.WorktreeSetupFailureInterruptionPersistence:
		failureCause.InterruptionPersistence = &serverapi.WorktreeSetupInterruptionPersistenceFailure{}
	case serverapi.WorktreeSetupFailureCanceled:
		failureCause.Canceled = &serverapi.WorktreeSetupCanceled{}
	case serverapi.WorktreeSetupFailureControllerShutdown:
		failureCause.ControllerShutdown = &serverapi.WorktreeSetupControllerShutdown{}
	default:
		panic(fmt.Sprintf("unsupported non-retryable Task preparation failure kind %q", kind))
	}
	return &serverapi.WorktreeSetupFailed{
		RetryReadiness: serverapi.WorktreeSetupNonRetryable,
		Cause:          failureCause,
		Diagnostic:     preparationDiagnostic(cause),
	}
}

func preparationDiagnostic(err error) string {
	if err == nil || strings.TrimSpace(err.Error()) == "" {
		return "Workflow Task preparation failed"
	}
	return err.Error()
}

func taskPreparationError(
	setupOperationID serverapi.WorktreeSetupOperationID,
	preflight initiatingActionTargetPreflight,
	result *worktree.WorktreeSetupResult,
	retainedPreviousWorktree *serverapi.RetainedPreviousWorktree,
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
	failed := preparationFailurePayload(result, retainedPreviousWorktree, err)
	if failed.RetryReadiness != serverapi.WorktreeSetupRetryReady {
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
	if detail.Fields == nil {
		detail.Fields = make(map[string]string)
	}
	detail.Fields[workflow.CurrentNodeInterruptionDiagnosticField] = failed.Diagnostic
	detail.SetupRecovery = &workflow.CurrentNodeSetupRecoveryDetail{
		SetupOperationID:         uuid.UUID(setupOperationID),
		Cause:                    workflow.CurrentNodeSetupRecoveryCause(failed.Cause.Kind),
		Diagnostic:               failed.Diagnostic,
		RetainedWorktree:         retained,
		RetainedPreviousWorktree: previous,
	}
	if validationErr := detail.Validate(); validationErr != nil {
		return errors.Join(err, validationErr)
	}
	return workflowexecution.NewTaskStartPreparationError(err, detail)
}

const reasonWorkflowTaskSetupFailed workflow.CurrentNodeInterruptionReason = "workflow_task_setup_failed"

func retainedCurrentNodeWorktree(entry serverapi.WorktreeTopologyEntry) (*workflow.CurrentNodeRetainedWorktree, error) {
	if entry.Variant != serverapi.WorktreeTopologyVariantRegistered || entry.Registered == nil {
		return nil, errors.New("setup recovery retained worktree must be registered")
	}
	retained := &workflow.CurrentNodeRetainedWorktree{
		WorktreeID: entry.Registered.Kent.WorktreeID,
		Root:       entry.Registered.Git.CanonicalRoot,
	}
	return retained, retained.Validate()
}
