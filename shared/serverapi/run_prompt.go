package serverapi

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protocol"

	"github.com/google/uuid"
)

type RunPromptRequest struct {
	Intent          SessionLaunchIntent `json:"intent"`
	CallerSessionID *string             `json:"caller_session_id,omitempty"`
	Prompt          string              `json:"prompt"`
	Timeout         time.Duration       `json:"timeout"`
	Overrides       RunPromptOverrides  `json:"overrides,omitempty"`
}

func (r *RunPromptRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		Intent          SessionLaunchIntent `json:"intent"`
		CallerSessionID *string             `json:"caller_session_id"`
		Prompt          string              `json:"prompt"`
		Timeout         time.Duration       `json:"timeout"`
		Overrides       RunPromptOverrides  `json:"overrides"`
	}
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	request := RunPromptRequest{
		Intent:          decoded.Intent,
		CallerSessionID: decoded.CallerSessionID,
		Prompt:          decoded.Prompt,
		Timeout:         decoded.Timeout,
		Overrides:       decoded.Overrides,
	}
	if err := request.Validate(); err != nil {
		return err
	}
	*r = request
	return nil
}

type OptionalStringKey struct {
	Present bool
	Value   string
}

type RunPromptOverridesKey struct {
	AgentRole           OptionalStringKey
	Model               string
	ProviderOverride    string
	ThinkingLevel       OptionalStringKey
	Theme               OptionalStringKey
	ModelTimeoutSeconds int
	Tools               string
	OpenAIBaseURL       string
}

func (o RunPromptOverrides) CanonicalKey() (RunPromptOverridesKey, error) {
	role, err := o.AgentRoleOverride()
	if err != nil {
		return RunPromptOverridesKey{}, err
	}
	key := RunPromptOverridesKey{
		Model:               strings.TrimSpace(o.Model),
		ProviderOverride:    strings.TrimSpace(o.ProviderOverride),
		ThinkingLevel:       CanonicalOptionalString(o.ThinkingLevel),
		Theme:               CanonicalOptionalString(o.Theme),
		ModelTimeoutSeconds: o.ModelTimeoutSeconds,
		Tools:               strings.TrimSpace(o.Tools),
		OpenAIBaseURL:       strings.TrimSpace(o.OpenAIBaseURL),
	}
	if role.Present {
		value := role.Role
		if role.Default {
			value = config.DefaultSubagentRole
		}
		key.AgentRole = OptionalStringKey{Present: true, Value: value}
	}
	return key, nil
}

func CanonicalOptionalString(value *string) OptionalStringKey {
	if value == nil {
		return OptionalStringKey{}
	}
	return OptionalStringKey{Present: true, Value: strings.TrimSpace(*value)}
}

func ValidateOptionalIdentifier(field string, value *string) error {
	if value == nil {
		return nil
	}
	if strings.TrimSpace(*value) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	return nil
}

func (r RunPromptRequest) Validate() error {
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("prompt is required")
	}
	if err := r.Intent.Validate(); err != nil {
		return fmt.Errorf("intent: %w", err)
	}
	if err := ValidateOptionalIdentifier("caller_session_id", r.CallerSessionID); err != nil {
		return err
	}
	return r.Overrides.Validate()
}

type RunPromptOverrides struct {
	AgentRole           *string `json:"agent_role,omitempty"`
	Model               string  `json:"model"`
	ProviderOverride    string  `json:"provider_override"`
	ThinkingLevel       *string `json:"thinking_level,omitempty"`
	Theme               *string `json:"theme,omitempty"`
	ModelTimeoutSeconds int     `json:"model_timeout_seconds"`
	Tools               string  `json:"tools"`
	OpenAIBaseURL       string  `json:"openai_base_url"`
}

var ErrInvalidRunPromptAgentRole = errors.New("invalid agent role")

type RunPromptAgentRoleOverride struct {
	Present bool
	Default bool
	Role    string
}

func (o RunPromptOverrides) AgentRoleOverride() (RunPromptAgentRoleOverride, error) {
	if o.AgentRole == nil {
		return RunPromptAgentRoleOverride{}, nil
	}
	raw := strings.TrimSpace(*o.AgentRole)
	if raw == "" {
		return RunPromptAgentRoleOverride{}, fmt.Errorf("%w %s", ErrInvalidRunPromptAgentRole, strconv.Quote(*o.AgentRole))
	}
	normalized := strings.ToLower(raw)
	if normalized == config.DefaultSubagentRole {
		return RunPromptAgentRoleOverride{Present: true, Default: true}, nil
	}
	if config.IsReservedSubagentRoleName(normalized) {
		return RunPromptAgentRoleOverride{}, fmt.Errorf("%w %s", ErrInvalidRunPromptAgentRole, strconv.Quote(raw))
	}
	roleName := config.NormalizeSubagentSelector(raw)
	if roleName == "" {
		return RunPromptAgentRoleOverride{}, fmt.Errorf("%w %s", ErrInvalidRunPromptAgentRole, strconv.Quote(raw))
	}
	return RunPromptAgentRoleOverride{Present: true, Role: roleName}, nil
}

func (o RunPromptOverrides) ValidateAgentRoleOverride() error {
	_, err := o.AgentRoleOverride()
	return err
}

