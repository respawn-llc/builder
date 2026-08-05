package runtime

import "strings"

type ReasoningTracePresentation struct {
	CompactText string
	Text        string
}

func ProjectReasoningTrace(text string) ReasoningTracePresentation {
	if strings.HasPrefix(text, "**") {
		text = text[2:]
	}
	if strings.HasSuffix(text, "**") {
		text = text[:len(text)-2]
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasSuffix(line, "\r") {
			line = line[:len(line)-1]
		}
		if strings.TrimSpace(line) != "" {
			return ReasoningTracePresentation{CompactText: line, Text: text}
		}
	}
	return ReasoningTracePresentation{Text: text}
}
