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
		line := transcriptrender.Line{Spans: []transcriptrender.Span{{Text: text, Role: role, Faint: faint}}}
		lines = append(lines, transcriptrender.TruncateLine(line, width, false))
	}
	return lines
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
