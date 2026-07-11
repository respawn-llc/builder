package transcript

import (
	"strings"

	"core/shared/toolspec"
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
	normalized := normalizeToolTextMeta(meta)
	candidates := []string{normalized.CompactText, normalized.PatchSummary, normalized.Command, text}
	if !IsPatchFamilyToolName(normalized.ToolName) {
		candidates = append(candidates, normalized.ToolName)
	}
	return firstToolTextCandidate(candidates, patchToolNameToSkip(normalized))
}

func DetailedToolCallText(meta *ToolCallMeta, text string) string {
	normalized := normalizeToolTextMeta(meta)
	candidates := []string{normalized.PatchDetail, normalized.Command, normalized.CompactText, text}
	if !IsPatchFamilyToolName(normalized.ToolName) {
		candidates = append(candidates, normalized.ToolName)
	}
	for _, candidate := range candidates {
		if skippedToolTextCandidate(normalized, candidate) {
			continue
		}
		if detailed := strings.TrimSpace(candidate); detailed != "" {
			return detailed
		}
	}
	return defaultToolCallFallback
}

func IsPatchFamilyToolName(toolName string) bool {
	id, ok := toolspec.ParseID(toolName)
	return ok && (id == toolspec.ToolPatch || id == toolspec.ToolEdit)
}

func normalizeToolTextMeta(meta *ToolCallMeta) ToolCallMeta {
	if meta != nil && meta.HasCompactText() {
		return NormalizeToolCallMeta(*meta)
	}
	if meta == nil {
		return ToolCallMeta{}
	}
	return NormalizeToolCallMeta(*meta)
}

func firstToolTextCandidate(candidates []string, skipped string) string {
	for _, candidate := range candidates {
		if skipped != "" && strings.TrimSpace(candidate) == skipped {
			continue
		}
		if text := firstToolTextLine(candidate); text != "" {
			return text
		}
	}
	return defaultToolCallFallback
}

func patchToolNameToSkip(meta ToolCallMeta) string {
	if !IsPatchFamilyToolName(meta.ToolName) {
		return ""
	}
	return strings.TrimSpace(meta.ToolName)
}

func skippedToolTextCandidate(meta ToolCallMeta, candidate string) bool {
	skipped := patchToolNameToSkip(meta)
	return skipped != "" && strings.TrimSpace(candidate) == skipped
}

func firstToolTextLine(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, "\n", 2)
	first := strings.TrimSpace(parts[0])
	if first == "" {
		return ""
	}
	command, _ := SplitInlineMeta(first)
	return command
}
