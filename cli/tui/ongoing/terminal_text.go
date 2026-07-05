package ongoing

import (
	"strings"
	"unicode"
)

func terminalSafePlainText(text string) string {
	var builder strings.Builder
	for _, r := range text {
		switch {
		case r == '\n':
			builder.WriteRune(r)
		case r == '\t':
			builder.WriteRune(' ')
		case unicode.IsControl(r):
			continue
		default:
			builder.WriteRune(r)
		}
	}
	return strings.TrimRight(builder.String(), "\n")
}

func TerminalSafeSingleLine(text string) string {
	var builder strings.Builder
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			builder.WriteRune(' ')
		case unicode.IsControl(r):
			continue
		default:
			builder.WriteRune(r)
		}
	}
	return strings.TrimSpace(builder.String())
}

func terminalSafeMarkdownSource(text string) string {
	return terminalSafePlainText(text)
}
