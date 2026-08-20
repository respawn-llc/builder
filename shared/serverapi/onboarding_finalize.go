package serverapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
	"core/shared/toolspec"

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
	Theme               *OnboardingTheme               `json:"theme,omitempty"`
	MainProvider        *OnboardingProviderChoice      `json:"main_provider,omitempty"`
	Model               *OnboardingModelChoice         `json:"model,omitempty"`
	ContextWindow       *OnboardingContextWindowChoice `json:"context_window,omitempty"`
	Thinking            *OnboardingThinkingChoice      `json:"thinking,omitempty"`
	Verbosity           *OnboardingVerbosity           `json:"verbosity,omitempty"`
	ModelTimeoutSeconds *int                           `json:"model_timeout_seconds,omitempty"`
	AskQuestion         *bool                          `json:"ask_question,omitempty"`
	ToolOverrides       []OnboardingToolOverride       `json:"tool_overrides,omitempty"`
	Supervisor          *OnboardingSupervisorChoice    `json:"supervisor,omitempty"`
	Compaction          *OnboardingCompactionMode      `json:"compaction,omitempty"`
	SkillsImport        *OnboardingImportSelection     `json:"skills_import,omitempty"`
	CommandsImport      *OnboardingImportSelection     `json:"commands_import,omitempty"`
	DisabledSkillNames  []string                       `json:"disabled_skill_names,omitempty"`
}

type OnboardingModelChoice struct {
	Kind    OnboardingModelKind `json:"kind"`
	ModelID string              `json:"model_id,omitempty"`
	Alias   string              `json:"alias,omitempty"`
}

type OnboardingProviderChoice struct {
	ProviderOverride *string `json:"provider_override,omitempty"`
	OpenAIBaseURL    *string `json:"openai_base_url,omitempty"`
}

type OnboardingToolOverride struct {
	ID      toolspec.ID `json:"id"`
	Enabled bool        `json:"enabled"`
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
	Mode             OnboardingImportMode `json:"mode"`
	ProviderUUID     *uuid.UUID           `json:"provider_uuid,omitempty"`
	ImportProviderID *string              `json:"import_provider_id,omitempty"`
	SourceRootPath   *string              `json:"source_root_path,omitempty"`
}

type OnboardingFinalizeResponse struct {
	Completed    bool   `json:"completed"`
	SettingsPath string `json:"settings_path"`
}

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

func ValidateOnboardingFinalizeRequest(req OnboardingFinalizeRequest) error {
	var fieldErrors []OnboardingFinalizeFieldError
	add := func(field, code string) {
		fieldErrors = append(fieldErrors, OnboardingFinalizeFieldError{Field: field, Code: code})
	}
	if req.Theme != nil && !oneOf(string(*req.Theme), "auto", "light", "dark") {
		add("theme", "unsupported_value")
	}
	validateProviderChoice(req.MainProvider, "main_provider", add)
	validateModelChoice(req.Model, "model", add)
	validateContextWindowChoice(req.ContextWindow, "context_window", add)
	validateThinkingChoice(req.Thinking, "thinking", add)
	if req.Verbosity != nil && !oneOf(string(*req.Verbosity), "low", "medium", "high") {
		add("verbosity", "unsupported_value")
	}
	if req.ModelTimeoutSeconds != nil && *req.ModelTimeoutSeconds <= 0 {
		add("model_timeout_seconds", "positive_required")
	}
	validateToolOverrides(req.ToolOverrides, add)
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

func validateToolOverrides(overrides []OnboardingToolOverride, add func(string, string)) {
	if overrides == nil {
		return
	}
	if len(overrides) == 0 {
		add("tool_overrides", "choice_required")
		return
	}
	seen := map[toolspec.ID]struct{}{}
	for index, override := range overrides {
		field := fmt.Sprintf("tool_overrides.%d.id", index)
		parsed, known := toolspec.ParseID(string(override.ID))
		if !known || parsed != override.ID {
			add(field, "unsupported_value")
			continue
		}
		if override.ID == toolspec.ToolAskQuestion {
			add(field, "forbidden")
			continue
		}
		if _, duplicate := seen[override.ID]; duplicate {
			add(field, "duplicate")
			continue
		}
		seen[override.ID] = struct{}{}
	}
}

func validateProviderChoice(choice *OnboardingProviderChoice, field string, add func(string, string)) {
	if choice == nil {
		return
	}
	if choice.ProviderOverride == nil && choice.OpenAIBaseURL == nil {
		add(field, "choice_required")
		return
	}
	provider := ""
	if choice.ProviderOverride != nil {
		provider = strings.ToLower(strings.TrimSpace(*choice.ProviderOverride))
		switch provider {
		case "":
			add(field+".provider_override", "required")
		case "openai", "anthropic":
		default:
			add(field+".provider_override", "unsupported_value")
		}
	}
	if choice.OpenAIBaseURL != nil {
		if strings.TrimSpace(*choice.OpenAIBaseURL) == "" {
			add(field+".openai_base_url", "required")
		}
		if provider != "" && provider != "openai" {
			add(field+".openai_base_url", "conflicts_with_provider_override")
		}
	}
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
		if selection.ProviderUUID != nil {
			add(field+".provider_uuid", "forbidden")
		}
		if selection.ImportProviderID != nil {
			add(field+".import_provider_id", "forbidden")
		}
		if selection.SourceRootPath != nil {
			add(field+".source_root_path", "forbidden")
		}
	case OnboardingImportModeSymlinkSource:
		hasUUID := selection.ProviderUUID != nil
		hasRef := selection.ImportProviderID != nil || selection.SourceRootPath != nil
		if hasUUID && hasRef {
			add(field+".provider_uuid", "conflicts_with_choice_ref")
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
		if selection.ImportProviderID == nil || strings.TrimSpace(*selection.ImportProviderID) == "" {
			add(field+".import_provider_id", "required")
		}
		if selection.SourceRootPath == nil || strings.TrimSpace(*selection.SourceRootPath) == "" {
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
