package protoapi

import (
	"fmt"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/workflowcontract"
	"core/shared/worktreecontract"

	"google.golang.org/protobuf/types/known/emptypb"
)

func WorktreeSetupEventToProto(event worktreecontract.SetupEvent) (*worktreepb.SetupEvent, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.SetupEvent{SetupOperationId: event.SetupOperationID.String()}
	switch event.Phase {
	case worktreecontract.SetupPhaseStarted:
		message.Phase = &worktreepb.SetupEvent_Started{Started: &worktreepb.SetupStarted{
			SourceWorkspaceRoot: event.Started.SourceWorkspaceRoot,
			WorktreeRoot:        event.Started.WorktreeRoot,
			ScriptPath:          event.Started.ScriptPath,
		}}
	case worktreecontract.SetupPhaseCompleted:
		retained, err := worktreeRetainedPreviousToProto(event.Completed.RetainedPreviousWorktree)
		if err != nil {
			return nil, err
		}
		message.Phase = &worktreepb.SetupEvent_Completed{
			Completed: &worktreepb.SetupCompleted{RetainedPreviousWorktree: retained},
		}
	case worktreecontract.SetupPhaseNotRequired:
		notRequired, err := worktreeSetupNotRequiredToProto(*event.NotRequired)
		if err != nil {
			return nil, err
		}
		message.Phase = &worktreepb.SetupEvent_NotRequired{NotRequired: notRequired}
	case worktreecontract.SetupPhaseFailed:
		failed, err := worktreeSetupFailedToProto(*event.Failed)
		if err != nil {
			return nil, err
		}
		message.Phase = &worktreepb.SetupEvent_Failed{Failed: failed}
	default:
		return nil, fmt.Errorf("worktree setup phase %q is unsupported", event.Phase)
	}
	return message, Validate(message)
}

func WorktreeSetupEventFromProto(message *worktreepb.SetupEvent) (worktreecontract.SetupEvent, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.SetupEvent{}, err
	}
	setupOperationID, err := worktreecontract.ParseSetupOperationID(message.SetupOperationId)
	if err != nil {
		return worktreecontract.SetupEvent{}, err
	}
	event := worktreecontract.SetupEvent{SetupOperationID: setupOperationID}
	switch phase := message.Phase.(type) {
	case *worktreepb.SetupEvent_Started:
		event.Phase = worktreecontract.SetupPhaseStarted
		event.Started = &worktreecontract.SetupStarted{
			SourceWorkspaceRoot: phase.Started.SourceWorkspaceRoot,
			WorktreeRoot:        phase.Started.WorktreeRoot,
			ScriptPath:          phase.Started.ScriptPath,
		}
	case *worktreepb.SetupEvent_Completed:
		retained, conversionErr := worktreeRetainedPreviousFromProto(
			phase.Completed.RetainedPreviousWorktree,
		)
		if conversionErr != nil {
			return worktreecontract.SetupEvent{}, conversionErr
		}
		event.Phase = worktreecontract.SetupPhaseCompleted
		event.Completed = &worktreecontract.SetupCompleted{RetainedPreviousWorktree: retained}
	case *worktreepb.SetupEvent_NotRequired:
		notRequired, conversionErr := worktreeSetupNotRequiredFromProto(phase.NotRequired)
		if conversionErr != nil {
			return worktreecontract.SetupEvent{}, conversionErr
		}
		event.Phase = worktreecontract.SetupPhaseNotRequired
		event.NotRequired = &notRequired
	case *worktreepb.SetupEvent_Failed:
		failed, conversionErr := worktreeSetupFailedFromProto(phase.Failed)
		if conversionErr != nil {
			return worktreecontract.SetupEvent{}, conversionErr
		}
		event.Phase = worktreecontract.SetupPhaseFailed
		event.Failed = &failed
	default:
		return worktreecontract.SetupEvent{}, fmt.Errorf("protobuf Worktree setup phase %T is unsupported", phase)
	}
	return event, event.Validate()
}

