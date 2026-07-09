package transcript

import (
	"strings"

	patchformat "core/shared/transcript/patchformat"
)

const (
	InlineMetaSeparator     = "\x1f"
	defaultToolCallFallback = "tool call"
)

func SplitInlineMeta(line string) (string, string) {
	parts := strings.SplitN(line, InlineMetaSeparator, 2)
	command := strings.TrimSpace(parts[0])
	if len(parts) == 1 {
		return command, ""
	}
	return command, strings.TrimSpace(parts[1])
}

func CompactToolCallText(meta *ToolCallMeta, text string) string {
	if meta != nil && meta.HasCompactText() {
		if compact := patchformat.StripEditedLabel(meta.CompactText); compact != "" {
			return compact
		}
	}
	if meta != nil && meta.HasPatchSummary() {
		if summary := patchformat.StripEditedLabel(meta.PatchSummary); summary != "" {
			return summary
		}
	}
	if meta != nil && strings.TrimSpace(meta.Command) != "" {
		return strings.TrimSpace(meta.Command)
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return defaultToolCallFallback
	}
	parts := strings.SplitN(trimmed, "\n", 2)
	first := strings.TrimSpace(parts[0])
	if first == "" {
		return defaultToolCallFallback
	}
	command, _ := SplitInlineMeta(first)
	if command == "" {
		return defaultToolCallFallback
	}
	return command
}
