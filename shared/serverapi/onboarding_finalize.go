package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"core/shared/protocol"

	"github.com/google/uuid"
)

type OnboardingImportMode string
type OnboardingImportKind string
type OnboardingFinalizeErrorCode string
type OnboardingCancelPhase string
type OnboardingImportUnavailableReason string
type OnboardingImportOperation string
type ServerNotReadyReason string

const (
	OnboardingImportModeNone          OnboardingImportMode = "none"
	OnboardingImportModeSymlinkSource OnboardingImportMode = "symlink_source"

	OnboardingImportKindSkills   OnboardingImportKind = "skills"
	OnboardingImportKindCommands OnboardingImportKind = "commands"

	OnboardingFinalizeInvalidRequest          OnboardingFinalizeErrorCode       = "invalid_request"
	OnboardingFinalizeConfigAlreadyExists     OnboardingFinalizeErrorCode       = "config_already_exists"
	OnboardingFinalizeImportUnavailable       OnboardingFinalizeErrorCode       = "import_unavailable"
	OnboardingFinalizeImportFailed            OnboardingFinalizeErrorCode       = "import_failed"
	OnboardingFinalizeConfigWriteFailed       OnboardingFinalizeErrorCode       = "config_write_failed"
	OnboardingFinalizeRollbackFailed          OnboardingFinalizeErrorCode       = "rollback_failed"
	OnboardingFinalizeCanceled                OnboardingFinalizeErrorCode       = "canceled"
	OnboardingCancelWaitingForLock            OnboardingCancelPhase             = "waiting_for_lock"
	OnboardingCancelValidating                OnboardingCancelPhase             = "validating"
	OnboardingCancelDiscoveringImports        OnboardingCancelPhase             = "discovering_imports"
	OnboardingCancelImporting                 OnboardingCancelPhase             = "importing"
	OnboardingImportReasonNotDiscovered       OnboardingImportUnavailableReason = "not_discovered"
	OnboardingImportReasonTargetExists        OnboardingImportUnavailableReason = "target_exists"
	OnboardingImportReasonUnsupportedProvider OnboardingImportUnavailableReason = "unsupported_provider"
	OnboardingImportOperationDiscover         OnboardingImportOperation         = "discover"
	OnboardingImportOperationPrepareTarget    OnboardingImportOperation         = "prepare_target"
	OnboardingImportOperationCreateSymlink    OnboardingImportOperation         = "create_symlink"

	ServerNotReadyOnboardingRequired ServerNotReadyReason = "onboarding_required"
	ServerNotReadyActivationFailed   ServerNotReadyReason = "activation_failed"
)

type OnboardingFinalizeFieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

type OnboardingInvalidRequestDetails struct {
	FieldErrors []OnboardingFinalizeFieldError `json:"field_errors"`
}

type OnboardingConfigAlreadyExistsDetails struct {
	SettingsPath string `json:"settings_path"`
}

type OnboardingImportUnavailableDetails struct {
	ImportKind       OnboardingImportKind              `json:"import_kind"`
	Mode             OnboardingImportMode              `json:"mode"`
	ProviderUUID     *uuid.UUID                        `json:"provider_uuid,omitempty"`
	ImportProviderID *string                           `json:"import_provider_id,omitempty"`
	SourceRootPath   *string                           `json:"source_root_path,omitempty"`
	ReasonCode       OnboardingImportUnavailableReason `json:"reason_code"`
}

type OnboardingImportFailedDetails struct {
	ImportKind       OnboardingImportKind      `json:"import_kind"`
	ProviderUUID     *uuid.UUID                `json:"provider_uuid,omitempty"`
	ImportProviderID *string                   `json:"import_provider_id,omitempty"`
	SourceRootPath   *string                   `json:"source_root_path,omitempty"`
	Operation        OnboardingImportOperation `json:"operation"`
	Cause            string                    `json:"cause"`
}

type OnboardingConfigWriteFailedDetails struct {
	SettingsPath string `json:"settings_path"`
	Operation    string `json:"operation"`
	Cause        string `json:"cause"`
}

type OnboardingInternalFailureDetails struct {
	Cause *string
}

type OnboardingRollbackPrimaryFailure struct {
	InvalidRequest      *OnboardingInvalidRequestDetails
	ConfigAlreadyExists *OnboardingConfigAlreadyExistsDetails
	ImportUnavailable   *OnboardingImportUnavailableDetails
	ImportFailed        *OnboardingImportFailedDetails
	ConfigWriteFailed   *OnboardingConfigWriteFailedDetails
	Canceled            *OnboardingCanceledDetails
	InternalFailure     *OnboardingInternalFailureDetails
}

type OnboardingRollbackFailureFact struct {
	Operation string
	Cause     string
}

type OnboardingRollbackFailedDetails struct {
	Primary  OnboardingRollbackPrimaryFailure
	Rollback OnboardingRollbackFailureFact
}

type OnboardingCanceledDetails struct {
	Phase OnboardingCancelPhase `json:"phase"`
}

type ServerNotReadyEnvelope struct {
	Type    string               `json:"type"`
	Reason  ServerNotReadyReason `json:"reason"`
	Details any                  `json:"details,omitempty"`
}

