package app

import (
	"fmt"

	"core/cli/tui/ongoing"
	"core/cli/tui/transcriptrender"
)

type ongoingLiveInputDisposition uint8

const (
	ongoingLiveInputQueued ongoingLiveInputDisposition = iota + 1
	ongoingLiveInputSteering
)

type ongoingLiveInput struct {
	Text        string
	Disposition ongoingLiveInputDisposition
}

func renderOngoingLiveInputLines(inputs []ongoingLiveInput, width int) []transcriptrender.Line {
	if width <= 0 {
		width = 80
	}
	lines := make([]transcriptrender.Line, 0, len(inputs))
	for _, input := range inputs {
		text := ongoing.TerminalSafeSingleLine(input.Text)
		if text == "" {
			continue
		}
		role, faint := ongoingLiveInputStyle(input.Disposition)
		lines = append(lines, renderOngoingLiveTextLine(text, width, role, faint))
	}
	return lines
}

func renderOngoingLiveTextLines(texts []string, width int, role transcriptrender.StyleRole, faint bool) []transcriptrender.Line {
	if width <= 0 {
		width = 80
	}
	lines := make([]transcriptrender.Line, 0, len(texts))
	for _, text := range texts {
		text = ongoing.TerminalSafeSingleLine(text)
		if text == "" {
			continue
		}
		lines = append(lines, renderOngoingLiveTextLine(text, width, role, faint))
	}
	return lines
}

func renderOngoingLiveTextLine(text string, width int, role transcriptrender.StyleRole, faint bool) transcriptrender.Line {
	span := transcriptrender.SemanticSpan(text, role)
	if faint {
		span.Style = span.Style.With(transcriptrender.SpanAttributeFaint)
	}
	line := transcriptrender.Line{Spans: []transcriptrender.Span{span}}
	return transcriptrender.TruncateLine(line, width, false)
}

func ongoingLiveInputStyle(disposition ongoingLiveInputDisposition) (transcriptrender.StyleRole, bool) {
	switch disposition {
	case ongoingLiveInputQueued:
		return transcriptrender.StyleRoleNoticeSecondary, true
	case ongoingLiveInputSteering:
		return transcriptrender.StyleRoleNoticePrimary, false
	default:
		panic(fmt.Sprintf("ongoing live input has invalid disposition %d", disposition))
	}
}
