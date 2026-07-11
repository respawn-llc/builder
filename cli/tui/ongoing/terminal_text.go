package ongoing

import (
	"strings"

	"core/cli/tui/transcriptrender"
)

func terminalSafePlainText(text string) string {
	return strings.TrimRight(transcriptrender.TerminalSafePlainText(text), "\n")
}

func TerminalSafeSingleLine(text string) string {
	return transcriptrender.TerminalSafeSingleLine(text)
}

func terminalSafeMarkdownSource(text string) string {
	return terminalSafePlainText(text)
}
