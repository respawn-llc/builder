package clientui

import "strings"

var supportedThinkingLevels = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
	"max":    {},
	"ultra":  {},
}

func NormalizeThinkingLevel(level string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(level))
	if normalized == "" {
		return "", false
	}
	_, ok := supportedThinkingLevels[normalized]
	return normalized, ok
}
