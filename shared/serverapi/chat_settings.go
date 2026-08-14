package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
	"core/shared/runtimeids"
)

type ChatSettingsReadTargetKind string

const (
	ChatSettingsReadTargetLazy    ChatSettingsReadTargetKind = "lazy"
	ChatSettingsReadTargetSession ChatSettingsReadTargetKind = "session"
)

type ChatSettingsReadTarget struct {
	TargetKind  ChatSettingsReadTargetKind `json:"kind"`
	ProjectID   *string                    `json:"project_id,omitempty"`
	WorkspaceID *string                    `json:"workspace_id,omitempty"`
	Session     *runtimeids.SessionID      `json:"session_id,omitempty"`
}

func LazyChatSettingsTarget(projectID, workspaceID string) ChatSettingsReadTarget {
	return ChatSettingsReadTarget{
		TargetKind:  ChatSettingsReadTargetLazy,
		ProjectID:   &projectID,
		WorkspaceID: &workspaceID,
	}
}

func SessionChatSettingsTarget(sessionID runtimeids.SessionID) ChatSettingsReadTarget {
	return ChatSettingsReadTarget{TargetKind: ChatSettingsReadTargetSession, Session: &sessionID}
}

func (t ChatSettingsReadTarget) Validate() error {
	switch t.TargetKind {
	case ChatSettingsReadTargetLazy:
		if t.Session != nil {
			return errors.New("lazy Chat settings target cannot contain session_id")
		}
		if err := validateChatTargetID("project_id", t.ProjectID); err != nil {
			return err
		}
		return validateChatTargetID("workspace_id", t.WorkspaceID)
	case ChatSettingsReadTargetSession:
		if t.ProjectID != nil || t.WorkspaceID != nil {
			return errors.New("session Chat settings target cannot contain lazy target identifiers")
		}
		if t.Session == nil || t.Session.IsZero() {
			return errors.New("session Chat settings target requires session_id")
		}
		return nil
	default:
		return errors.New("Chat settings target kind is invalid")
	}
}

func validateChatTargetID(field string, value *string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(*value) != *value {
		return fmt.Errorf("%s must not have leading or trailing whitespace", field)
	}
	return nil
}

type ChatSettingsReadRequest struct {
	Target ChatSettingsReadTarget `json:"target"`
}

func (r ChatSettingsReadRequest) Validate() error { return r.Target.Validate() }

func (r *ChatSettingsReadRequest) UnmarshalJSON(data []byte) error {
	type wire ChatSettingsReadRequest
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = ChatSettingsReadRequest(decoded)
	return r.Validate()
}

type ChatSettingsEditability string

const (
	ChatSettingsEditable       ChatSettingsEditability = "editable"
	ChatSettingsWorkflowLock   ChatSettingsEditability = "workflow_lock"
	ChatSettingsCachingLock    ChatSettingsEditability = "caching_lock"
	ChatSettingsPolicyDisabled ChatSettingsEditability = "policy_disabled"
)

type ChatSettingsSupervisorValue string

const (
	ChatSettingsSupervisorOff        ChatSettingsSupervisorValue = "off"
	ChatSettingsSupervisorAfterEdits ChatSettingsSupervisorValue = "edits"
	ChatSettingsSupervisorAlways     ChatSettingsSupervisorValue = "all"
)

type ChatSettingsThinkingKind string

const (
	ChatSettingsThinkingEnumerated ChatSettingsThinkingKind = "enumerated"
	ChatSettingsThinkingCustom     ChatSettingsThinkingKind = "custom"
)

type ChatSettingsAutoCompactionPolicy string

const (
	ChatSettingsAutoCompactionOptional ChatSettingsAutoCompactionPolicy = "optional"
	ChatSettingsAutoCompactionRequired ChatSettingsAutoCompactionPolicy = "required"
	ChatSettingsAutoCompactionDisabled ChatSettingsAutoCompactionPolicy = "disabled"
)

type ChatSettingsAgentSummary struct {
	Role     string `json:"role"`
	Model    string `json:"model"`
	Thinking string `json:"thinking"`
}

type ChatSettingsAgentChoice struct {
	Role               string   `json:"role"`
	Model              string   `json:"model"`
	Thinking           string   `json:"thinking"`
	Tools              []string `json:"tools"`
	CustomSystemPrompt bool     `json:"custom_system_prompt"`
	CustomCapabilities bool     `json:"custom_capabilities"`
	AgentCallable      bool     `json:"agent_callable"`
}

type ChatSettingsSupervisor struct {
	Value       ChatSettingsSupervisorValue `json:"value"`
	Editability ChatSettingsEditability     `json:"editability"`
}

type ChatSettingsThinking struct {
	Kind          ChatSettingsThinkingKind `json:"kind"`
	Value         string                   `json:"value"`
	BaselineValue string                   `json:"baseline_value"`
	Values        []string                 `json:"values,omitempty"`
	Editability   ChatSettingsEditability  `json:"editability"`
}

type ChatSettingsFast struct {
	Value       bool                    `json:"value"`
	Editability ChatSettingsEditability `json:"editability"`
}

