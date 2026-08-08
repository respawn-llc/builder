package session

import "strings"

const persistedMessageTypeCompactionSummary = "compaction_summary"

const firstPromptPreviewMaxChars = 120

func normalizeFirstPromptPreview(content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if trimmed == "" {
			continue
		}
		return truncatePromptPreview(trimmed, firstPromptPreviewMaxChars)
	}
	return ""
}

func truncatePromptPreview(text string, maxChars int) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || maxChars <= 0 {
		return trimmed
	}
	runes := []rune(trimmed)
	if len(runes) <= maxChars {
		return trimmed
	}
	if maxChars == 1 {
		return "…"
	}
	return string(runes[:maxChars-1]) + "…"
}

func isVisibleUserMessageFields(role string, messageType string, content string) bool {
	if strings.TrimSpace(role) != "user" {
		return false
	}
	if strings.TrimSpace(content) == "" {
		return false
	}
	if strings.TrimSpace(messageType) == persistedMessageTypeCompactionSummary {
		return false
	}
	return true
}
