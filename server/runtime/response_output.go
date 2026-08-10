package runtime

import (
	"strings"

	"core/server/llm"
)

func responseOutputIsReasoningOnly(items []llm.ResponseItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.Type != llm.ResponseItemTypeReasoning {
			return false
		}
	}
	return true
}

func responseOutputContainsNonemptyCommentary(items []llm.ResponseItem) bool {
	for _, item := range items {
		if item.Type != llm.ResponseItemTypeMessage ||
			item.Role == nil ||
			*item.Role != llm.RoleAssistant ||
			item.Phase == nil ||
			*item.Phase != llm.MessagePhaseCommentary ||
			item.Content == nil ||
			strings.TrimSpace(*item.Content) == "" {
			continue
		}
		return true
	}
	return false
}

func responseContainsProgress(response llm.Response) bool {
	return len(response.Reasoning) > 0 ||
		len(response.ReasoningItems) > 0 ||
		responseOutputIsReasoningOnly(response.OutputItems) ||
		responseOutputContainsNonemptyCommentary(response.OutputItems)
}
