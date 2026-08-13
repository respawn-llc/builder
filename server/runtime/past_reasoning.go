package runtime

import (
	"core/server/llm"
	"core/shared/textutil"
)

func pastReasoningBeforeLatestKentInstructionBoundary(items []llm.ResponseItem) []llm.ResponseItem {
	latestBoundary := -1
	for index := range items {
		if isKentInstructionBoundary(items[index]) {
			latestBoundary = index
		}
	}
	if latestBoundary < 0 {
		return nil
	}
	selected := make([]llm.ResponseItem, 0)
	for _, item := range items[:latestBoundary] {
		if item.Type != llm.ResponseItemTypeReasoning {
			continue
		}
		if _, present := textutil.OptionalTrimmed(item.ID); !present {
			continue
		}
		if _, present := textutil.OptionalTrimmed(item.EncryptedContent); !present {
			continue
		}
		selected = append(selected, llm.CloneResponseItems([]llm.ResponseItem{item})[0])
	}
	return selected
}

func isKentInstructionBoundary(item llm.ResponseItem) bool {
	if item.Type != llm.ResponseItemTypeMessage || item.Role == nil {
		return false
	}
	if *item.Role == llm.RoleUser {
		return item.MessageType == nil
	}
	return *item.Role == llm.RoleDeveloper &&
		item.MessageType != nil &&
		*item.MessageType == llm.MessageTypeAgentSteer
}
