package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"core/shared/protocol"

	"github.com/google/uuid"
)

type OnboardingTheme string
type OnboardingModelKind string
type OnboardingContextWindowKind string
type OnboardingThinkingKind string
type OnboardingVerbosity string
type OnboardingSupervisorFrequency string
type OnboardingCompactionMode string
type OnboardingImportMode string
type OnboardingImportKind string
type OnboardingFinalizeErrorCode string
type OnboardingCancelPhase string
type OnboardingImportUnavailableReason string
type OnboardingImportOperation string
type ServerNotReadyReason string

const (
	OnboardingThemeAuto  OnboardingTheme = "auto"
	OnboardingThemeLight OnboardingTheme = "light"
	OnboardingThemeDark  OnboardingTheme = "dark"

	OnboardingModelKnown  OnboardingModelKind = "known"
	OnboardingModelCustom OnboardingModelKind = "custom"

	OnboardingContextWindowDefault OnboardingContextWindowKind = "default"
	OnboardingContextWindowLarge   OnboardingContextWindowKind = "large"
	OnboardingContextWindowCustom  OnboardingContextWindowKind = "custom"

	OnboardingThinkingDefault  OnboardingThinkingKind = "default"
	OnboardingThinkingDisabled OnboardingThinkingKind = "disabled"
	OnboardingThinkingLevel    OnboardingThinkingKind = "level"
	OnboardingThinkingCustom   OnboardingThinkingKind = "custom"

	OnboardingVerbosityLow    OnboardingVerbosity = "low"
	OnboardingVerbosityMedium OnboardingVerbosity = "medium"
	OnboardingVerbosityHigh   OnboardingVerbosity = "high"

	OnboardingSupervisorOff   OnboardingSupervisorFrequency = "off"
	OnboardingSupervisorEdits OnboardingSupervisorFrequency = "edits"
	OnboardingSupervisorAll   OnboardingSupervisorFrequency = "all"

	OnboardingCompactionNative OnboardingCompactionMode = "native"
	OnboardingCompactionLocal  OnboardingCompactionMode = "local"
	OnboardingCompactionNone   OnboardingCompactionMode = "none"

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

type OnboardingFinalizeRequest struct {
	Theme              *OnboardingTheme               `json:"theme,omitempty"`
	Model              *OnboardingModelChoice         `json:"model,omitempty"`
	ContextWindow      *OnboardingContextWindowChoice `json:"context_window,omitempty"`
	Thinking           *OnboardingThinkingChoice      `json:"thinking,omitempty"`
	Verbosity          *OnboardingVerbosity           `json:"verbosity,omitempty"`
	AskQuestion        *bool                          `json:"ask_question,omitempty"`
	Supervisor         *OnboardingSupervisorChoice    `json:"supervisor,omitempty"`
	Compaction         *OnboardingCompactionMode      `json:"compaction,omitempty"`
	SkillsImport       *OnboardingImportSelection     `json:"skills_import,omitempty"`
	CommandsImport     *OnboardingImportSelection     `json:"commands_import,omitempty"`
	DisabledSkillNames []string                       `json:"disabled_skill_names,omitempty"`
	unknownFields      []string
}

type OnboardingModelChoice struct {
	Kind    OnboardingModelKind `json:"kind"`
	ModelID string              `json:"model_id,omitempty"`
	Alias   string              `json:"alias,omitempty"`
}

type OnboardingContextWindowChoice struct {
	Kind   OnboardingContextWindowKind `json:"kind"`
	Tokens int                         `json:"tokens,omitempty"`
}

type OnboardingThinkingChoice struct {
	Kind  OnboardingThinkingKind `json:"kind"`
	Level string                 `json:"level,omitempty"`
	Value string                 `json:"value,omitempty"`
}

type OnboardingSupervisorChoice struct {
	Frequency OnboardingSupervisorFrequency `json:"frequency"`
	Model     *OnboardingModelChoice        `json:"model,omitempty"`
	Thinking  *OnboardingThinkingChoice     `json:"thinking,omitempty"`
}

type OnboardingImportSelection struct {
	Mode              OnboardingImportMode `json:"mode"`
	ProviderUUID      *uuid.UUID           `json:"provider_uuid,omitempty"`
	ImportProviderID  *string              `json:"import_provider_id,omitempty"`
	SourceRootPath    *string              `json:"source_root_path,omitempty"`
	providerRaw       string
	providerBad       bool
	importProviderBad bool
	sourceRootBad     bool
}

func (r *OnboardingFinalizeRequest) UnmarshalJSON(data []byte) error {
	type alias OnboardingFinalizeRequest
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	allowed := map[string]bool{
		"theme": true, "model": true, "context_window": true, "thinking": true, "verbosity": true,
		"ask_question": true, "supervisor": true, "compaction": true, "skills_import": true,
		"commands_import": true, "disabled_skill_names": true,
	}
	for key := range raw {
		if !allowed[key] {
			decoded.unknownFields = append(decoded.unknownFields, key)
		}
	}
	sort.Strings(decoded.unknownFields)
	*r = OnboardingFinalizeRequest(decoded)
	return nil
}

func (s *OnboardingImportSelection) UnmarshalJSON(data []byte) error {
	var raw struct {
		Mode             OnboardingImportMode `json:"mode"`
		ProviderUUID     *json.RawMessage     `json:"provider_uuid"`
		ImportProviderID *json.RawMessage     `json:"import_provider_id"`
		SourceRootPath   *json.RawMessage     `json:"source_root_path"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.Mode = raw.Mode
	s.ProviderUUID = nil
	s.ImportProviderID = nil
	s.SourceRootPath = nil
	s.providerRaw = ""
	s.providerBad = false
	s.importProviderBad = false
	s.sourceRootBad = false
	if raw.ProviderUUID != nil {
		var value *string
		if err := json.Unmarshal(*raw.ProviderUUID, &value); err != nil {
			s.providerBad = true
		} else if value != nil {
			s.providerRaw = strings.TrimSpace(*value)
			parsed, err := uuid.Parse(s.providerRaw)
			if err != nil {
				s.providerBad = true
			} else {
				s.ProviderUUID = &parsed
			}
		}
	}
	if raw.ImportProviderID != nil {
		value, ok := importSelectionString(raw.ImportProviderID)
		if !ok {
			s.importProviderBad = true
		} else {
			s.ImportProviderID = value
		}
	}
	if raw.SourceRootPath != nil {
		value, ok := importSelectionString(raw.SourceRootPath)
		if !ok {
			s.sourceRootBad = true
		} else {
			s.SourceRootPath = value
		}
	}
	return nil
}

func importSelectionString(raw *json.RawMessage) (*string, bool) {
	var value *string
	if err := json.Unmarshal(*raw, &value); err != nil {
		return nil, false
	}
	if value == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, false
	}
	return &trimmed, true
}

func (s OnboardingImportSelection) MarshalJSON() ([]byte, error) {
	type wire struct {
		Mode             OnboardingImportMode `json:"mode"`
		ProviderUUID     *uuid.UUID           `json:"provider_uuid,omitempty"`
		ImportProviderID *string              `json:"import_provider_id,omitempty"`
		SourceRootPath   *string              `json:"source_root_path,omitempty"`
	}
	return json.Marshal(wire{Mode: s.Mode, ProviderUUID: s.ProviderUUID, ImportProviderID: s.ImportProviderID, SourceRootPath: s.SourceRootPath})
}

type OnboardingFinalizeResponse struct {
	Completed    bool   `json:"completed"`
	SettingsPath string `json:"settings_path"`
}

type OnboardingFinalizeFieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

type OnboardingFinalizeErrorEnvelope struct {
	Type    string                      `json:"type"`
	Code    OnboardingFinalizeErrorCode `json:"code"`
	Details any                         `json:"details,omitempty"`
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

type OnboardingRollbackFailedDetails struct {
	Primary  any `json:"primary"`
	Rollback any `json:"rollback"`
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

func (e *OnboardingFinalizeError) RPCErrorCode() int { return protocol.ErrCodeOnboardingFinalizeFailed }
func (e *OnboardingFinalizeError) RPCErrorData() json.RawMessage {
	return marshalRPCErrorData(OnboardingFinalizeErrorEnvelope{Type: "onboarding_finalize_error", Code: e.Code, Details: e.Details})
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

func DecodeOnboardingFinalizeError(data json.RawMessage, message string) error {
	var envelope struct {
		Type    string                      `json:"type"`
		Code    OnboardingFinalizeErrorCode `json:"code"`
		Details json.RawMessage             `json:"details,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type != "onboarding_finalize_error" || envelope.Code == "" {
		return errors.New(strings.TrimSpace(message))
	}
	err := &OnboardingFinalizeError{Code: envelope.Code, Details: decodeOnboardingFinalizeDetails(envelope.Code, envelope.Details)}
	if envelope.Code == OnboardingFinalizeCanceled {
		err.cause = context.Canceled
	}
	return err
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

func decodeOnboardingFinalizeDetails(code OnboardingFinalizeErrorCode, data json.RawMessage) any {
	switch code {
	case OnboardingFinalizeInvalidRequest:
		return decodeJSONDetails[OnboardingInvalidRequestDetails](data)
	case OnboardingFinalizeConfigAlreadyExists:
		return decodeJSONDetails[OnboardingConfigAlreadyExistsDetails](data)
	case OnboardingFinalizeImportUnavailable:
		return decodeJSONDetails[OnboardingImportUnavailableDetails](data)
	case OnboardingFinalizeImportFailed:
		return decodeJSONDetails[OnboardingImportFailedDetails](data)
	case OnboardingFinalizeConfigWriteFailed:
		return decodeJSONDetails[OnboardingConfigWriteFailedDetails](data)
	case OnboardingFinalizeRollbackFailed:
		return decodeJSONDetails[OnboardingRollbackFailedDetails](data)
	case OnboardingFinalizeCanceled:
		return decodeJSONDetails[OnboardingCanceledDetails](data)
	default:
		return decodeJSONDetails[map[string]any](data)
	}
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

func ValidateOnboardingFinalizeRequest(req OnboardingFinalizeRequest) error {
	var fieldErrors []OnboardingFinalizeFieldError
	add := func(field, code string) {
		fieldErrors = append(fieldErrors, OnboardingFinalizeFieldError{Field: field, Code: code})
	}
	for _, field := range req.unknownFields {
		add(field, "unknown_field")
	}
	if req.Theme != nil && !oneOf(string(*req.Theme), "auto", "light", "dark") {
		add("theme", "unsupported_value")
	}
	validateModelChoice(req.Model, "model", add)
	validateContextWindowChoice(req.ContextWindow, "context_window", add)
	validateThinkingChoice(req.Thinking, "thinking", add)
	if req.Verbosity != nil && !oneOf(string(*req.Verbosity), "low", "medium", "high") {
		add("verbosity", "unsupported_value")
	}
	if req.Supervisor != nil {
		if !oneOf(string(req.Supervisor.Frequency), "off", "edits", "all") {
			add("supervisor.frequency", "unsupported_value")
		}
		validateModelChoice(req.Supervisor.Model, "supervisor.model", add)
		validateThinkingChoice(req.Supervisor.Thinking, "supervisor.thinking", add)
	}
	if req.Compaction != nil && !oneOf(string(*req.Compaction), "native", "local", "none") {
		add("compaction", "unsupported_value")
	}
	validateImportSelection(req.SkillsImport, "skills_import", add)
	validateImportSelection(req.CommandsImport, "commands_import", add)
	for index, name := range req.DisabledSkillNames {
		if strings.TrimSpace(name) == "" {
			add(fmt.Sprintf("disabled_skill_names.%d", index), "required")
		}
	}
	if len(fieldErrors) > 0 {
		return NewOnboardingFinalizeError(OnboardingFinalizeInvalidRequest, OnboardingInvalidRequestDetails{FieldErrors: fieldErrors}, nil)
	}
	return nil
}

func validateModelChoice(choice *OnboardingModelChoice, field string, add func(string, string)) {
	if choice == nil {
		return
	}
	switch choice.Kind {
	case OnboardingModelKnown:
		if strings.TrimSpace(choice.ModelID) == "" {
			add(field+".model_id", "required")
		}
	case OnboardingModelCustom:
		if strings.TrimSpace(choice.Alias) == "" {
			add(field+".alias", "required")
		}
	default:
		add(field+".kind", "unsupported_value")
	}
}

func validateContextWindowChoice(choice *OnboardingContextWindowChoice, field string, add func(string, string)) {
	if choice == nil {
		return
	}
	switch choice.Kind {
	case OnboardingContextWindowDefault, OnboardingContextWindowLarge:
	case OnboardingContextWindowCustom:
		if choice.Tokens <= 0 {
			add(field+".tokens", "positive_required")
		}
	default:
		add(field+".kind", "unsupported_value")
	}
}

func validateThinkingChoice(choice *OnboardingThinkingChoice, field string, add func(string, string)) {
	if choice == nil {
		return
	}
	switch choice.Kind {
	case OnboardingThinkingDefault, OnboardingThinkingDisabled:
	case OnboardingThinkingLevel:
		if strings.TrimSpace(choice.Level) == "" {
			add(field+".level", "required")
		}
	case OnboardingThinkingCustom:
		if strings.TrimSpace(choice.Value) == "" {
			add(field+".value", "required")
		}
	default:
		add(field+".kind", "unsupported_value")
	}
}

func validateImportSelection(selection *OnboardingImportSelection, field string, add func(string, string)) {
	if selection == nil {
		return
	}
	switch selection.Mode {
	case OnboardingImportModeNone:
		if selection.ProviderUUID != nil || selection.providerBad {
			add(field+".provider_uuid", "forbidden")
		}
		if selection.ImportProviderID != nil || selection.importProviderBad {
			add(field+".import_provider_id", "forbidden")
		}
		if selection.SourceRootPath != nil || selection.sourceRootBad {
			add(field+".source_root_path", "forbidden")
		}
	case OnboardingImportModeSymlinkSource:
		hasUUID := selection.ProviderUUID != nil || selection.providerBad
		hasRef := selection.ImportProviderID != nil || selection.SourceRootPath != nil || selection.importProviderBad || selection.sourceRootBad
		if hasUUID && hasRef {
			add(field+".provider_uuid", "conflicts_with_choice_ref")
			break
		}
		if selection.providerBad {
			add(field+".provider_uuid", "uuid_v4_required")
			break
		}
		if selection.ProviderUUID != nil {
			if selection.ProviderUUID.Version() != 4 {
				add(field+".provider_uuid", "uuid_v4_required")
			}
			break
		}
		if !hasRef {
			add(field+".provider_uuid", "required")
			break
		}
		if selection.importProviderBad || selection.ImportProviderID == nil {
			add(field+".import_provider_id", "required")
		}
		if selection.sourceRootBad || selection.SourceRootPath == nil {
			add(field+".source_root_path", "required")
		}
	default:
		add(field+".mode", "unsupported_value")
	}
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
