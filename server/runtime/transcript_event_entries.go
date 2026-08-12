package runtime

import (
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func VisibleChatEntriesFromMessage(msg llm.Message) []ChatEntry {
	return visibleChatEntriesFromMessage(msg, nil, nil)
}

func visibleChatEntriesFromMessage(msg llm.Message, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) []ChatEntry {
	entries := make([]ChatEntry, 0, 1+len(msg.ToolCalls))
	switch msg.Role {
	case llm.RoleUser:
		if entry, ok := visibleUserTranscriptEntry(msg); ok {
			entries = append(entries, entry)
		}
	case llm.RoleAssistant:
		if msg.Content != nil && strings.TrimSpace(*msg.Content) != "" && !isBlankFinalAnswer(msg) {
			phase, _ := textutil.OptionalValue(msg.Phase)
			entries = append(entries, ChatEntry{
				Visibility: assistantTranscriptVisibility(phase),
				Role:       "assistant",
				Text:       *msg.Content,
				Phase:      phase,
			})
		}
		for _, call := range msg.ToolCalls {
			entries = append(entries, formatPersistedToolCall(call))
			if result, ok := synthesizedToolResultForCall(call, completions, materializedToolCalls); ok {
				entries = append(entries, toolResultChatEntry(result))
			}
		}
	case llm.RoleTool:
		entries = append(entries, toolResultChatEntry(resolvedToolResultForMessage(msg, completions)))
	case llm.RoleDeveloper:
		if entry, ok := visibleDeveloperChatEntry(msg); ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func synthesizedToolResultForCall(call llm.ToolCall, completions map[string]tools.Result, materializedToolCalls map[string]struct{}) (tools.Result, bool) {
	callID := strings.TrimSpace(call.ID)
	if callID == "" {
		return tools.Result{}, false
	}
	if _, ok := materializedToolCalls[callID]; ok {
		return tools.Result{}, false
	}
	completion, ok := completions[callID]
	return completion, ok
}

func assistantTranscriptVisibility(phase llm.MessagePhase) transcript.EntryVisibility {
	switch transcript.ClassifyAssistantPhase(string(phase)) {
	case transcript.AssistantPhaseCommentary:
		return transcript.EntryVisibilityDetail
	case transcript.AssistantPhaseFinal:
		return transcript.EntryVisibilityOngoing
	default:
		panic(fmt.Sprintf("unsupported assistant transcript phase %q", phase))
	}
}

func TranscriptEntriesFromEvent(evt Event) []ChatEntry {
	var entries []ChatEntry
	switch evt.Kind {
	case EventConversationUpdated:
		entries = VisibleChatEntriesFromMessage(evt.Message)
	case EventUserMessageFlushed:
		text := strings.TrimSpace(evt.UserMessage)
		if text == "" {
			return nil
		}
		entries = []ChatEntry{{Visibility: transcript.EntryVisibilityOngoing, Role: "user", Text: evt.UserMessage}}
	case EventAssistantMessage:
		entries = VisibleChatEntriesFromMessage(evt.Message)
	case EventToolCallStarted:
		if evt.ToolCall == nil {
			return nil
		}
		entries = []ChatEntry{formatPersistedToolCall(*evt.ToolCall)}
	case EventToolCallCompleted:
		if evt.ToolResult == nil {
			return nil
		}
		entries = []ChatEntry{toolResultChatEntry(*evt.ToolResult)}
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
		return nil
	case EventCacheWarning:
		if evt.CacheWarning == nil {
			return nil
		}
		entries = []ChatEntry{{Role: cacheWarningTranscriptRole, Text: transcript.CacheWarningText(*evt.CacheWarning), Visibility: evt.CacheWarningVisibility}}
	case EventLocalEntryAdded:
		if evt.LocalEntry == nil {
			return nil
		}
		entry := *evt.LocalEntry
		entries = []ChatEntry{entry}
	case EventBackgroundUpdated:
		if evt.Background == nil {
			return nil
		}
		if !evt.Background.Type.IsTerminal() {
			return nil
		}
		compact := formatBackgroundShellCompact(*evt.Background)
		entries = []ChatEntry{{
			Role:                 string(transcript.EntryRoleSystem),
			Visibility:           transcript.EntryVisibilityOngoingCollapsed,
			Text:                 formatBackgroundShellNotice(*evt.Background),
			CondensedText:        compact,
			MessageType:          llm.MessageTypeBackgroundNotice,
			CompactLabel:         compact,
			BackgroundActivityID: evt.Background.ActivityID.String(),
			BackgroundProcessID:  strings.TrimSpace(evt.Background.ID),
			BackgroundExitCode:   textutil.Pointer(evt.Background.ExitCode),
		}}
	default:
		return nil
	}
	stepID := strings.TrimSpace(evt.StepID)
	for index := range entries {
		existing := strings.TrimSpace(entries[index].StepID)
		if existing != "" && stepID != "" && existing != stepID {
			panic(fmt.Sprintf(
				"transcript entry step identity conflicts with runtime event: entry_index=%d entry_step_id=%q event_step_id=%q event_kind=%q",
				index,
				existing,
				stepID,
				evt.Kind,
			))
		}
		if stepID != "" {
			entries[index].StepID = stepID
		}
	}
	return entries
}

func resolvedToolResultForMessage(msg llm.Message, completions map[string]tools.Result) tools.Result {
	callID, _ := textutil.OptionalTrimmed(msg.ToolCallID)
	var output []byte
	if msg.Content != nil {
		output = []byte(*msg.Content)
	}
	name, _ := textutil.OptionalTrimmed(msg.Name)
	result := tools.Result{
		CallID: callID,
		Name:   toolspec.ID(name),
		Output: output,
	}
	if completion, ok := completions[callID]; ok {
		if result.Name == "" {
			result.Name = completion.Name
		}
		if msg.Content == nil && len(completion.Output) > 0 {
			result.Output = completion.Output
		}
		result.IsError = completion.IsError
		result.Summary = completion.Summary
		result.CondensedText = completion.CondensedText
		result.Presentation = completion.Presentation
	}
	if result.Name == "" {
		result.Name = toolspec.ID("tool")
	}
	return result
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
	condensedText, _ := textutil.OptionalTrimmed(result.CondensedText)
	summary, _ := textutil.OptionalTrimmed(result.Summary)
	return ChatEntry{
		Visibility:        transcript.EntryVisibilityOngoingCollapsed,
		Role:              role,
		Text:              tools.FormatToolResultByName(string(result.Name), result.Output, result.IsError),
		CondensedText:     condensedText,
		ToolCallID:        strings.TrimSpace(result.CallID),
		ToolResultSummary: summary,
		ToolCall:          presentation,
	}
}