type ServerNotReadyDetails struct {
	OnboardingCompleted bool    `json:"onboarding_completed,omitempty"`
	SettingsPath        *string `json:"settings_path,omitempty"`
	Diagnostic          *string `json:"diagnostic,omitempty"`
}

type OnboardingFinalizeError struct {
	Code    OnboardingFinalizeErrorCode
	Details any
	cause   error
}

var (
	ErrOnboardingFinalizeInvalidRequest      = &onboardingFinalizeCodeError{code: OnboardingFinalizeInvalidRequest}
	ErrOnboardingFinalizeConfigAlreadyExists = &onboardingFinalizeCodeError{code: OnboardingFinalizeConfigAlreadyExists}
	ErrOnboardingFinalizeImportUnavailable   = &onboardingFinalizeCodeError{code: OnboardingFinalizeImportUnavailable}
	ErrOnboardingFinalizeImportFailed        = &onboardingFinalizeCodeError{code: OnboardingFinalizeImportFailed}
	ErrOnboardingFinalizeConfigWriteFailed   = &onboardingFinalizeCodeError{code: OnboardingFinalizeConfigWriteFailed}
	ErrOnboardingFinalizeRollbackFailed      = &onboardingFinalizeCodeError{code: OnboardingFinalizeRollbackFailed}
	ErrOnboardingFinalizeCanceled            = &onboardingFinalizeCodeError{code: OnboardingFinalizeCanceled}
	ErrServerNotReadyOnboardingRequired      = &serverNotReadyReasonError{reason: ServerNotReadyOnboardingRequired}
	ErrServerNotReadyActivationFailed        = &serverNotReadyReasonError{reason: ServerNotReadyActivationFailed}
)

type onboardingFinalizeCodeError struct {
	code OnboardingFinalizeErrorCode
}

func (e *onboardingFinalizeCodeError) Error() string {
	return "onboarding finalize failed: " + string(e.code)
}

type serverNotReadyReasonError struct {
	reason ServerNotReadyReason
}

func (e *serverNotReadyReasonError) Error() string {
	return "server not ready: " + string(e.reason)
}

func (e *OnboardingFinalizeError) Error() string {
	if e == nil {
		return "onboarding finalize failed"
	}
	code := strings.TrimSpace(string(e.Code))
	if code == "" {
		code = "unknown"
	}
	return "onboarding finalize failed: " + code
}

func (e *OnboardingFinalizeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *OnboardingFinalizeError) Is(target error) bool {
	codeTarget, ok := target.(*onboardingFinalizeCodeError)
	return ok && e != nil && e.Code == codeTarget.code
}

type ServerNotReadyError struct {
	Reason  ServerNotReadyReason
	Details any
	cause   error
}

func (e *ServerNotReadyError) Error() string {
	if e == nil {
		return "server not ready"
	}
	reason := strings.TrimSpace(string(e.Reason))
	if reason == "" {
		reason = "unknown"
	}
	return "server not ready: " + reason
}

func (e *ServerNotReadyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *ServerNotReadyError) Is(target error) bool {
	reasonTarget, ok := target.(*serverNotReadyReasonError)
	return ok && e != nil && e.Reason == reasonTarget.reason
}

func (e *ServerNotReadyError) RPCErrorCode() int { return protocol.ErrCodeServerNotReady }
func (e *ServerNotReadyError) RPCErrorData() json.RawMessage {
	return marshalRPCErrorData(ServerNotReadyEnvelope{Type: "server_not_ready", Reason: e.Reason, Details: e.Details})
}

func marshalRPCErrorData(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func NewOnboardingFinalizeError(code OnboardingFinalizeErrorCode, details any, cause error) *OnboardingFinalizeError {
	return &OnboardingFinalizeError{Code: code, Details: details, cause: cause}
}

func NewOnboardingCanceledError(phase OnboardingCancelPhase) *OnboardingFinalizeError {
	return &OnboardingFinalizeError{Code: OnboardingFinalizeCanceled, Details: OnboardingCanceledDetails{Phase: phase}, cause: context.Canceled}
}

func NewServerNotReadyError(reason ServerNotReadyReason, details any, cause error) *ServerNotReadyError {
	return &ServerNotReadyError{Reason: reason, Details: details, cause: cause}
}

func DecodeServerNotReadyError(data json.RawMessage, message string) error {
	var envelope struct {
		Type    string               `json:"type"`
		Reason  ServerNotReadyReason `json:"reason"`
		Details json.RawMessage      `json:"details,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type != "server_not_ready" || envelope.Reason == "" {
		return errors.New(strings.TrimSpace(message))
	}
	return &ServerNotReadyError{Reason: envelope.Reason, Details: decodeJSONDetails[ServerNotReadyDetails](envelope.Details)}
}

func decodeJSONDetails[T any](data json.RawMessage) any {
	if len(data) == 0 {
		return nil
	}
	var value *T
	if err := json.Unmarshal(data, &value); err != nil {
		var fallback map[string]any
		if fallbackErr := json.Unmarshal(data, &fallback); fallbackErr == nil {
			return fallback
		}
		return nil
	}
	if value == nil {
		return nil
	}
	return *value
}
