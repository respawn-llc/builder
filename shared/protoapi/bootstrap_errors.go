package protoapi

import (
	"context"
	"errors"
	"fmt"

	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func ServerNotReadyToProto(err *serverapi.ServerNotReadyError) (*serverpb.ServerNotReadyDetails, error) {
	if err == nil {
		return nil, errors.New("server-not-ready error is required")
	}
	reason, conversionErr := serverNotReadyReasonToProto(err.Reason)
	if conversionErr != nil {
		return nil, conversionErr
	}
	details := &serverpb.ServerNotReadyDetails{Reason: reason}
	switch value := err.Details.(type) {
	case nil:
	case serverapi.ServerNotReadyDetails:
		details.OnboardingCompleted = value.OnboardingCompleted
		details.SettingsPath = clonePointer(value.SettingsPath)
		details.Diagnostic = clonePointer(value.Diagnostic)
	default:
		return nil, fmt.Errorf("server-not-ready details type %T is unsupported", err.Details)
	}
	if validationErr := Validate(details); validationErr != nil {
		return nil, validationErr
	}
	return details, nil
}

func ServerNotReadyFromProto(details *serverpb.ServerNotReadyDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	reason, err := serverNotReadyReasonFromProto(details.Reason)
	if err != nil {
		return err
	}
	return serverapi.NewServerNotReadyError(reason, serverapi.ServerNotReadyDetails{
		OnboardingCompleted: details.OnboardingCompleted,
		SettingsPath:        clonePointer(details.SettingsPath),
		Diagnostic:          clonePointer(details.Diagnostic),
	}, nil)
}

func OnboardingFinalizeErrorToProto(err *serverapi.OnboardingFinalizeError) (*onboardingpb.FinalizeError, error) {
	if err == nil {
		return nil, errors.New("onboarding finalize error is required")
	}
	result := &onboardingpb.FinalizeError{Code: string(err.Code)}
	switch err.Code {
	case serverapi.OnboardingFinalizeInvalidRequest:
		details, ok := err.Details.(serverapi.OnboardingInvalidRequestDetails)
		if !ok {
			return nil, onboardingDetailsTypeError(err, details)
		}
		result.Detail = &onboardingpb.FinalizeError_InvalidRequest{InvalidRequest: invalidRequestDetailsToProto(details)}
	case serverapi.OnboardingFinalizeConfigAlreadyExists:
		details, ok := err.Details.(serverapi.OnboardingConfigAlreadyExistsDetails)
		if !ok {
			return nil, onboardingDetailsTypeError(err, details)
		}
		result.Detail = &onboardingpb.FinalizeError_ConfigAlreadyExists{
			ConfigAlreadyExists: &onboardingpb.ConfigAlreadyExistsDetails{SettingsPath: details.SettingsPath},
		}
	case serverapi.OnboardingFinalizeImportUnavailable:
		details, ok := err.Details.(serverapi.OnboardingImportUnavailableDetails)
		if !ok {
			return nil, onboardingDetailsTypeError(err, details)
		}
		converted, conversionErr := importUnavailableDetailsToProto(details)
		if conversionErr != nil {
			return nil, conversionErr
		}
		result.Detail = &onboardingpb.FinalizeError_ImportUnavailable{ImportUnavailable: converted}
	case serverapi.OnboardingFinalizeImportFailed:
		details, ok := err.Details.(serverapi.OnboardingImportFailedDetails)
		if !ok {
			return nil, onboardingDetailsTypeError(err, details)
		}
		converted, conversionErr := importFailedDetailsToProto(details)
		if conversionErr != nil {
			return nil, conversionErr
		}
		result.Detail = &onboardingpb.FinalizeError_ImportFailed{ImportFailed: converted}
	case serverapi.OnboardingFinalizeConfigWriteFailed:
		details, ok := err.Details.(serverapi.OnboardingConfigWriteFailedDetails)
		if !ok {
			return nil, onboardingDetailsTypeError(err, details)
		}
		result.Detail = &onboardingpb.FinalizeError_ConfigWriteFailed{
			ConfigWriteFailed: &onboardingpb.ConfigWriteFailedDetails{
				SettingsPath: details.SettingsPath,
				Operation:    details.Operation,
				Cause:        details.Cause,
			},
		}
	case serverapi.OnboardingFinalizeRollbackFailed:
		details, ok := err.Details.(serverapi.OnboardingRollbackFailedDetails)
		if !ok {
			return nil, onboardingDetailsTypeError(err, details)
		}
		converted, conversionErr := rollbackFailedDetailsToProto(details)
		if conversionErr != nil {
			return nil, conversionErr
		}
		result.Detail = &onboardingpb.FinalizeError_RollbackFailed{RollbackFailed: converted}
	case serverapi.OnboardingFinalizeCanceled:
		details, ok := err.Details.(serverapi.OnboardingCanceledDetails)
		if !ok {
			return nil, onboardingDetailsTypeError(err, details)
		}
		phase, conversionErr := cancelPhaseToProto(details.Phase)
		if conversionErr != nil {
			return nil, conversionErr
		}
		result.Detail = &onboardingpb.FinalizeError_Canceled{
			Canceled: &onboardingpb.CanceledDetails{Phase: phase},
		}
	default:
		return nil, fmt.Errorf("onboarding finalize error code %q is unsupported", err.Code)
	}
	if validationErr := Validate(result); validationErr != nil {
		return nil, validationErr
	}
	return result, nil
}

func OnboardingFinalizeErrorFromProto(message *onboardingpb.FinalizeError) error {
	if err := Validate(message); err != nil {
		return err
	}
	code := serverapi.OnboardingFinalizeErrorCode(message.Code)
	var details any
	switch detail := message.Detail.(type) {
	case *onboardingpb.FinalizeError_InvalidRequest:
		details = invalidRequestDetailsFromProto(detail.InvalidRequest)
	case *onboardingpb.FinalizeError_ConfigAlreadyExists:
		details = serverapi.OnboardingConfigAlreadyExistsDetails{SettingsPath: detail.ConfigAlreadyExists.SettingsPath}
	case *onboardingpb.FinalizeError_ImportUnavailable:
		converted, err := importUnavailableDetailsFromProto(detail.ImportUnavailable)
		if err != nil {
			return err
		}
		details = converted
	case *onboardingpb.FinalizeError_ImportFailed:
		converted, err := importFailedDetailsFromProto(detail.ImportFailed)
		if err != nil {
			return err
		}
		details = converted
	case *onboardingpb.FinalizeError_ConfigWriteFailed:
		details = serverapi.OnboardingConfigWriteFailedDetails{
			SettingsPath: detail.ConfigWriteFailed.SettingsPath,
			Operation:    detail.ConfigWriteFailed.Operation,
			Cause:        detail.ConfigWriteFailed.Cause,
		}
	case *onboardingpb.FinalizeError_RollbackFailed:
		converted, err := rollbackFailedDetailsFromProto(detail.RollbackFailed)
		if err != nil {
			return err
		}
		details = converted
	case *onboardingpb.FinalizeError_Canceled:
		phase, err := cancelPhaseFromProto(detail.Canceled.Phase)
		if err != nil {
			return err
		}
		details = serverapi.OnboardingCanceledDetails{Phase: phase}
	case *onboardingpb.FinalizeError_ServerNotReady:
		return ServerNotReadyFromProto(detail.ServerNotReady)
	case *onboardingpb.FinalizeError_InternalFailure:
		return InternalFailureFromProto(detail.InternalFailure)
	default:
		return fmt.Errorf("onboarding finalize error detail %T is unsupported", detail)
	}
	typed := serverapi.NewOnboardingFinalizeError(code, details, nil)
	if code == serverapi.OnboardingFinalizeCanceled {
		return errors.Join(typed, context.Canceled)
	}
	return typed
}

func InternalFailureFromProto(details *sharedpb.InternalFailureDetails) error {
	if err := Validate(details); err != nil {
		return err
	}
	if details.Cause != nil {
		return errors.New(*details.Cause)
	}
	return errors.New("server operation failed")
}

func invalidRequestDetailsToProto(details serverapi.OnboardingInvalidRequestDetails) *onboardingpb.InvalidRequestDetails {
	return &onboardingpb.InvalidRequestDetails{
		FieldErrors: mapSlice(details.FieldErrors, func(field serverapi.OnboardingFinalizeFieldError) *onboardingpb.FieldError {
			return &onboardingpb.FieldError{Field: field.Field, Code: field.Code}
		}),
	}
}

func invalidRequestDetailsFromProto(details *onboardingpb.InvalidRequestDetails) serverapi.OnboardingInvalidRequestDetails {
	return serverapi.OnboardingInvalidRequestDetails{
		FieldErrors: mapSlice(details.FieldErrors, func(field *onboardingpb.FieldError) serverapi.OnboardingFinalizeFieldError {
			return serverapi.OnboardingFinalizeFieldError{Field: field.Field, Code: field.Code}
		}),
	}
}

func importUnavailableDetailsToProto(details serverapi.OnboardingImportUnavailableDetails) (*onboardingpb.ImportUnavailableDetails, error) {
	kind, err := importKindToProto(details.ImportKind)
	if err != nil {
		return nil, err
	}
	mode, err := onboardingImportModeToProto(details.Mode)
	if err != nil {
		return nil, err
	}
	reason, err := importUnavailableReasonToProto(details.ReasonCode)
	if err != nil {
		return nil, err
	}
	return &onboardingpb.ImportUnavailableDetails{
		ImportKind:       kind,
		Mode:             mode,
		ProviderUuid:     uuidToString(details.ProviderUUID),
		ImportProviderId: clonePointer(details.ImportProviderID),
		SourceRootPath:   clonePointer(details.SourceRootPath),
		Reason:           reason,
	}, nil
}

func importUnavailableDetailsFromProto(details *onboardingpb.ImportUnavailableDetails) (serverapi.OnboardingImportUnavailableDetails, error) {
	kind, err := importKindFromProto(details.ImportKind)
	if err != nil {
		return serverapi.OnboardingImportUnavailableDetails{}, err
	}
	mode, err := onboardingImportModeFromProto(details.Mode)
	if err != nil {
		return serverapi.OnboardingImportUnavailableDetails{}, err
	}
	reason, err := importUnavailableReasonFromProto(details.Reason)
	if err != nil {
		return serverapi.OnboardingImportUnavailableDetails{}, err
	}
	provider, err := uuidFromString(details.ProviderUuid)
	if err != nil {
		return serverapi.OnboardingImportUnavailableDetails{}, err
	}
	return serverapi.OnboardingImportUnavailableDetails{
		ImportKind:       kind,
		Mode:             mode,
		ProviderUUID:     provider,
		ImportProviderID: clonePointer(details.ImportProviderId),
		SourceRootPath:   clonePointer(details.SourceRootPath),
		ReasonCode:       reason,
	}, nil
}

func importFailedDetailsToProto(details serverapi.OnboardingImportFailedDetails) (*onboardingpb.ImportFailedDetails, error) {
	var kind *onboardingpb.ImportKind
	if details.ImportKind != "" {
		value, err := importKindToProto(details.ImportKind)
		if err != nil {
			return nil, err
		}
		kind = &value
	}
	operation, err := importOperationToProto(details.Operation)
	if err != nil {
		return nil, err
	}
	return &onboardingpb.ImportFailedDetails{
		ImportKind:       kind,
		ProviderUuid:     uuidToString(details.ProviderUUID),
		ImportProviderId: clonePointer(details.ImportProviderID),
		SourceRootPath:   clonePointer(details.SourceRootPath),
		Operation:        operation,
		Cause:            details.Cause,
	}, nil
}

func importFailedDetailsFromProto(details *onboardingpb.ImportFailedDetails) (serverapi.OnboardingImportFailedDetails, error) {
	var kind serverapi.OnboardingImportKind
	if details.ImportKind != nil {
		value, err := importKindFromProto(*details.ImportKind)
		if err != nil {
			return serverapi.OnboardingImportFailedDetails{}, err
		}
		kind = value
	}
	operation, err := importOperationFromProto(details.Operation)
	if err != nil {
		return serverapi.OnboardingImportFailedDetails{}, err
	}
	provider, err := uuidFromString(details.ProviderUuid)
	if err != nil {
		return serverapi.OnboardingImportFailedDetails{}, err
	}
	return serverapi.OnboardingImportFailedDetails{
		ImportKind:       kind,
		ProviderUUID:     provider,
		ImportProviderID: clonePointer(details.ImportProviderId),
		SourceRootPath:   clonePointer(details.SourceRootPath),
		Operation:        operation,
		Cause:            details.Cause,
	}, nil
}

func rollbackFailedDetailsToProto(details serverapi.OnboardingRollbackFailedDetails) (*onboardingpb.RollbackFailedDetails, error) {
	primary, err := rollbackPrimaryToProto(details.Primary)
	if err != nil {
		return nil, err
	}
	return &onboardingpb.RollbackFailedDetails{
		Primary: primary,
		Rollback: &onboardingpb.RollbackFailureFact{
			Operation: details.Rollback.Operation,
			Cause:     details.Rollback.Cause,
		},
	}, nil
}

func rollbackPrimaryToProto(primary serverapi.OnboardingRollbackPrimaryFailure) (*onboardingpb.RollbackPrimaryFailure, error) {
	message := &onboardingpb.RollbackPrimaryFailure{}
	switch {
	case primary.InvalidRequest != nil:
		message.Failure = &onboardingpb.RollbackPrimaryFailure_InvalidRequest{InvalidRequest: invalidRequestDetailsToProto(*primary.InvalidRequest)}
	case primary.ConfigAlreadyExists != nil:
		message.Failure = &onboardingpb.RollbackPrimaryFailure_ConfigAlreadyExists{
			ConfigAlreadyExists: &onboardingpb.ConfigAlreadyExistsDetails{SettingsPath: primary.ConfigAlreadyExists.SettingsPath},
		}
	case primary.ImportUnavailable != nil:
		details, err := importUnavailableDetailsToProto(*primary.ImportUnavailable)
		if err != nil {
			return nil, err
		}
		message.Failure = &onboardingpb.RollbackPrimaryFailure_ImportUnavailable{ImportUnavailable: details}
	case primary.ImportFailed != nil:
		details, err := importFailedDetailsToProto(*primary.ImportFailed)
		if err != nil {
			return nil, err
		}
		message.Failure = &onboardingpb.RollbackPrimaryFailure_ImportFailed{ImportFailed: details}
	case primary.ConfigWriteFailed != nil:
		message.Failure = &onboardingpb.RollbackPrimaryFailure_ConfigWriteFailed{
			ConfigWriteFailed: &onboardingpb.ConfigWriteFailedDetails{
				SettingsPath: primary.ConfigWriteFailed.SettingsPath,
				Operation:    primary.ConfigWriteFailed.Operation,
				Cause:        primary.ConfigWriteFailed.Cause,
			},
		}
	case primary.Canceled != nil:
		phase, err := cancelPhaseToProto(primary.Canceled.Phase)
		if err != nil {
			return nil, err
		}
		message.Failure = &onboardingpb.RollbackPrimaryFailure_Canceled{
			Canceled: &onboardingpb.CanceledDetails{Phase: phase},
		}
	case primary.InternalFailure != nil:
		message.Failure = &onboardingpb.RollbackPrimaryFailure_InternalFailure{
			InternalFailure: &sharedpb.InternalFailureDetails{Cause: clonePointer(primary.InternalFailure.Cause)},
		}
	default:
		return nil, errors.New("onboarding rollback primary failure is required")
	}
	return message, nil
}

func rollbackFailedDetailsFromProto(details *onboardingpb.RollbackFailedDetails) (serverapi.OnboardingRollbackFailedDetails, error) {
	primary, err := rollbackPrimaryFromProto(details.Primary)
	if err != nil {
		return serverapi.OnboardingRollbackFailedDetails{}, err
	}
	return serverapi.OnboardingRollbackFailedDetails{
		Primary: primary,
		Rollback: serverapi.OnboardingRollbackFailureFact{
			Operation: details.Rollback.Operation,
			Cause:     details.Rollback.Cause,
		},
	}, nil
}

func rollbackPrimaryFromProto(message *onboardingpb.RollbackPrimaryFailure) (serverapi.OnboardingRollbackPrimaryFailure, error) {
	switch failure := message.Failure.(type) {
	case *onboardingpb.RollbackPrimaryFailure_InvalidRequest:
		details := invalidRequestDetailsFromProto(failure.InvalidRequest)
		return serverapi.OnboardingRollbackPrimaryFailure{InvalidRequest: &details}, nil
	case *onboardingpb.RollbackPrimaryFailure_ConfigAlreadyExists:
		details := serverapi.OnboardingConfigAlreadyExistsDetails{SettingsPath: failure.ConfigAlreadyExists.SettingsPath}
		return serverapi.OnboardingRollbackPrimaryFailure{ConfigAlreadyExists: &details}, nil
	case *onboardingpb.RollbackPrimaryFailure_ImportUnavailable:
		details, err := importUnavailableDetailsFromProto(failure.ImportUnavailable)
		return serverapi.OnboardingRollbackPrimaryFailure{ImportUnavailable: &details}, err
	case *onboardingpb.RollbackPrimaryFailure_ImportFailed:
		details, err := importFailedDetailsFromProto(failure.ImportFailed)
		return serverapi.OnboardingRollbackPrimaryFailure{ImportFailed: &details}, err
	case *onboardingpb.RollbackPrimaryFailure_ConfigWriteFailed:
		details := serverapi.OnboardingConfigWriteFailedDetails{
			SettingsPath: failure.ConfigWriteFailed.SettingsPath,
			Operation:    failure.ConfigWriteFailed.Operation,
			Cause:        failure.ConfigWriteFailed.Cause,
		}
		return serverapi.OnboardingRollbackPrimaryFailure{ConfigWriteFailed: &details}, nil
	case *onboardingpb.RollbackPrimaryFailure_Canceled:
		phase, err := cancelPhaseFromProto(failure.Canceled.Phase)
		details := serverapi.OnboardingCanceledDetails{Phase: phase}
		return serverapi.OnboardingRollbackPrimaryFailure{Canceled: &details}, err
	case *onboardingpb.RollbackPrimaryFailure_InternalFailure:
		details := serverapi.OnboardingInternalFailureDetails{Cause: clonePointer(failure.InternalFailure.Cause)}
		return serverapi.OnboardingRollbackPrimaryFailure{InternalFailure: &details}, nil
	default:
		return serverapi.OnboardingRollbackPrimaryFailure{}, fmt.Errorf("onboarding rollback primary failure %T is unsupported", failure)
	}
}

func onboardingDetailsTypeError(err *serverapi.OnboardingFinalizeError, details any) error {
	return fmt.Errorf("onboarding finalize error %q has unsupported details type %T", err.Code, details)
}

func serverNotReadyReasonToProto(reason serverapi.ServerNotReadyReason) (serverpb.ServerNotReadyReason, error) {
	switch reason {
	case serverapi.ServerNotReadyOnboardingRequired:
		return serverpb.ServerNotReadyReason_SERVER_NOT_READY_REASON_ONBOARDING_REQUIRED, nil
	case serverapi.ServerNotReadyActivationFailed:
		return serverpb.ServerNotReadyReason_SERVER_NOT_READY_REASON_ACTIVATION_FAILED, nil
	default:
		return 0, fmt.Errorf("server-not-ready reason %q is unsupported", reason)
	}
}

func serverNotReadyReasonFromProto(reason serverpb.ServerNotReadyReason) (serverapi.ServerNotReadyReason, error) {
	switch reason {
	case serverpb.ServerNotReadyReason_SERVER_NOT_READY_REASON_ONBOARDING_REQUIRED:
		return serverapi.ServerNotReadyOnboardingRequired, nil
	case serverpb.ServerNotReadyReason_SERVER_NOT_READY_REASON_ACTIVATION_FAILED:
		return serverapi.ServerNotReadyActivationFailed, nil
	default:
		return "", fmt.Errorf("protobuf server-not-ready reason %v is unsupported", reason)
	}
}

func importKindToProto(kind serverapi.OnboardingImportKind) (onboardingpb.ImportKind, error) {
	switch kind {
	case serverapi.OnboardingImportKindSkills:
		return onboardingpb.ImportKind_IMPORT_KIND_SKILLS, nil
	case serverapi.OnboardingImportKindCommands:
		return onboardingpb.ImportKind_IMPORT_KIND_COMMANDS, nil
	default:
		return 0, fmt.Errorf("onboarding import kind %q is unsupported", kind)
	}
}

func importKindFromProto(kind onboardingpb.ImportKind) (serverapi.OnboardingImportKind, error) {
	switch kind {
	case onboardingpb.ImportKind_IMPORT_KIND_SKILLS:
		return serverapi.OnboardingImportKindSkills, nil
	case onboardingpb.ImportKind_IMPORT_KIND_COMMANDS:
		return serverapi.OnboardingImportKindCommands, nil
	default:
		return "", fmt.Errorf("protobuf onboarding import kind %v is unsupported", kind)
	}
}

func importUnavailableReasonToProto(reason serverapi.OnboardingImportUnavailableReason) (onboardingpb.ImportUnavailableReason, error) {
	switch reason {
	case serverapi.OnboardingImportReasonNotDiscovered:
		return onboardingpb.ImportUnavailableReason_IMPORT_UNAVAILABLE_REASON_NOT_DISCOVERED, nil
	case serverapi.OnboardingImportReasonTargetExists:
		return onboardingpb.ImportUnavailableReason_IMPORT_UNAVAILABLE_REASON_TARGET_EXISTS, nil
	case serverapi.OnboardingImportReasonUnsupportedProvider:
		return onboardingpb.ImportUnavailableReason_IMPORT_UNAVAILABLE_REASON_UNSUPPORTED_PROVIDER, nil
	default:
		return 0, fmt.Errorf("onboarding import-unavailable reason %q is unsupported", reason)
	}
}

func importUnavailableReasonFromProto(reason onboardingpb.ImportUnavailableReason) (serverapi.OnboardingImportUnavailableReason, error) {
	switch reason {
	case onboardingpb.ImportUnavailableReason_IMPORT_UNAVAILABLE_REASON_NOT_DISCOVERED:
		return serverapi.OnboardingImportReasonNotDiscovered, nil
	case onboardingpb.ImportUnavailableReason_IMPORT_UNAVAILABLE_REASON_TARGET_EXISTS:
		return serverapi.OnboardingImportReasonTargetExists, nil
	case onboardingpb.ImportUnavailableReason_IMPORT_UNAVAILABLE_REASON_UNSUPPORTED_PROVIDER:
		return serverapi.OnboardingImportReasonUnsupportedProvider, nil
	default:
		return "", fmt.Errorf("protobuf onboarding import-unavailable reason %v is unsupported", reason)
	}
}

func importOperationToProto(operation serverapi.OnboardingImportOperation) (onboardingpb.ImportOperation, error) {
	switch operation {
	case serverapi.OnboardingImportOperationDiscover:
		return onboardingpb.ImportOperation_IMPORT_OPERATION_DISCOVER, nil
	case serverapi.OnboardingImportOperationPrepareTarget:
		return onboardingpb.ImportOperation_IMPORT_OPERATION_PREPARE_TARGET, nil
	case serverapi.OnboardingImportOperationCreateSymlink:
		return onboardingpb.ImportOperation_IMPORT_OPERATION_CREATE_SYMLINK, nil
	default:
		return 0, fmt.Errorf("onboarding import operation %q is unsupported", operation)
	}
}

func importOperationFromProto(operation onboardingpb.ImportOperation) (serverapi.OnboardingImportOperation, error) {
	switch operation {
	case onboardingpb.ImportOperation_IMPORT_OPERATION_DISCOVER:
		return serverapi.OnboardingImportOperationDiscover, nil
	case onboardingpb.ImportOperation_IMPORT_OPERATION_PREPARE_TARGET:
		return serverapi.OnboardingImportOperationPrepareTarget, nil
	case onboardingpb.ImportOperation_IMPORT_OPERATION_CREATE_SYMLINK:
		return serverapi.OnboardingImportOperationCreateSymlink, nil
	default:
		return "", fmt.Errorf("protobuf onboarding import operation %v is unsupported", operation)
	}
}

func cancelPhaseToProto(phase serverapi.OnboardingCancelPhase) (onboardingpb.CancelPhase, error) {
	switch phase {
	case serverapi.OnboardingCancelWaitingForLock:
		return onboardingpb.CancelPhase_CANCEL_PHASE_WAITING_FOR_LOCK, nil
	case serverapi.OnboardingCancelValidating:
		return onboardingpb.CancelPhase_CANCEL_PHASE_VALIDATING, nil
	case serverapi.OnboardingCancelDiscoveringImports:
		return onboardingpb.CancelPhase_CANCEL_PHASE_DISCOVERING_IMPORTS, nil
	case serverapi.OnboardingCancelImporting:
		return onboardingpb.CancelPhase_CANCEL_PHASE_IMPORTING, nil
	default:
		return 0, fmt.Errorf("onboarding cancel phase %q is unsupported", phase)
	}
}

func cancelPhaseFromProto(phase onboardingpb.CancelPhase) (serverapi.OnboardingCancelPhase, error) {
	switch phase {
	case onboardingpb.CancelPhase_CANCEL_PHASE_WAITING_FOR_LOCK:
		return serverapi.OnboardingCancelWaitingForLock, nil
	case onboardingpb.CancelPhase_CANCEL_PHASE_VALIDATING:
		return serverapi.OnboardingCancelValidating, nil
	case onboardingpb.CancelPhase_CANCEL_PHASE_DISCOVERING_IMPORTS:
		return serverapi.OnboardingCancelDiscoveringImports, nil
	case onboardingpb.CancelPhase_CANCEL_PHASE_IMPORTING:
		return serverapi.OnboardingCancelImporting, nil
	default:
		return "", fmt.Errorf("protobuf onboarding cancel phase %v is unsupported", phase)
	}
}

func uuidToString(value *uuid.UUID) *string {
	if value == nil {
		return nil
	}
	text := value.String()
	return &text
}

func uuidFromString(value *string) (*uuid.UUID, error) {
	if value == nil {
		return nil, nil
	}
	parsed, err := uuid.Parse(*value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
