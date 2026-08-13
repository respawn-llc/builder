package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/toolspec"
)

type ChatSettingsReadTargetKind string

const (
	ChatSettingsReadTargetLazy    ChatSettingsReadTargetKind = "lazy"
	ChatSettingsReadTargetSession ChatSettingsReadTargetKind = "session"
)

type ChatSettingsReadTarget struct {
	kind        ChatSettingsReadTargetKind
	projectID   *string
	workspaceID *string
	sessionID   *runtimeids.SessionID
}

func LazyChatSettingsTarget(projectID, workspaceID string) ChatSettingsReadTarget {
	return ChatSettingsReadTarget{
		kind:        ChatSettingsReadTargetLazy,
		projectID:   &projectID,
		workspaceID: &workspaceID,
	}
}

func SessionChatSettingsTarget(sessionID runtimeids.SessionID) ChatSettingsReadTarget {
	return ChatSettingsReadTarget{
		kind:      ChatSettingsReadTargetSession,
		sessionID: &sessionID,
	}
}

func (t ChatSettingsReadTarget) Kind() ChatSettingsReadTargetKind {
	return t.kind
}

func (t ChatSettingsReadTarget) Lazy() (projectID, workspaceID string, ok bool) {
	if t.projectID == nil || t.workspaceID == nil {
		return "", "", false
	}
	return *t.projectID, *t.workspaceID, true
}

func (t ChatSettingsReadTarget) SessionID() (runtimeids.SessionID, bool) {
	if t.sessionID == nil {
		return runtimeids.SessionID{}, false
	}
	return *t.sessionID, true
}

func (t ChatSettingsReadTarget) Validate() error {
	switch t.kind {
	case ChatSettingsReadTargetLazy:
		if t.sessionID != nil {
			return errors.New("lazy Chat settings target cannot contain session_id")
		}
		if err := validateOpaqueChatTargetID("project_id", t.projectID); err != nil {
			return err
		}
		return validateOpaqueChatTargetID("workspace_id", t.workspaceID)
	case ChatSettingsReadTargetSession:
		if t.projectID != nil || t.workspaceID != nil {
			return errors.New("session Chat settings target cannot contain lazy target identifiers")
		}
		if t.sessionID == nil || t.sessionID.IsZero() {
			return errors.New("session Chat settings target requires session_id")
		}
		return nil
	default:
		return errors.New("Chat settings target kind is invalid")
	}
}

func validateOpaqueChatTargetID(field string, value *string) error {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(*value) != *value {
		return fmt.Errorf("%s must not have leading or trailing whitespace", field)
	}
	return nil
}

func (t ChatSettingsReadTarget) MarshalJSON() ([]byte, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	type wire struct {
		Kind        ChatSettingsReadTargetKind `json:"kind"`
		ProjectID   *string                    `json:"project_id,omitempty"`
		WorkspaceID *string                    `json:"workspace_id,omitempty"`
		SessionID   *runtimeids.SessionID      `json:"session_id,omitempty"`
	}
	return json.Marshal(wire{
		Kind:        t.kind,
		ProjectID:   t.projectID,
		WorkspaceID: t.workspaceID,
		SessionID:   t.sessionID,
	})
}

func (t *ChatSettingsReadTarget) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind        ChatSettingsReadTargetKind `json:"kind"`
		ProjectID   *string                    `json:"project_id"`
		WorkspaceID *string                    `json:"workspace_id"`
		SessionID   *runtimeids.SessionID      `json:"session_id"`
	}
	if err := protocol.DecodeStrictJSON(data, &wire); err != nil {
		return err
	}
	decoded := ChatSettingsReadTarget{
		kind:        wire.Kind,
		projectID:   wire.ProjectID,
		workspaceID: wire.WorkspaceID,
		sessionID:   wire.SessionID,
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*t = decoded
	return nil
}

type ChatSettingsReadRequest struct {
	Target ChatSettingsReadTarget `json:"target"`
}

func (r ChatSettingsReadRequest) Validate() error {
	return r.Target.Validate()
}

func (r *ChatSettingsReadRequest) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Target ChatSettingsReadTarget `json:"target"`
	}
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	request := ChatSettingsReadRequest{Target: decoded.Target}
	if err := request.Validate(); err != nil {
		return err
	}
	*r = request
	return nil
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

var ErrChatSettingsAgentPreparation = errors.New("Chat settings Agent preparation failed")

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
	if e == nil {
		return ErrChatSettingsAgentPreparation.Error()
	}
	return fmt.Sprintf("%s: %s (%s)", ErrChatSettingsAgentPreparation, e.Agent, e.Category)
}

func (e *ChatSettingsAgentPreparationError) Is(target error) bool {
	return target == ErrChatSettingsAgentPreparation
}

