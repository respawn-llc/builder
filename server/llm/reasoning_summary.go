package llm

import (
	"strings"

	"core/shared/textutil"
)

func normalizeReasoningEntries(entries []ReasoningEntry) []ReasoningEntry {
	out := make([]ReasoningEntry, 0, len(entries))
	for _, entry := range entries {
		role, present := textutil.OptionalTrimmed(entry.Role)
		summary := normalizeReasoningSummaryLines(strings.Split(strings.ReplaceAll(entry.Text, "\r\n", "\n"), "\n"))
		if !present || summary == "" {
			continue
		}
		out = append(out, ReasoningEntry{Role: textutil.Value(role), Text: summary})
	}
	return out
}

func reasoningSummaryDeltaFromText(key, role, text string) ReasoningSummaryDelta {
	return ReasoningSummaryDelta{
		Key:           key,
		Role:          role,
		Text:          text,
		CurrentStatus: currentReasoningStatus(text),
	}
}

func normalizeReasoningSummaryLines(lines []string) string {
	firstContent := -1
	lastContent := -1
	for idx, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if firstContent < 0 {
			firstContent = idx
		}
		lastContent = idx
	}
	if firstContent < 0 || lastContent < firstContent {
		return ""
	}

	trimmed := lines[firstContent : lastContent+1]
	out := make([]string, 0, len(trimmed))
	prevBlank := false
	for _, line := range trimmed {
		blank := strings.TrimSpace(line) == ""
		if blank {
			if prevBlank {
				continue
			}
			out = append(out, "")
			prevBlank = true
			continue
		}
		out = append(out, line)
		prevBlank = false
	}
	return strings.Join(out, "\n")
}