func worktreeSetupNotRequiredToProto(value worktreecontract.SetupNotRequired) (*worktreepb.SetupNotRequired, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	var reason worktreepb.SetupNotRequiredReason
	switch value.Reason {
	case worktreecontract.SetupNotRequiredNoTargetPreparation:
		reason = worktreepb.SetupNotRequiredReason_WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_TARGET_PREPARATION
	case worktreecontract.SetupNotRequiredNoConfiguredScript:
		reason = worktreepb.SetupNotRequiredReason_WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_CONFIGURED_SCRIPT
	default:
		return nil, fmt.Errorf("worktree setup not-required reason %q is unsupported", value.Reason)
	}
	retained, err := worktreeRetainedPreviousToProto(value.RetainedPreviousWorktree)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.SetupNotRequired{Reason: reason, RetainedPreviousWorktree: retained}
	return message, Validate(message)
}

func worktreeSetupNotRequiredFromProto(message *worktreepb.SetupNotRequired) (worktreecontract.SetupNotRequired, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.SetupNotRequired{}, err
	}
	var reason worktreecontract.SetupNotRequiredReason
	switch message.Reason {
	case worktreepb.SetupNotRequiredReason_WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_TARGET_PREPARATION:
		reason = worktreecontract.SetupNotRequiredNoTargetPreparation
	case worktreepb.SetupNotRequiredReason_WORKTREE_SETUP_NOT_REQUIRED_REASON_NO_CONFIGURED_SCRIPT:
		reason = worktreecontract.SetupNotRequiredNoConfiguredScript
	default:
		return worktreecontract.SetupNotRequired{}, fmt.Errorf(
			"protobuf Worktree setup not-required reason %v is unsupported",
			message.Reason,
		)
	}
	retained, err := worktreeRetainedPreviousFromProto(message.RetainedPreviousWorktree)
	if err != nil {
		return worktreecontract.SetupNotRequired{}, err
	}
	value := worktreecontract.SetupNotRequired{Reason: reason, RetainedPreviousWorktree: retained}
	return value, value.Validate()
}

func worktreeSetupFailedToProto(value worktreecontract.SetupFailed) (*worktreepb.SetupFailed, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	readiness, err := worktreeSetupRetryReadinessToProto(value.RetryReadiness)
	if err != nil {
		return nil, err
	}
	cause, err := worktreeSetupFailureCauseToProto(value.Cause)
	if err != nil {
		return nil, err
	}
	executionTarget, err := worktreeSetupExecutionTargetToProto(value.ExecutionTarget)
	if err != nil {
		return nil, err
	}
	var retainedWorktree *worktreepb.RegisteredFacts
	if value.RetainedWorktree != nil {
		retainedWorktree, err = worktreeRegisteredFactsToProto(*value.RetainedWorktree.Registered)
		if err != nil {
			return nil, err
		}
	}
	retainedPrevious, err := worktreeRetainedPreviousToProto(value.RetainedPreviousWorktree)
	if err != nil {
		return nil, err
	}
	message := &worktreepb.SetupFailed{
		RetryReadiness:           readiness,
		Cause:                    cause,
		Diagnostic:               value.Diagnostic,
		ScriptPath:               clonePointer(value.ScriptPath),
		ExecutionTarget:          executionTarget,
		RetainedWorktree:         retainedWorktree,
		RetainedPreviousWorktree: retainedPrevious,
	}
	return message, Validate(message)
}

func worktreeSetupFailedFromProto(message *worktreepb.SetupFailed) (worktreecontract.SetupFailed, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.SetupFailed{}, err
	}
	readiness, err := worktreeSetupRetryReadinessFromProto(message.RetryReadiness)
	if err != nil {
		return worktreecontract.SetupFailed{}, err
	}
	cause, err := worktreeSetupFailureCauseFromProto(message.Cause)
	if err != nil {
		return worktreecontract.SetupFailed{}, err
	}
	executionTarget, err := worktreeSetupExecutionTargetFromProto(message.ExecutionTarget)
	if err != nil {
		return worktreecontract.SetupFailed{}, err
	}
	var retainedWorktree *worktreecontract.TopologyEntry
	if message.RetainedWorktree != nil {
		registered, conversionErr := worktreeRegisteredFactsFromProto(message.RetainedWorktree)
		if conversionErr != nil {
			return worktreecontract.SetupFailed{}, conversionErr
		}
		retainedWorktree = &worktreecontract.TopologyEntry{
			Variant:    worktreecontract.TopologyVariantRegistered,
			Registered: &registered,
		}
	}
	retainedPrevious, err := worktreeRetainedPreviousFromProto(message.RetainedPreviousWorktree)
	if err != nil {
		return worktreecontract.SetupFailed{}, err
	}
	value := worktreecontract.SetupFailed{
		RetryReadiness:           readiness,
		Cause:                    cause,
		Diagnostic:               message.Diagnostic,
		ScriptPath:               clonePointer(message.ScriptPath),
		ExecutionTarget:          executionTarget,
		RetainedWorktree:         retainedWorktree,
		RetainedPreviousWorktree: retainedPrevious,
	}
	return value, value.Validate()
}