func (e *ChatSettingsAgentPreparationError) Validate() error {
	if e == nil {
		return errors.New("Chat settings Agent preparation error is required")
	}
	if err := validateTrimmedNonblank("agent", e.Agent); err != nil {
		return err
	}
	switch e.Category {
	case ChatSettingsAgentInvalidConfiguration,
		ChatSettingsAgentProviderUnavailable,
		ChatSettingsAgentInternalPreparation:
		return nil
	default:
		return errors.New("category is invalid")
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
	}{
		Type:                              "chat_settings_agent_preparation",
		ChatSettingsAgentPreparationError: *e,
	})
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
		return ErrChatSettingsAgentPreparation
	}
	return errors.Join(ErrChatSettingsAgentPreparation, errors.New(strings.TrimSpace(message)))
}

func (r ChatSettingsReadResponse) Validate() error {
	if err := r.Settings.Validate(); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if r.Session != nil {
		if err := r.Session.Validate(); err != nil {
			return fmt.Errorf("session: %w", err)
		}
	}
	return nil
}

func (r ChatSettingsReadResponse) ValidateForTarget(target ChatSettingsReadTarget) error {
	if err := target.Validate(); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if err := r.Validate(); err != nil {
		return err
	}
	switch target.Kind() {
	case ChatSettingsReadTargetLazy:
		if r.Session != nil {
			return errors.New("lazy Chat settings response cannot contain Session facts")
		}
	case ChatSettingsReadTargetSession:
		if r.Session == nil {
			return errors.New("materialized Chat settings response requires Session facts")
		}
		targetSessionID, _ := target.SessionID()
		if r.Session.SessionID != targetSessionID {
			return errors.New("materialized Chat settings response Session ID must match target")
		}
	default:
		return errors.New("Chat settings target kind is invalid")
	}
	return nil
}

func (r ChatSettingsReadResponse) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire ChatSettingsReadResponse
	return json.Marshal(wire(r))
}

func (r *ChatSettingsReadResponse) UnmarshalJSON(data []byte) error {
	type wire ChatSettingsReadResponse
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	response := ChatSettingsReadResponse(decoded)
	if err := response.Validate(); err != nil {
		return err
	}
	*r = response
	return nil
}

func (s ChatSettings) Validate() error {
	if err := s.SelectedAgent.Validate(); err != nil {
		return fmt.Errorf("selected agent: %w", err)
	}
	if len(s.AgentChoices) == 0 {
		return errors.New("agent choices are required")
	}
	roles := make(map[string]struct{}, len(s.AgentChoices))
	for index, choice := range s.AgentChoices {
		if err := choice.Validate(); err != nil {
			return fmt.Errorf("agent choice %d: %w", index, err)
		}
		if _, exists := roles[choice.Role]; exists {
			return fmt.Errorf("agent choice role %q is duplicated", choice.Role)
		}
		roles[choice.Role] = struct{}{}
		if index == 0 && choice.Role != "default" {
			return errors.New("default must be the first agent choice")
		}
		if index == 1 && choice.Role == "fast" {
			continue
		}
		if index > 0 {
			previous := s.AgentChoices[index-1].Role
			if previous != "default" && previous != "fast" && previous >= choice.Role {
				return errors.New("remaining agent choices must be sorted by role")
			}
			if previous == "default" && choice.Role != "fast" && index+1 < len(s.AgentChoices) &&
				s.AgentChoices[index+1].Role == "fast" {
				return errors.New("fast must immediately follow default")
			}
		}
	}
	_, selectedPresent := roles[s.SelectedAgent.Role]
	if !selectedPresent && !s.CachingLocked {
		return errors.New("selected agent must be present in choices unless caching locked")
	}
	if s.AgentLocked != (s.WorkflowLocked || s.CachingLocked) {
		return errors.New("agent_locked must equal workflow_locked or caching_locked")
	}
	wantAgentEditability := ChatSettingsEditable
	if s.WorkflowLocked {
		wantAgentEditability = ChatSettingsWorkflowLock
	} else if s.CachingLocked {
		wantAgentEditability = ChatSettingsCachingLock
	}
	if s.AgentEditability != wantAgentEditability {
		return fmt.Errorf("agent editability must be %q", wantAgentEditability)
	}
	if err := s.Supervisor.Validate(); err != nil {
		return fmt.Errorf("supervisor: %w", err)
	}
	if s.Thinking != nil {
		if err := s.Thinking.Validate(); err != nil {
			return fmt.Errorf("thinking: %w", err)
		}
	}
	if s.Fast != nil {
		if err := s.Fast.Validate(); err != nil {
			return fmt.Errorf("fast: %w", err)
		}
	}
	if err := s.Questions.Validate(); err != nil {
		return fmt.Errorf("questions: %w", err)
	}
	if err := s.AutoCompaction.Validate(s.WorkflowLocked); err != nil {
		return fmt.Errorf("auto_compaction: %w", err)
	}
	return nil
}

