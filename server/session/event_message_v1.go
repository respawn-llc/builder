package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"core/shared/transcript"
)

type MessageType string

const (
	MessageTypeAgentsMD               MessageType = "agents.md"
	MessageTypeSkills                 MessageType = "skills"
	MessageTypeSubagents              MessageType = "subagents"
	MessageTypeEnvironment            MessageType = "environment"
	MessageTypeCompactionSummary      MessageType = "compaction_summary"
	MessageTypeInterruption           MessageType = "interruption"
	MessageTypeErrorFeedback          MessageType = "error_feedback"
	MessageTypeCompactionSoonReminder MessageType = "compaction_soon_reminder"
	MessageTypeHandoffFutureMessage   MessageType = "handoff_future_message"
	MessageTypeReviewerFeedback       MessageType = "reviewer_feedback"
	MessageTypeBackgroundNotice       MessageType = "background_notice"
	MessageTypeCustomToolCallOutput   MessageType = "custom_tool_call_output"
	// MessageTypeCompactionPreservedUserMessage retains its legacy serialized
	// value so existing Session logs remain readable without migration.
	MessageTypeCompactionPreservedUserMessage MessageType = "manual_compaction_carryover"
	MessageTypeHeadlessMode                   MessageType = "headless_mode"
	MessageTypeHeadlessModeExit               MessageType = "headless_mode_exit"
	MessageTypeWorkflowMode                   MessageType = "workflow_mode"
	MessageTypeWorkflowModeExit               MessageType = "workflow_mode_exit"
	MessageTypeWorktreeMode                   MessageType = "worktree_mode"
	MessageTypeWorktreeModeExit               MessageType = "worktree_mode_exit"
	MessageTypeGoal                           MessageType = "goal"
	MessageTypeActiveGoalContinuation         MessageType = "active_goal_continuation"
	MessageTypeAgentSteer                     MessageType = MessageType(transcript.MessageTypeAgentSteer)
)

type MessagePhase string

const (
	MessagePhaseCommentary MessagePhase = "commentary"
	MessagePhaseFinal      MessagePhase = "final_answer"
)

type ToolCallKind string

const (
	ToolCallKindFunction ToolCallKind = "function"
	ToolCallKindCustom   ToolCallKind = "custom"
)

type MessageToolCallRecord struct {
	CallID       string          `json:"call_id"`
	Name         string          `json:"name"`
	Kind         ToolCallKind    `json:"kind"`
	Presentation json.RawMessage `json:"presentation,omitempty"`
	Input        json.RawMessage `json:"input"`
	CustomInput  *string         `json:"custom_input,omitempty"`
}

type MessageReasoningRecord struct {
	ID               string `json:"id"`
	EncryptedContent string `json:"encrypted_content"`
}

type MessageRecord struct {
	Role                 MessageRole              `json:"role"`
	MessageType          *MessageType             `json:"message_type,omitempty"`
	SourcePath           *string                  `json:"source_path,omitempty"`
	WorktreeContext      *WorktreeContext         `json:"worktree_context,omitempty"`
	Content              *string                  `json:"content,omitempty"`
	CompactContent       *string                  `json:"compact_content,omitempty"`
	Name                 *string                  `json:"name,omitempty"`
	ToolCallID           *string                  `json:"tool_call_id,omitempty"`
	Phase                *MessagePhase            `json:"phase,omitempty"`
	BackgroundActivityID *string                  `json:"background_activity_id,omitempty"`
	BackgroundExitCode   *int                     `json:"background_exit_code,omitempty"`
	ToolCalls            []MessageToolCallRecord  `json:"tool_calls,omitempty"`
	ReasoningItems       []MessageReasoningRecord `json:"reasoning_items,omitempty"`
}

func (MessageRecord) eventKind() EventKind {
	return EventKindMessage
}

func (m MessageRecord) validate() error {
	_, err := normalizeMessageRecord(m)
	return err
}

func normalizeMessageRecord(message MessageRecord) (MessageRecord, error) {
	if err := validateMessageRole(message.Role); err != nil {
		return MessageRecord{}, err
	}
	var err error
	if message.MessageType, err = normalizeOptionalMessageType(message.MessageType); err != nil {
		return MessageRecord{}, err
	}
	if message.SourcePath, err = normalizeOptionalEventText("source path", message.SourcePath); err != nil {
		return MessageRecord{}, err
	}
	if message.Content, err = normalizeMessageContent(message); err != nil {
		return MessageRecord{}, err
	}
	if message.CompactContent, err = normalizeOptionalEventText("compact content", message.CompactContent); err != nil {
		return MessageRecord{}, err
	}
	if message.Name, err = normalizeOptionalEventIdentity("message name", message.Name); err != nil {
		return MessageRecord{}, err
	}
	if message.ToolCallID, err = normalizeOptionalEventIdentity("tool call identity", message.ToolCallID); err != nil {
		return MessageRecord{}, err
	}
	if message.BackgroundActivityID, err = normalizeOptionalEventIdentity("background activity identity", message.BackgroundActivityID); err != nil {
		return MessageRecord{}, err
	}
	if hasPartialBackgroundNoticeIdentity(message) {
		return MessageRecord{}, errBackgroundNoticePartialIdentity
	}
	if message.Phase, err = normalizeOptionalMessagePhase(message.Phase); err != nil {
		return MessageRecord{}, err
	}
	if message.WorktreeContext != nil {
		context, contextErr := normalizeWorktreeContext(*message.WorktreeContext)
		if contextErr != nil {
			return MessageRecord{}, fmt.Errorf("worktree context: %w", contextErr)
		}
		message.WorktreeContext = &context
	}
	if message.BackgroundExitCode != nil {
		exitCode := *message.BackgroundExitCode
		message.BackgroundExitCode = &exitCode
	}
	if len(message.ToolCalls) > 0 {
		message.ToolCalls = append([]MessageToolCallRecord(nil), message.ToolCalls...)
		for index := range message.ToolCalls {
			call, callErr := normalizeMessageToolCallRecord(message.ToolCalls[index])
			if callErr != nil {
				return MessageRecord{}, fmt.Errorf("tool call %d: %w", index, callErr)
			}
			message.ToolCalls[index] = call
		}
	}
	if len(message.ReasoningItems) > 0 {
		message.ReasoningItems = append([]MessageReasoningRecord(nil), message.ReasoningItems...)
		for index := range message.ReasoningItems {
			reasoning := message.ReasoningItems[index]
			reasoning.ID = strings.TrimSpace(reasoning.ID)
			reasoning.EncryptedContent = strings.TrimSpace(reasoning.EncryptedContent)
			if reasoning.ID == "" {
				return MessageRecord{}, fmt.Errorf("reasoning item %d identity is required", index)
			}
			if reasoning.EncryptedContent == "" {
				return MessageRecord{}, fmt.Errorf("reasoning item %d encrypted content is required", index)
			}
			message.ReasoningItems[index] = reasoning
		}
	}
	return message, nil
}

