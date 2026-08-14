package serverapi

import (
	"errors"
	"fmt"

	"core/shared/protocol"
	"core/shared/runtimeids"
)

type ChatContextRequest struct {
	Target ChatContextTarget `json:"target"`
}

func NewWorkspaceChatContextRequest() ChatContextRequest {
	return ChatContextRequest{
		Target: ChatContextTarget{WorkspaceChat: &ChatContextWorkspaceTarget{}},
	}
}

func NewSessionChatContextRequest(sessionID runtimeids.SessionID) ChatContextRequest {
	return ChatContextRequest{
		Target: ChatContextTarget{
			Session: &ChatContextSessionTarget{SessionID: sessionID},
		},
	}
}

func (r ChatContextRequest) Validate() error {
	return r.Target.Validate()
}

func (r *ChatContextRequest) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("Chat Context request is required")
	}
	type wire ChatContextRequest
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	request := ChatContextRequest(decoded)
	if err := request.Validate(); err != nil {
		return err
	}
	*r = request
	return nil
}

type ChatContextTarget struct {
	WorkspaceChat *ChatContextWorkspaceTarget `json:"workspace_chat,omitempty"`
	Session       *ChatContextSessionTarget   `json:"session,omitempty"`
}

func (t ChatContextTarget) Validate() error {
	if (t.WorkspaceChat == nil) == (t.Session == nil) {
		return errors.New("Chat Context target requires exactly one workspace_chat or session")
	}
	if t.Session != nil {
		return t.Session.Validate()
	}
	return nil
}

func (t ChatContextTarget) IsWorkspaceChat() bool {
	return t.WorkspaceChat != nil && t.Session == nil
}

func (t ChatContextTarget) SessionID() (runtimeids.SessionID, bool) {
	if t.Session == nil || t.WorkspaceChat != nil {
		return runtimeids.SessionID{}, false
	}
	return t.Session.SessionID, true
}

type ChatContextWorkspaceTarget struct{}

func (t *ChatContextWorkspaceTarget) UnmarshalJSON(data []byte) error {
	if t == nil {
		return errors.New("workspace Chat Context target is required")
	}
	type wire ChatContextWorkspaceTarget
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*t = ChatContextWorkspaceTarget(decoded)
	return nil
}

type ChatContextSessionTarget struct {
	SessionID runtimeids.SessionID `json:"session_id"`
}

func (t ChatContextSessionTarget) Validate() error {
	if t.SessionID.IsZero() || !t.SessionID.IsCanonicalUUIDv4() {
		return errors.New("Chat Context Session id must be a canonical UUIDv4")
	}
	return nil
}

type ChatContextCompactionMode string

const (
	ChatContextCompactionModeDisabled       ChatContextCompactionMode = "disabled"
	ChatContextCompactionModeLocal          ChatContextCompactionMode = "local"
	ChatContextCompactionModeProviderNative ChatContextCompactionMode = "provider_native"
)

func (m ChatContextCompactionMode) Validate() error {
	switch m {
	case ChatContextCompactionModeDisabled,
		ChatContextCompactionModeLocal,
		ChatContextCompactionModeProviderNative:
		return nil
	default:
		return fmt.Errorf("Chat Context Compaction Mode %q is invalid", m)
	}
}

type ChatContextResponse struct {
	Context ChatContext `json:"context"`
}

func (r ChatContextResponse) Validate() error {
	return r.Context.Validate()
}

func (r *ChatContextResponse) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("Chat Context response is required")
	}
	type wire ChatContextResponse
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = ChatContextResponse(decoded)
	return nil
}

type ChatContext struct {
	ContextWindowTokens      int64                     `json:"context_window_tokens"`
	UsedTokens               int64                     `json:"used_tokens"`
	RemainingTokens          int64                     `json:"remaining_tokens"`
	AutomaticThresholdTokens int64                     `json:"automatic_threshold_tokens"`
	AutoCompactionEnabled    bool                      `json:"auto_compaction_enabled"`
	CompactionMode           ChatContextCompactionMode `json:"compaction_mode"`
	CompletedCompactionCount int64                     `json:"completed_compaction_count"`
	CompactionRunning        bool                      `json:"compaction_running"`
	ManualCompactAvailable   bool                      `json:"manual_compact_available"`
}

func (c ChatContext) Validate() error {
	if c.ContextWindowTokens <= 0 {
		return errors.New("Chat Context window tokens must be positive")
	}
	if c.UsedTokens < 0 {
		return errors.New("Chat Context used tokens must be non-negative")
	}
	if c.RemainingTokens != c.ContextWindowTokens-c.UsedTokens {
		return errors.New("Chat Context remaining tokens must equal context window tokens minus used tokens")
	}
	if c.AutomaticThresholdTokens < 0 || c.AutomaticThresholdTokens > c.ContextWindowTokens {
		return errors.New("Chat Context automatic threshold tokens must be between zero and the context window")
	}
	if err := c.CompactionMode.Validate(); err != nil {
		return err
	}
	if c.CompletedCompactionCount < 0 {
		return errors.New("Chat Context completed compaction count must be non-negative")
	}
	return nil
}
