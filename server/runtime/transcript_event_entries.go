package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func VisibleChatEntriesFromMessage(msg llm.Message) []ChatEntry {
	entries := make([]ChatEntry, 0, 1+len(msg.ToolCalls))
	switch msg.Role {
	case llm.RoleUser:
		if entry, ok := visibleUserTranscriptEntry(msg); ok {
			entries = append(entries, entry)
		}
	case llm.RoleAssistant:
		if strings.TrimSpace(msg.Content) != "" && !isNoopFinalAnswer(msg) {
			entries = append(entries, ChatEntry{Role: "assistant", Text: msg.Content, Phase: msg.Phase})
		}
		for _, call := range msg.ToolCalls {
			entries = append(entries, formatPersistedToolCall(call))
		}
	case llm.RoleTool:
		callID := strings.TrimSpace(msg.ToolCallID)
		result := tools.Result{
			CallID: callID,
			Name:   toolspec.ID(strings.TrimSpace(msg.Name)),
			Output: []byte(msg.Content),
		}
		if result.Name == "" {
			result.Name = toolspec.ID("tool")
		}
		entries = append(entries, toolResultChatEntry(result))
	case llm.RoleDeveloper:
		if entry, ok := visibleDeveloperChatEntry(msg); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func TranscriptEntriesFromEvent(evt Event) []ChatEntry {
	switch evt.Kind {
	case EventConversationUpdated:
		return VisibleChatEntriesFromMessage(evt.Message)
	case EventUserMessageFlushed:
		text := strings.TrimSpace(evt.UserMessage)
		if text == "" {
			return nil
		}
		return []ChatEntry{{Role: "user", Text: evt.UserMessage}}
	case EventAssistantMessage:
		return VisibleChatEntriesFromMessage(evt.Message)
	case EventToolCallStarted:
		if evt.ToolCall == nil {
			return nil
		}
		return []ChatEntry{formatPersistedToolCall(*evt.ToolCall)}
	case EventToolCallCompleted:
		if evt.ToolResult == nil {
			return nil
		}
		return []ChatEntry{toolResultChatEntry(*evt.ToolResult)}
	case EventReviewerCompleted:
		// Reviewer completion remains a runtime-status event only.
		// Persisted reviewer terminal rows must arrive through local_entry_added
		// so the client has exactly one committed transcript source.
		return nil
	case EventCompactionCompleted:
		return nil
	case EventCompactionFailed:
		return nil
	case EventInFlightClearFailed:
		if strings.TrimSpace(evt.Error) == "" {
			return nil
		}
		return []ChatEntry{{Role: "error", Text: fmt.Sprintf("Run cleanup warning: %s", evt.Error)}}
	case EventCacheWarning:
		if evt.CacheWarning == nil {
			return nil
		}
		return []ChatEntry{{Role: cacheWarningTranscriptRole, Text: transcript.CacheWarningText(*evt.CacheWarning), Visibility: evt.CacheWarningVisibility}}
	case EventLocalEntryAdded:
		if evt.LocalEntry == nil {
			return nil
		}
		entry := *evt.LocalEntry
		return []ChatEntry{entry}
	case EventBackgroundUpdated:
		if evt.Background == nil {
			return nil
		}
		if evt.Background.Type != "completed" && evt.Background.Type != "killed" {
			return nil
		}
		compact := formatBackgroundShellCompact(*evt.Background)
		return []ChatEntry{{
			Role:          "system",
			Text:          formatBackgroundShellNotice(*evt.Background),
			CondensedText: compact,
			MessageType:   llm.MessageTypeBackgroundNotice,
			CompactLabel:  compact,
		}}
	default:
		return nil
	}
}

func toolResultChatEntry(result tools.Result) ChatEntry {
	role := "tool_result_ok"
	if result.IsError {
		role = "tool_result_error"
	}
	presentation := result.Presentation
	if presentation != nil {
		normalized := transcript.NormalizeToolCallMeta(*presentation)
		presentation = &normalized
	}
	return ChatEntry{
		Role:              role,
		Text:              tools.FormatToolResultByName(string(result.Name), result.Output, result.IsError),
		CondensedText:     strings.TrimSpace(result.CondensedText),
		ToolCallID:        strings.TrimSpace(result.CallID),
		ToolResultSummary: strings.TrimSpace(result.Summary),
		ToolCall:          presentation,
	}
}