func worktreeSetupRetryReadinessToProto(value worktreecontract.SetupRetryReadiness) (worktreepb.SetupRetryReadiness, error) {
	switch value {
	case worktreecontract.SetupRetryReady:
		return worktreepb.SetupRetryReadiness_WORKTREE_SETUP_RETRY_READY, nil
	case worktreecontract.SetupNonRetryable:
		return worktreepb.SetupRetryReadiness_WORKTREE_SETUP_NON_RETRYABLE, nil
	default:
		return worktreepb.SetupRetryReadiness_WORKTREE_SETUP_RETRY_READINESS_UNSPECIFIED, fmt.Errorf(
			"worktree setup retry readiness %q is unsupported",
			value,
		)
	}
}

func worktreeSetupRetryReadinessFromProto(value worktreepb.SetupRetryReadiness) (worktreecontract.SetupRetryReadiness, error) {
	switch value {
	case worktreepb.SetupRetryReadiness_WORKTREE_SETUP_RETRY_READY:
		return worktreecontract.SetupRetryReady, nil
	case worktreepb.SetupRetryReadiness_WORKTREE_SETUP_NON_RETRYABLE:
		return worktreecontract.SetupNonRetryable, nil
	default:
		return "", fmt.Errorf("protobuf Worktree setup retry readiness %v is unsupported", value)
	}
}

func worktreeSetupFailureCauseToProto(value worktreecontract.SetupFailureCause) (*worktreepb.SetupFailureCause, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	message := &worktreepb.SetupFailureCause{}
	switch value.Kind {
	case worktreecontract.SetupFailureProcessExit:
		exitCode, err := projectInt32(value.ProcessExit.ExitCode, "worktree setup process exit code")
		if err != nil {
			return nil, err
		}
		message.Cause = &worktreepb.SetupFailureCause_ProcessExit{
			ProcessExit: &worktreepb.SetupProcessExit{
				ExitCode: exitCode,
				Stdout:   clonePointer(value.ProcessExit.Stdout),
				Stderr:   clonePointer(value.ProcessExit.Stderr),
			},
		}
	case worktreecontract.SetupFailureTimeout:
		message.Cause = &worktreepb.SetupFailureCause_Timeout{
			Timeout: &worktreepb.SetupTimeout{
				Stdout: clonePointer(value.Timeout.Stdout),
				Stderr: clonePointer(value.Timeout.Stderr),
			},
		}
	case worktreecontract.SetupFailureTargetPreparation:
		message.Cause = &worktreepb.SetupFailureCause_TargetPreparation{
			TargetPreparation: &emptypb.Empty{},
		}
	case worktreecontract.SetupFailureInterruptionPersistence:
		message.Cause = &worktreepb.SetupFailureCause_InterruptionPersistence{
			InterruptionPersistence: &emptypb.Empty{},
		}
	case worktreecontract.SetupFailureCanceled:
		message.Cause = &worktreepb.SetupFailureCause_Canceled{Canceled: &emptypb.Empty{}}
	case worktreecontract.SetupFailureControllerShutdown:
		message.Cause = &worktreepb.SetupFailureCause_ControllerShutdown{
			ControllerShutdown: &emptypb.Empty{},
		}
	case worktreecontract.SetupFailureOperational:
		message.Cause = &worktreepb.SetupFailureCause_Operational{Operational: &emptypb.Empty{}}
	default:
		return nil, fmt.Errorf("worktree setup failure kind %q is unsupported", value.Kind)
	}
	return message, Validate(message)
}

