package workflowsvc

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/worktree"
	"core/shared/protoapi"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

type taskSetupObservation struct {
	setupOperationID worktreecontract.SetupOperationID
	executionTarget  *worktreepb.SetupExecutionTargetSelection
	publisher        workflowTaskSetupEventPublisher

	mu             sync.Mutex
	prepared       preparedInitiatingActionTarget
	preparationErr error
	finalized      bool
}

func newTaskSetupObservation(setupOperationID worktreecontract.SetupOperationID, executionTarget workflow.ExecutionTargetSelection, publisher workflowTaskSetupEventPublisher) (*taskSetupObservation, error) {
	if err := setupOperationID.Validate(); err != nil {
		return nil, err
	}
	if publisher == nil {
		return nil, errors.New("Workflow Task setup event publisher is required")
	}
	target, err := worktreeSetupExecutionTarget(executionTarget)
	if err != nil {
		return nil, err
	}
	return &taskSetupObservation{
		setupOperationID: setupOperationID,
		executionTarget:  target,
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

	event := &worktreepb.SetupEvent{SetupOperationId: o.setupOperationID.String()}
	switch finalization.Kind {
	case workflowexecution.TaskPreparationHandedOff:
		switch {
		case prepared.setupResult == nil:
			event.Phase = &worktreepb.SetupEvent_NotRequired{NotRequired: &worktreepb.SetupNotRequired{
				Reason:                   worktreepb.SetupNotRequiredReason_WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_TARGET_PREPARATION,
				RetainedPreviousWorktree: prepared.retainedPreviousWorktree,
			}}
		case prepared.setupResult.Completed != nil:
			completed := proto.Clone(prepared.setupResult.Completed).(*worktreepb.SetupCompleted)
			if prepared.retainedPreviousWorktree != nil {
				completed.RetainedPreviousWorktree = prepared.retainedPreviousWorktree
			}
			event.Phase = &worktreepb.SetupEvent_Completed{Completed: completed}
		case prepared.setupResult.NotRequired != nil:
			notRequired := proto.Clone(prepared.setupResult.NotRequired).(*worktreepb.SetupNotRequired)
			if prepared.retainedPreviousWorktree != nil {
				notRequired.RetainedPreviousWorktree = prepared.retainedPreviousWorktree
			}
			event.Phase = &worktreepb.SetupEvent_NotRequired{NotRequired: notRequired}
		default:
			panic("successful Task preparation has a failed or invalid setup result")
		}
	case workflowexecution.TaskPreparationFailed:
		failed := preparationFailurePayload(prepared.setupResult, prepared.retainedWorktree, prepared.retainedPreviousWorktree, errors.Join(preparationErr, finalization.Cause))
		failed.ExecutionTarget = proto.Clone(o.executionTarget).(*worktreepb.SetupExecutionTargetSelection)
		event.Phase = &worktreepb.SetupEvent_Failed{Failed: failed}
	case workflowexecution.TaskPreparationInterruptionFailed:
		event.Phase = &worktreepb.SetupEvent_Failed{Failed: interruptionPersistenceFailurePayload(
			prepared.setupResult,
			prepared.retainedWorktree,
			prepared.retainedPreviousWorktree,
			preparationErr,
			finalization.Cause,
		)}
	case workflowexecution.TaskPreparationCanceled:
		event.Phase = &worktreepb.SetupEvent_Failed{Failed: nonRetryablePreparationFailure(
			worktreecontract.SetupFailureCanceled,
			finalization.Cause,
		)}
	case workflowexecution.TaskPreparationControllerShutDown:
		event.Phase = &worktreepb.SetupEvent_Failed{Failed: nonRetryablePreparationFailure(
			worktreecontract.SetupFailureControllerShutdown,
			finalization.Cause,
		)}
	default:
		panic(fmt.Sprintf("unknown Task preparation finalization kind %q", finalization.Kind))
	}
	o.publisher.PublishWorkflowTaskSetupEvent(event)
}

func preparationFailurePayload(result *worktree.WorktreeSetupResult, retainedWorktree *worktreepb.RegisteredFacts, retainedPreviousWorktree *worktreepb.RetainedPreviousWorktree, cause error) *worktreepb.SetupFailed {
	if result != nil && result.Failed != nil {
		failed := proto.Clone(result.Failed).(*worktreepb.SetupFailed)
		if failed.RetainedWorktree == nil && retainedWorktree != nil {
			failed.RetainedWorktree = retainedWorktree
		}
		if retainedPreviousWorktree != nil {
			failed.RetainedPreviousWorktree = retainedPreviousWorktree
		}
		return failed
	}
	return &worktreepb.SetupFailed{
		RetryReadiness: worktreepb.SetupRetryReadiness_WORKTREE_SETUP_RETRY_READY,
		Cause: &worktreepb.SetupFailureCause{
			Cause: &worktreepb.SetupFailureCause_TargetPreparation{TargetPreparation: &emptypb.Empty{}},
		},
		Diagnostic:               preparationDiagnostic(cause),
		RetainedWorktree:         retainedWorktree,
		RetainedPreviousWorktree: retainedPreviousWorktree,
	}
}

func nonRetryablePreparationFailure(kind worktreecontract.SetupFailureKind, cause error) *worktreepb.SetupFailed {
	var failureCause *worktreepb.SetupFailureCause
	switch kind {
	case worktreecontract.SetupFailureCanceled:
		failureCause = &worktreepb.SetupFailureCause{
			Cause: &worktreepb.SetupFailureCause_Canceled{Canceled: &emptypb.Empty{}},
		}
	case worktreecontract.SetupFailureControllerShutdown:
		failureCause = &worktreepb.SetupFailureCause{
			Cause: &worktreepb.SetupFailureCause_ControllerShutdown{ControllerShutdown: &emptypb.Empty{}},
		}
	default:
		panic(fmt.Sprintf("unsupported non-retryable Task preparation failure kind %q", kind))
	}
	return &worktreepb.SetupFailed{
		RetryReadiness: worktreepb.SetupRetryReadiness_WORKTREE_SETUP_NON_RETRYABLE,
		Cause:          failureCause,
		Diagnostic:     preparationDiagnostic(cause),
	}
}

func interruptionPersistenceFailurePayload(result *worktree.WorktreeSetupResult, retainedWorktree *worktreepb.RegisteredFacts, retainedPreviousWorktree *worktreepb.RetainedPreviousWorktree, preparationErr error, persistenceErr error) *worktreepb.SetupFailed {
	failed := preparationFailurePayload(
		result,
		retainedWorktree,
		retainedPreviousWorktree,
		errors.Join(preparationErr, persistenceErr),
	)
	failed.RetryReadiness = worktreepb.SetupRetryReadiness_WORKTREE_SETUP_NON_RETRYABLE
	failed.Cause = &worktreepb.SetupFailureCause{
		Cause: &worktreepb.SetupFailureCause_InterruptionPersistence{InterruptionPersistence: &emptypb.Empty{}},
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
	retainedWorktree *worktreepb.RegisteredFacts,
	retainedPreviousWorktree *worktreepb.RetainedPreviousWorktree,
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
	if failed.RetryReadiness != worktreepb.SetupRetryReadiness_WORKTREE_SETUP_RETRY_READY {
		return err
	}
	failureKind, kindErr := worktreeSetupFailureKind(failed.Cause)
	if kindErr != nil {
		return errors.Join(err, kindErr)
	}
	var retained *workflow.CurrentNodeRetainedWorktree
	var retainedErr error
	if failed.RetainedWorktree != nil {
		retained, retainedErr = retainedCurrentNodeWorktree(failed.RetainedWorktree)
		if retainedErr != nil {
			return errors.Join(err, retainedErr)
		}
	}
	var previous *workflow.CurrentNodeRetainedWorktree
	if failed.RetainedPreviousWorktree != nil {
		previous, retainedErr = retainedCurrentNodeWorktree(failed.RetainedPreviousWorktree.GetWorktree())
		if retainedErr != nil {
			return errors.Join(err, retainedErr)
		}
	}
	delete(detail.Fields, workflow.CurrentNodeInterruptionDiagnosticField)
	detail.SetupRecovery = &workflow.CurrentNodeSetupRecoveryDetail{
		SetupOperationID:         uuid.UUID(setupOperationID),
		Cause:                    failureKind,
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

func retainedCurrentNodeWorktree(entry *worktreepb.RegisteredFacts) (*workflow.CurrentNodeRetainedWorktree, error) {
	if entry == nil {
		return nil, errors.New("setup recovery retained worktree must be registered")
	}
	if err := protoapi.Validate(entry); err != nil {
		return nil, err
	}
	retained := &workflow.CurrentNodeRetainedWorktree{
		WorktreeID: entry.GetKent().GetWorktreeId(),
		Root:       entry.GetGit().GetCanonicalRoot(),
	}
	return retained, retained.Validate()
}

func worktreeSetupExecutionTarget(selection workflow.ExecutionTargetSelection) (*worktreepb.SetupExecutionTargetSelection, error) {
	target := &worktreepb.SetupExecutionTargetSelection{CustomRef: selection.CustomRef}
	switch selection.Mode {
	case workflow.ExecutionTargetModeNone:
		target.Mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_NONE
	case workflow.ExecutionTargetModeHead:
		target.Mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_HEAD
	case workflow.ExecutionTargetModeDefaultBranch:
		target.Mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_DEFAULT_BRANCH
	case workflow.ExecutionTargetModeCustomRef:
		target.Mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_CUSTOM_REF
	case workflow.ExecutionTargetModeAskOnFirstExecution:
		target.Mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_ASK_ON_FIRST_EXECUTION
	default:
		return nil, fmt.Errorf("worktree setup execution target mode %q is invalid", selection.Mode)
	}
	if err := protoapi.Validate(target); err != nil {
		return nil, err
	}
	return target, nil
}

func worktreeSetupFailureKind(cause *worktreepb.SetupFailureCause) (worktreecontract.SetupFailureKind, error) {
	if cause == nil {
		return "", errors.New("worktree setup failure cause is required")
	}
	switch cause.GetCause().(type) {
	case *worktreepb.SetupFailureCause_ProcessExit:
		return worktreecontract.SetupFailureProcessExit, nil
	case *worktreepb.SetupFailureCause_Timeout:
		return worktreecontract.SetupFailureTimeout, nil
	case *worktreepb.SetupFailureCause_TargetPreparation:
		return worktreecontract.SetupFailureTargetPreparation, nil
	case *worktreepb.SetupFailureCause_InterruptionPersistence:
		return worktreecontract.SetupFailureInterruptionPersistence, nil
	case *worktreepb.SetupFailureCause_Canceled:
		return worktreecontract.SetupFailureCanceled, nil
	case *worktreepb.SetupFailureCause_ControllerShutdown:
		return worktreecontract.SetupFailureControllerShutdown, nil
	case *worktreepb.SetupFailureCause_Operational:
		return worktreecontract.SetupFailureOperational, nil
	default:
		return "", errors.New("worktree setup failure cause is invalid")
	}
}
