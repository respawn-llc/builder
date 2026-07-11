package ongoing

import (
	"strings"

	"core/cli/tui/terminaltext"
)

func terminalSafePlainText(text string) string {
	return strings.TrimRight(terminaltext.Plain(text), "\n")
}

func TerminalSafeSingleLine(text string) string {
	return terminaltext.SingleLine(text)
}

func terminalSafeMarkdownSource(text string) string {
	return terminalSafePlainText(text)
}
