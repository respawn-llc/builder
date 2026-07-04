package runtime

import (
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