func normalizeMessageContent(message MessageRecord) (*string, error) {
	if message.Content != nil &&
		strings.TrimSpace(*message.Content) == "" {
		if transcript.IsBlankAssistantFinal(transcript.AssistantFinalCandidate{
			IsAssistant:    message.Role == MessageRoleAssistant,
			IsFinal:        message.Phase != nil && *message.Phase == MessagePhaseFinal,
			HasMessageType: message.MessageType != nil,
			Content:        message.Content,
		}) {
			content := *message.Content
			return &content, nil
		}
		return nil, fmt.Errorf("content must be non-empty when present")
	}
	return normalizeOptionalEventText("content", message.Content)
}

func hasPartialBackgroundNoticeIdentity(message MessageRecord) bool {
	return message.MessageType != nil &&
		*message.MessageType == MessageTypeBackgroundNotice &&
		(message.Name == nil) != (message.BackgroundActivityID == nil)
}

func validateMessageRole(role MessageRole) error {
	switch role {
	case MessageRoleSystem, MessageRoleUser, MessageRoleAssistant, MessageRoleTool, MessageRoleDeveloper:
		return nil
	default:
		return fmt.Errorf("unsupported role %q", role)
	}
}

func normalizeOptionalMessageType(messageType *MessageType) (*MessageType, error) {
	if messageType == nil {
		return nil, nil
	}
	switch *messageType {
	case MessageTypeAgentsMD, MessageTypeSkills, MessageTypeSubagents, MessageTypeEnvironment,
		MessageTypeCompactionSummary, MessageTypeInterruption, MessageTypeErrorFeedback,
		MessageTypeCompactionSoonReminder, MessageTypeHandoffFutureMessage,
		MessageTypeReviewerFeedback, MessageTypeBackgroundNotice, MessageTypeCustomToolCallOutput,
		MessageTypeCompactionPreservedUserMessage, MessageTypeHeadlessMode, MessageTypeHeadlessModeExit,
		MessageTypeWorkflowMode, MessageTypeWorkflowModeExit, MessageTypeWorktreeMode, MessageTypeWorktreeModeExit,
		MessageTypeGoal, MessageTypeActiveGoalContinuation, MessageTypeAgentSteer:
		value := *messageType
		return &value, nil
	default:
		return nil, fmt.Errorf("unsupported message type %q", *messageType)
	}
}

func normalizeOptionalMessagePhase(phase *MessagePhase) (*MessagePhase, error) {
	if phase == nil {
		return nil, nil
	}
	switch *phase {
	case MessagePhaseCommentary, MessagePhaseFinal:
		value := *phase
		return &value, nil
	default:
		return nil, fmt.Errorf("unsupported message phase %q", *phase)
	}
}

func normalizeMessageToolCallRecord(call MessageToolCallRecord) (MessageToolCallRecord, error) {
	call.CallID = strings.TrimSpace(call.CallID)
	call.Name = strings.TrimSpace(call.Name)
	if call.CallID == "" {
		return MessageToolCallRecord{}, fmt.Errorf("call identity is required")
	}
	if call.Name == "" {
		return MessageToolCallRecord{}, fmt.Errorf("tool name is required")
	}
	switch call.Kind {
	case ToolCallKindFunction:
	case ToolCallKindCustom:
	default:
		return MessageToolCallRecord{}, fmt.Errorf("unsupported tool call kind %q", call.Kind)
	}
	customInput, err := normalizeOptionalEventText("custom input", call.CustomInput)
	if err != nil {
		return MessageToolCallRecord{}, err
	}
	call.CustomInput = customInput
	if err := validateJSONValue("input", call.Input); err != nil {
		return MessageToolCallRecord{}, err
	}
	call.Input = append(json.RawMessage(nil), call.Input...)
	if len(call.Presentation) > 0 {
		if err := validateJSONValue("presentation", call.Presentation); err != nil {
			return MessageToolCallRecord{}, err
		}
		if _, ok := transcript.DecodeToolCallMeta(call.Presentation); !ok {
			return MessageToolCallRecord{}, fmt.Errorf("presentation is invalid")
		}
		call.Presentation = append(json.RawMessage(nil), call.Presentation...)
	}
	return call, nil
}
