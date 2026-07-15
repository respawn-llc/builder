package serverapi

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"core/shared/clientui"
	"core/shared/config"

	"github.com/google/uuid"
)

type RunPromptRequest struct {
	ClientRequestID string
	Intent          SessionLaunchIntent
	Prompt          string
	Timeout         time.Duration
	Overrides       RunPromptOverrides
}

func (r RunPromptRequest) Validate() error {
	if strings.TrimSpace(r.ClientRequestID) == "" {
		return errors.New("client_request_id is required")
	}
	if strings.TrimSpace(r.Prompt) == "" {
		return errors.New("prompt is required")
	}
	if err := r.Intent.Validate(); err != nil {
		return fmt.Errorf("intent: %w", err)
	}
	return r.Overrides.ValidateAgentRoleOverride()
}

type RunPromptOverrides struct {
	AgentRole           string
	Model               string
	ProviderOverride    string
	ThinkingLevel       string
	Theme               string
	ModelTimeoutSeconds int
	Tools               string
	OpenAIBaseURL       string
}

var ErrInvalidRunPromptAgentRole = errors.New("invalid agent role")

type RunPromptAgentRoleOverride struct {
	Present bool
	Default bool
	Role    string
}

func (o RunPromptOverrides) AgentRoleOverride() (RunPromptAgentRoleOverride, error) {
	raw := strings.TrimSpace(o.AgentRole)
	if raw == "" {
		return RunPromptAgentRoleOverride{}, nil
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

func (o RunPromptOverrides) HasAgentRoleOverride() bool {
	return strings.TrimSpace(o.AgentRole) != ""
}

func (o RunPromptOverrides) HasAny() bool {
	return strings.TrimSpace(o.AgentRole) != "" ||
		strings.TrimSpace(o.Model) != "" ||
		strings.TrimSpace(o.ProviderOverride) != "" ||
		strings.TrimSpace(o.ThinkingLevel) != "" ||
		strings.TrimSpace(o.Theme) != "" ||
		o.ModelTimeoutSeconds > 0 ||
		strings.TrimSpace(o.Tools) != "" ||
		strings.TrimSpace(o.OpenAIBaseURL) != ""
}

func (o RunPromptOverrides) HasConfigOverrides() bool {
	return strings.TrimSpace(o.Model) != "" ||
		strings.TrimSpace(o.ProviderOverride) != "" ||
		strings.TrimSpace(o.ThinkingLevel) != "" ||
		strings.TrimSpace(o.Theme) != "" ||
		o.ModelTimeoutSeconds > 0 ||
		strings.TrimSpace(o.Tools) != "" ||
		strings.TrimSpace(o.OpenAIBaseURL) != ""
}

func (o RunPromptOverrides) NeedsAuthState() bool {
	role, err := o.AgentRoleOverride()
	return err == nil && role.Present && !role.Default
}

type RunPromptResponse struct {
	SessionID   string
	SessionName string
	Result      string
	Duration    time.Duration
	Warnings    []string
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