type ChatSettingsQuestions struct {
	Capable     bool                    `json:"capable"`
	Enabled     bool                    `json:"enabled"`
	Editability ChatSettingsEditability `json:"editability"`
}

type ChatSettingsAutoCompaction struct {
	Policy      ChatSettingsAutoCompactionPolicy `json:"policy"`
	Stored      bool                             `json:"stored"`
	Effective   bool                             `json:"effective"`
	Editability ChatSettingsEditability          `json:"editability"`
}

type ChatSettings struct {
	SelectedAgent    ChatSettingsAgentSummary   `json:"selected_agent"`
	AgentChoices     []ChatSettingsAgentChoice  `json:"agent_choices"`
	AgentEditability ChatSettingsEditability    `json:"agent_editability"`
	Supervisor       ChatSettingsSupervisor     `json:"supervisor"`
	Thinking         *ChatSettingsThinking      `json:"thinking,omitempty"`
	Fast             *ChatSettingsFast          `json:"fast,omitempty"`
	Questions        ChatSettingsQuestions      `json:"questions"`
	AutoCompaction   ChatSettingsAutoCompaction `json:"auto_compaction"`
	AgentLocked      bool                       `json:"agent_locked"`
	WorkflowLocked   bool                       `json:"workflow_locked"`
	CachingLocked    bool                       `json:"caching_locked"`
}

type ChatSettingsSessionFacts struct {
	SessionID         runtimeids.SessionID  `json:"session_id"`
	PreviousSessionID *runtimeids.SessionID `json:"previous_session_id,omitempty"`
	TaskID            *string               `json:"task_id,omitempty"`
}

type ChatSettingsReadResponse struct {
	Settings ChatSettings              `json:"settings"`
	Session  *ChatSettingsSessionFacts `json:"session,omitempty"`
}

func (r ChatSettingsReadResponse) ValidateForTarget(target ChatSettingsReadTarget) error {
	if err := target.Validate(); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	switch target.TargetKind {
	case ChatSettingsReadTargetLazy:
		if r.Session != nil {
			return errors.New("lazy Chat settings response cannot contain Session facts")
		}
	case ChatSettingsReadTargetSession:
		if r.Session == nil || r.Session.SessionID != *target.Session {
			return errors.New("materialized Chat settings response must contain the target Session")
		}
		if r.Session.PreviousSessionID != nil && r.Session.PreviousSessionID.IsZero() {
			return errors.New("previous Session ID is invalid")
		}
		if r.Session.TaskID != nil {
			if _, err := runtimeids.ParseTaskID(*r.Session.TaskID); err != nil {
				return fmt.Errorf("task_id: %w", err)
			}
		}
	}
	return nil
}

type ChatSettingsAgentPreparationCategory string

const (
	ChatSettingsAgentInvalidConfiguration ChatSettingsAgentPreparationCategory = "invalid_configuration"
	ChatSettingsAgentProviderUnavailable  ChatSettingsAgentPreparationCategory = "provider_unavailable"
	ChatSettingsAgentInternalPreparation  ChatSettingsAgentPreparationCategory = "internal_preparation"
)

type ChatSettingsAgentPreparationError struct {
	Agent    string                               `json:"agent"`
	Category ChatSettingsAgentPreparationCategory `json:"category"`
}

func (e *ChatSettingsAgentPreparationError) Error() string {
	return fmt.Sprintf("Chat settings Agent preparation failed: %s (%s)", e.Agent, e.Category)
}

func (e *ChatSettingsAgentPreparationError) Validate() error {
	if e == nil || strings.TrimSpace(e.Agent) == "" || strings.TrimSpace(e.Agent) != e.Agent {
		return errors.New("Chat settings Agent is invalid")
	}
	switch e.Category {
	case ChatSettingsAgentInvalidConfiguration,
		ChatSettingsAgentProviderUnavailable,
		ChatSettingsAgentInternalPreparation:
		return nil
	default:
		return errors.New("Chat settings Agent preparation category is invalid")
	}
}

func (e *ChatSettingsAgentPreparationError) RPCErrorCode() int {
	return protocol.ErrCodeChatSettingsAgentPreparation
}

func (e *ChatSettingsAgentPreparationError) RPCErrorData() json.RawMessage {
	if e == nil || e.Validate() != nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type string `json:"type"`
		ChatSettingsAgentPreparationError
	}{"chat_settings_agent_preparation", *e})
}

func DecodeChatSettingsAgentPreparationError(data json.RawMessage, message string) error {
	var envelope struct {
		Type string `json:"type"`
		ChatSettingsAgentPreparationError
	}
	if err := protocol.DecodeStrictJSON(data, &envelope); err == nil &&
		envelope.Type == "chat_settings_agent_preparation" &&
		envelope.ChatSettingsAgentPreparationError.Validate() == nil {
		return &envelope.ChatSettingsAgentPreparationError
	}
	if strings.TrimSpace(message) == "" {
		return errors.New("Chat settings Agent preparation failed")
	}
	return errors.New(strings.TrimSpace(message))
}
