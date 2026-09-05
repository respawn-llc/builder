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
	ChatSettingsReadTargetNewChat ChatSettingsReadTargetKind = "new_chat"
	ChatSettingsReadTargetSession ChatSettingsReadTargetKind = "session"
)

type ChatSettingsReadTarget struct {
	TargetKind  ChatSettingsReadTargetKind `json:"kind"`
	ProjectID   *string                    `json:"project_id,omitempty"`
	WorkspaceID *string                    `json:"workspace_id,omitempty"`
	Session     *runtimeids.SessionID      `json:"session_id,omitempty"`
}

func NewChatSettingsTarget(projectID, workspaceID string) ChatSettingsReadTarget {
	return ChatSettingsReadTarget{
		TargetKind:  ChatSettingsReadTargetNewChat,
		ProjectID:   &projectID,
		WorkspaceID: &workspaceID,
	}
}

func SessionChatSettingsTarget(sessionID runtimeids.SessionID) ChatSettingsReadTarget {
	return ChatSettingsReadTarget{TargetKind: ChatSettingsReadTargetSession, Session: &sessionID}
}

func (t ChatSettingsReadTarget) Validate() error {
	switch t.TargetKind {
	case ChatSettingsReadTargetNewChat:
		if t.Session != nil {
			return errors.New("New Chat settings target cannot contain session_id")
		}
		if err := validateChatTargetID("project_id", t.ProjectID); err != nil {
			return err
		}
		return validateChatTargetID("workspace_id", t.WorkspaceID)
	case ChatSettingsReadTargetSession:
		if t.ProjectID != nil || t.WorkspaceID != nil {
			return errors.New("Session Chat settings target cannot contain New Chat identifiers")
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
	Baseline    ChatSettingsSupervisorValue `json:"baseline"`
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
	TaskShortID       *string               `json:"task_short_id,omitempty"`
}

type ChatSettingsTaskIdentity struct {
	TaskID      string
	TaskShortID string
}

func (i ChatSettingsTaskIdentity) Validate() error {
	if _, err := runtimeids.ParseTaskID(i.TaskID); err != nil {
		return fmt.Errorf("task_id: %w", err)
	}
	if strings.TrimSpace(i.TaskShortID) == "" || strings.TrimSpace(i.TaskShortID) != i.TaskShortID {
		return errors.New("task_short_id is invalid")
	}
	return nil
}

type ChatSettingsReadResponse struct {
	Settings ChatSettings              `json:"settings"`
	Session  *ChatSettingsSessionFacts `json:"session,omitempty"`
}

type ChatSettingsMutationOperationKind string

const (
	ChatSettingsMutationAgent          ChatSettingsMutationOperationKind = "agent"
	ChatSettingsMutationSupervisor     ChatSettingsMutationOperationKind = "supervisor"
	ChatSettingsMutationThinking       ChatSettingsMutationOperationKind = "thinking"
	ChatSettingsMutationFast           ChatSettingsMutationOperationKind = "fast"
	ChatSettingsMutationQuestions      ChatSettingsMutationOperationKind = "questions"
	ChatSettingsMutationAutoCompaction ChatSettingsMutationOperationKind = "auto_compaction"
)

type ChatSettingsMutationOperation struct {
	Kind    ChatSettingsMutationOperationKind `json:"kind"`
	Role    *string                           `json:"role,omitempty"`
	Value   *string                           `json:"value,omitempty"`
	Enabled *bool                             `json:"enabled,omitempty"`
}

func (o ChatSettingsMutationOperation) Validate() error {
	switch o.Kind {
	case ChatSettingsMutationAgent:
		if o.Role == nil || strings.TrimSpace(*o.Role) == "" || o.Value != nil || o.Enabled != nil {
			return errors.New("Agent Chat settings operation requires only role")
		}
	case ChatSettingsMutationSupervisor, ChatSettingsMutationThinking:
		if o.Value == nil || strings.TrimSpace(*o.Value) == "" || o.Role != nil || o.Enabled != nil {
			return errors.New("value Chat settings operation requires only value")
		}
	case ChatSettingsMutationFast, ChatSettingsMutationQuestions, ChatSettingsMutationAutoCompaction:
		if o.Enabled == nil || o.Role != nil || o.Value != nil {
			return errors.New("boolean Chat settings operation requires only enabled")
		}
	default:
		return fmt.Errorf("Chat settings mutation operation kind %q is invalid", o.Kind)
	}
	return nil
}

type ChatSettingsMutationRequest struct {
	SessionID runtimeids.SessionID          `json:"session_id"`
	Operation ChatSettingsMutationOperation `json:"operation"`
}

func (r ChatSettingsMutationRequest) Validate() error {
	if r.SessionID.IsZero() {
		return errors.New("Session Chat settings mutation requires session_id")
	}
	return r.Operation.Validate()
}
func (r *ChatSettingsMutationRequest) UnmarshalJSON(data []byte) error {
	type wire ChatSettingsMutationRequest
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	request := ChatSettingsMutationRequest(decoded)
	if err := request.Validate(); err != nil {
		return err
	}
	*r = request
	return nil
}

type ChatSettingsMutationResultKind string

const (
	ChatSettingsMutationApplied  ChatSettingsMutationResultKind = "applied"
	ChatSettingsMutationRejected ChatSettingsMutationResultKind = "rejected"
)

type ChatSettingsMutationRejectionReason string

const (
	ChatSettingsMutationAgentLocked              ChatSettingsMutationRejectionReason = "agent_locked"
	ChatSettingsMutationAgentUnavailable         ChatSettingsMutationRejectionReason = "agent_unavailable"
	ChatSettingsMutationThinkingUnavailable      ChatSettingsMutationRejectionReason = "thinking_unavailable"
	ChatSettingsMutationFastUnavailable          ChatSettingsMutationRejectionReason = "fast_unavailable"
	ChatSettingsMutationAutoCompactionPolicyLock ChatSettingsMutationRejectionReason = "auto_compaction_policy_locked"
)

func (r ChatSettingsMutationRejectionReason) Validate() error {
	switch r {
	case ChatSettingsMutationAgentLocked,
		ChatSettingsMutationAgentUnavailable,
		ChatSettingsMutationThinkingUnavailable,
		ChatSettingsMutationFastUnavailable,
		ChatSettingsMutationAutoCompactionPolicyLock:
		return nil
	default:
		return fmt.Errorf("Chat settings mutation rejection reason %q is invalid", r)
	}
}

type ChatSettingsMutationAppliedResult struct {
	Changed bool `json:"changed"`
}
type ChatSettingsMutationRejectedResult struct {
	Reason ChatSettingsMutationRejectionReason `json:"reason"`
}
type ChatSettingsMutationResult struct {
	Kind     ChatSettingsMutationResultKind      `json:"kind"`
	Applied  *ChatSettingsMutationAppliedResult  `json:"applied,omitempty"`
	Rejected *ChatSettingsMutationRejectedResult `json:"rejected,omitempty"`
}

func NewChatSettingsMutationApplied(changed bool) ChatSettingsMutationResult {
	return ChatSettingsMutationResult{Kind: ChatSettingsMutationApplied, Applied: &ChatSettingsMutationAppliedResult{Changed: changed}}
}
func NewChatSettingsMutationRejected(reason ChatSettingsMutationRejectionReason) ChatSettingsMutationResult {
	return ChatSettingsMutationResult{Kind: ChatSettingsMutationRejected, Rejected: &ChatSettingsMutationRejectedResult{Reason: reason}}
}
func (r ChatSettingsMutationResult) Validate() error {
	switch r.Kind {
	case ChatSettingsMutationApplied:
		if r.Applied == nil || r.Rejected != nil {
			return errors.New("applied Chat settings result requires only applied data")
		}
	case ChatSettingsMutationRejected:
		if r.Rejected == nil || r.Applied != nil {
			return errors.New("rejected Chat settings result requires only rejected data")
		}
		if err := r.Rejected.Reason.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("Chat settings mutation result kind %q is invalid", r.Kind)
	}
	return nil
}

type ChatSettingsMutationResponse struct {
	Result   ChatSettingsMutationResult `json:"result"`
	Settings ChatSettings               `json:"settings"`
	Session  *ChatSettingsSessionFacts  `json:"session,omitempty"`
	Context  ChatContext                `json:"context"`
}

func (r *ChatSettingsMutationResponse) UnmarshalJSON(data []byte) error {
	type wire ChatSettingsMutationResponse
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = ChatSettingsMutationResponse(decoded)
	return nil
}

func (r ChatSettingsMutationResponse) ValidateForSession(sessionID runtimeids.SessionID) error {
	if err := r.Result.Validate(); err != nil {
		return fmt.Errorf("result: %w", err)
	}
	if err := (ChatSettingsReadResponse{
		Settings: r.Settings,
		Session:  r.Session,
	}).ValidateForTarget(SessionChatSettingsTarget(sessionID)); err != nil {
		return err
	}
	return r.Context.Validate()
}
func (r ChatSettingsReadResponse) ValidateForTarget(target ChatSettingsReadTarget) error {
	if err := target.Validate(); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	switch target.TargetKind {
	case ChatSettingsReadTargetNewChat:
		if r.Session != nil {
			return errors.New("New Chat settings response cannot contain Session facts")
		}
	case ChatSettingsReadTargetSession:
		if r.Session == nil || r.Session.SessionID != *target.Session {
			return errors.New("Session Chat settings response must contain the target Session")
		}
		if r.Session.PreviousSessionID != nil && r.Session.PreviousSessionID.IsZero() {
			return errors.New("previous Session ID is invalid")
		}
		if (r.Session.TaskID == nil) != (r.Session.TaskShortID == nil) {
			return errors.New("Task ID and Task Short ID must both be present or absent")
		}
		if r.Session.TaskID != nil {
			if err := (ChatSettingsTaskIdentity{
				TaskID:      *r.Session.TaskID,
				TaskShortID: *r.Session.TaskShortID,
			}).Validate(); err != nil {
				return err
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
