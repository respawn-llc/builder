package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (i *interactiveAuthInteractor) printAuthSection(theme, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	var out strings.Builder
	out.WriteByte('\n')
	out.WriteString(lipgloss.NewStyle().Foreground(uiPalette(theme).primary).Bold(true).Render(title))
	out.WriteByte('\n')
	for idx, line := range lines {
		if idx > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(line)
	}
	out.WriteString("\n\n")
	_, _ = fmt.Fprintf(i.stderrOrDiscard(), "%s", out.String())
}

func (i *interactiveAuthInteractor) stderrOrDiscard() io.Writer {
	if i == nil || i.stderr == nil {
		return io.Discard
	}
	return i.stderr
}