func (o RunPromptOverrides) Validate() error {
	if err := o.ValidateAgentRoleOverride(); err != nil {
		return err
	}
	if err := ValidateOptionalIdentifier("thinking_level", o.ThinkingLevel); err != nil {
		return err
	}
	return ValidateOptionalIdentifier("theme", o.Theme)
}

func (o RunPromptOverrides) HasAgentRoleOverride() bool {
	return o.AgentRole != nil
}

func (o RunPromptOverrides) HasAny() bool {
	return o.AgentRole != nil ||
		strings.TrimSpace(o.Model) != "" ||
		strings.TrimSpace(o.ProviderOverride) != "" ||
		o.ThinkingLevel != nil ||
		o.Theme != nil ||
		o.ModelTimeoutSeconds > 0 ||
		strings.TrimSpace(o.Tools) != "" ||
		strings.TrimSpace(o.OpenAIBaseURL) != ""
}

func (o RunPromptOverrides) HasConfigOverrides() bool {
	return strings.TrimSpace(o.Model) != "" ||
		strings.TrimSpace(o.ProviderOverride) != "" ||
		o.ThinkingLevel != nil ||
		o.Theme != nil ||
		o.ModelTimeoutSeconds > 0 ||
		strings.TrimSpace(o.Tools) != "" ||
		strings.TrimSpace(o.OpenAIBaseURL) != ""
}

func (o RunPromptOverrides) NeedsAuthState() bool {
	role, err := o.AgentRoleOverride()
	return err == nil && role.Present && !role.Default
}

type RunPromptResponse struct {
	SessionID                 string
	SessionName               *string
	Result                    string
	Duration                  time.Duration
	Warnings                  []string
	WorkflowResumeDiagnostics []RunPromptWorkflowResumeDiagnostic
}

type RunPromptWorkflowResumeDiagnostic struct {
	TaskID              string
	NodeID              string
	TransitionBranchKey *string
	Cause               string
}

type RunPromptProgress struct {
	Kind             RunPromptProgressKind
	SessionStarted   *RunPromptSessionStarted  `json:",omitempty"`
	AssistantMessage *RunPromptVisibleResponse `json:",omitempty"`
	SteeredMessage   *RunPromptSteeredMessage  `json:",omitempty"`
	Failure          *RunPromptFailure         `json:",omitempty"`
}

type RunPromptProgressKind string

const (
	RunPromptProgressKindSessionStarted    RunPromptProgressKind = "session_started"
	RunPromptProgressKindAssistantMessage  RunPromptProgressKind = "assistant_message"
	RunPromptProgressKindSteeredMessage    RunPromptProgressKind = "steered_message"
	RunPromptProgressKindCompactionStarted RunPromptProgressKind = "compaction_started"
	RunPromptProgressKindCompactionFailed  RunPromptProgressKind = "compaction_failed"
	RunPromptProgressKindRunLoggingFailed  RunPromptProgressKind = "run_logging_failed"
	RunPromptProgressKindRunCleanupFailed  RunPromptProgressKind = "run_cleanup_failed"
)

type RunPromptSessionStarted struct {
	SessionID uuid.UUID
}

type RunPromptVisibleResponse struct {
	Phase   clientui.MessagePhase
	Content string
}

type RunPromptSteeredMessage struct {
	Content string
}

type RunPromptFailure struct {
	Error *string `json:",omitempty"`
}

type RunPromptProgressSink interface {
	PublishRunPromptProgress(RunPromptProgress)
}

type RunPromptProgressFunc func(RunPromptProgress)

func (fn RunPromptProgressFunc) PublishRunPromptProgress(progress RunPromptProgress) {
	if fn != nil {
		fn(progress)
	}
}

func (p RunPromptProgress) Validate() error {
	switch p.Kind {
	case RunPromptProgressKindSessionStarted:
		if p.SessionStarted == nil {
			return errors.New("session_started payload is required")
		}
		if p.SessionStarted.SessionID == uuid.Nil || p.SessionStarted.SessionID.Version() != 4 {
			return errors.New("session_started.session_id must be a UUIDv4")
		}
	case RunPromptProgressKindAssistantMessage:
		if p.AssistantMessage == nil {
			return errors.New("assistant_message payload is required")
		}
		switch p.AssistantMessage.Phase {
		case clientui.MessagePhaseCommentary, clientui.MessagePhaseFinal:
		default:
			return errors.New("assistant_message.phase is invalid")
		}
		if strings.TrimSpace(p.AssistantMessage.Content) == "" {
			return errors.New("assistant_message.content is required")
		}
	case RunPromptProgressKindSteeredMessage:
		if p.SteeredMessage == nil || strings.TrimSpace(p.SteeredMessage.Content) == "" {
			return errors.New("steered_message.content is required")
		}
	case RunPromptProgressKindCompactionStarted:
	case RunPromptProgressKindCompactionFailed, RunPromptProgressKindRunLoggingFailed, RunPromptProgressKindRunCleanupFailed:
		if p.Failure == nil {
			return errors.New("failure payload is required")
		}
		if p.Failure.Error != nil && strings.TrimSpace(*p.Failure.Error) == "" {
			return errors.New("failure.error must not be blank")
		}
	default:
		return fmt.Errorf("invalid run prompt progress kind %q", p.Kind)
	}
	return nil
}
