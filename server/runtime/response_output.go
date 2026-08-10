package runtime

import "core/server/llm"

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

func responseContainsThinking(response llm.Response) bool {
	return len(response.Reasoning) > 0 ||
		len(response.ReasoningItems) > 0 ||
		responseOutputIsReasoningOnly(response.OutputItems)
}