func worktreeSetupFailureCauseFromProto(message *worktreepb.SetupFailureCause) (worktreecontract.SetupFailureCause, error) {
	if err := Validate(message); err != nil {
		return worktreecontract.SetupFailureCause{}, err
	}
	var cause worktreecontract.SetupFailureCause
	switch value := message.Cause.(type) {
	case *worktreepb.SetupFailureCause_ProcessExit:
		cause.Kind = worktreecontract.SetupFailureProcessExit
		cause.ProcessExit = &worktreecontract.SetupProcessExit{
			ExitCode: int(value.ProcessExit.ExitCode),
			Stdout:   clonePointer(value.ProcessExit.Stdout),
			Stderr:   clonePointer(value.ProcessExit.Stderr),
		}
	case *worktreepb.SetupFailureCause_Timeout:
		cause.Kind = worktreecontract.SetupFailureTimeout
		cause.Timeout = &worktreecontract.SetupTimeout{
			Stdout: clonePointer(value.Timeout.Stdout),
			Stderr: clonePointer(value.Timeout.Stderr),
		}
	case *worktreepb.SetupFailureCause_TargetPreparation:
		cause.Kind = worktreecontract.SetupFailureTargetPreparation
		cause.Preparation = &worktreecontract.SetupPreparationFailure{}
	case *worktreepb.SetupFailureCause_InterruptionPersistence:
		cause.Kind = worktreecontract.SetupFailureInterruptionPersistence
		cause.InterruptionPersistence = &worktreecontract.SetupInterruptionPersistenceFailure{}
	case *worktreepb.SetupFailureCause_Canceled:
		cause.Kind = worktreecontract.SetupFailureCanceled
		cause.Canceled = &worktreecontract.SetupCanceled{}
	case *worktreepb.SetupFailureCause_ControllerShutdown:
		cause.Kind = worktreecontract.SetupFailureControllerShutdown
		cause.ControllerShutdown = &worktreecontract.SetupControllerShutdown{}
	case *worktreepb.SetupFailureCause_Operational:
		cause.Kind = worktreecontract.SetupFailureOperational
		cause.Operational = &worktreecontract.SetupOperationalFailure{}
	default:
		return worktreecontract.SetupFailureCause{}, fmt.Errorf(
			"protobuf Worktree setup failure cause %T is unsupported",
			value,
		)
	}
	return cause, cause.Validate()
}

func worktreeSetupExecutionTargetToProto(value *workflowcontract.ExecutionTargetSelection) (*worktreepb.SetupExecutionTargetSelection, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	var mode worktreepb.SetupExecutionTargetMode
	switch value.Mode {
	case workflowcontract.ExecutionTargetModeNone:
		mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_NONE
	case workflowcontract.ExecutionTargetModeHead:
		mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_HEAD
	case workflowcontract.ExecutionTargetModeDefaultBranch:
		mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_DEFAULT_BRANCH
	case workflowcontract.ExecutionTargetModeCustomRef:
		mode = worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_CUSTOM_REF
	default:
		return nil, fmt.Errorf("worktree setup execution target mode %q is unsupported", value.Mode)
	}
	message := &worktreepb.SetupExecutionTargetSelection{
		Mode: mode, CustomRef: clonePointer(value.CustomRef),
	}
	return message, Validate(message)
}

func worktreeSetupExecutionTargetFromProto(message *worktreepb.SetupExecutionTargetSelection) (*workflowcontract.ExecutionTargetSelection, error) {
	if message == nil {
		return nil, nil
	}
	if err := Validate(message); err != nil {
		return nil, err
	}
	var mode workflowcontract.ExecutionTargetMode
	switch message.Mode {
	case worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_NONE:
		mode = workflowcontract.ExecutionTargetModeNone
	case worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_HEAD:
		mode = workflowcontract.ExecutionTargetModeHead
	case worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_DEFAULT_BRANCH:
		mode = workflowcontract.ExecutionTargetModeDefaultBranch
	case worktreepb.SetupExecutionTargetMode_WORKTREE_SETUP_EXECUTION_TARGET_MODE_CUSTOM_REF:
		mode = workflowcontract.ExecutionTargetModeCustomRef
	default:
		return nil, fmt.Errorf("protobuf Worktree setup execution target mode %v is unsupported", message.Mode)
	}
	value := &workflowcontract.ExecutionTargetSelection{
		Mode: mode, CustomRef: clonePointer(message.CustomRef),
	}
	return value, value.Validate()
}