func (a ChatSettingsAgentSummary) Validate() error {
	if err := validateTrimmedNonblank("role", a.Role); err != nil {
		return err
	}
	if err := validateTrimmedNonblank("model", a.Model); err != nil {
		return err
	}
	if err := validateTrimmedNonblank("thinking", a.Thinking); err != nil {
		return err
	}
	return nil
}

func (a ChatSettingsAgentChoice) Validate() error {
	if err := (ChatSettingsAgentSummary{
		Role:     a.Role,
		Model:    a.Model,
		Thinking: a.Thinking,
	}).Validate(); err != nil {
		return err
	}
	return validateChatSettingsTools(a.Tools)
}

func (s ChatSettingsSupervisor) Validate() error {
	switch s.Value {
	case ChatSettingsSupervisorOff, ChatSettingsSupervisorAfterEdits, ChatSettingsSupervisorAlways:
	default:
		return errors.New("value is invalid")
	}
	return requireEditable(s.Editability)
}

func (t ChatSettingsThinking) Validate() error {
	if err := validateTrimmedNonblank("value", t.Value); err != nil {
		return err
	}
	if err := validateTrimmedNonblank("baseline_value", t.BaselineValue); err != nil {
		return err
	}
	if err := requireEditable(t.Editability); err != nil {
		return err
	}
	switch t.Kind {
	case ChatSettingsThinkingEnumerated:
		if len(t.Values) == 0 {
			return errors.New("enumerated Thinking values are required")
		}
		seen := make(map[string]struct{}, len(t.Values))
		for _, value := range t.Values {
			if err := validateTrimmedNonblank("Thinking value", value); err != nil {
				return err
			}
			if _, exists := seen[value]; exists {
				return fmt.Errorf("Thinking value %q is duplicated", value)
			}
			seen[value] = struct{}{}
		}
		if _, ok := seen[t.Value]; !ok {
			return errors.New("current Thinking value must be enumerated")
		}
		if _, ok := seen[t.BaselineValue]; !ok {
			return errors.New("baseline Thinking value must be enumerated")
		}
	case ChatSettingsThinkingCustom:
		if len(t.Values) != 0 {
			return errors.New("custom Thinking cannot contain enumerated values")
		}
	default:
		return errors.New("kind is invalid")
	}
	return nil
}

func (f ChatSettingsFast) Validate() error {
	return requireEditable(f.Editability)
}

func (q ChatSettingsQuestions) Validate() error {
	return requireEditable(q.Editability)
}

func (a ChatSettingsAutoCompaction) Validate(workflowLocked bool) error {
	var wantEffective bool
	var wantEditability ChatSettingsEditability
	switch a.Policy {
	case ChatSettingsAutoCompactionOptional:
		wantEffective = a.Stored
		wantEditability = ChatSettingsEditable
	case ChatSettingsAutoCompactionRequired:
		wantEffective = true
		if workflowLocked {
			wantEditability = ChatSettingsWorkflowLock
		} else {
			wantEditability = ChatSettingsEditable
		}
	case ChatSettingsAutoCompactionDisabled:
		wantEffective = false
		wantEditability = ChatSettingsPolicyDisabled
	default:
		return errors.New("policy is invalid")
	}
	if a.Effective != wantEffective {
		return fmt.Errorf("effective value must be %t for %q policy", wantEffective, a.Policy)
	}
	if a.Editability != wantEditability {
		return fmt.Errorf("editability must be %q for %q policy", wantEditability, a.Policy)
	}
	return nil
}

func (f ChatSettingsSessionFacts) Validate() error {
	if f.SessionID.IsZero() {
		return errors.New("session_id is required")
	}
	if f.PreviousSessionID != nil && f.PreviousSessionID.IsZero() {
		return errors.New("previous_session_id cannot be empty")
	}
	if f.TaskID != nil {
		if _, err := runtimeids.ParseTaskID(*f.TaskID); err != nil {
			return fmt.Errorf("task_id: %w", err)
		}
	}
	return nil
}

func requireEditable(value ChatSettingsEditability) error {
	if value != ChatSettingsEditable {
		return fmt.Errorf("editability must be %q", ChatSettingsEditable)
	}
	return nil
}

func validateTrimmedNonblank(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must not have leading or trailing whitespace", field)
	}
	return nil
}

func validateChatSettingsTools(values []string) error {
	catalog := toolspec.IDStrings(toolspec.CatalogIDs())
	lastIndex := -1
	for _, value := range values {
		index := slices.Index(catalog, value)
		if index < 0 {
			return fmt.Errorf("unknown tool %q", value)
		}
		if index <= lastIndex {
			return errors.New("tools must be unique and in canonical catalog order")
		}
		lastIndex = index
	}
	return nil
}
