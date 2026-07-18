package app

import (
	"strings"

	tuiinput "core/cli/tui/input"
	"core/shared/theme"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// uiEditableInputRenderSpec remains the shared projection contract for
// independent app-owned input fields outside the main composer and ask pane.
type uiEditableInputRenderSpec struct {
	Prefix       string
	Text         string
	CursorIndex  int
	RenderCursor bool
	Mask         rune
	Placeholder  string
}

func renderFramedEditableInputLines(width, maxContentLines int, spec uiEditableInputRenderSpec, lineStyle lipgloss.Style, borderStyle lipgloss.Style) []string {
	if width < 1 {
		return []string{padRight("", width)}
	}
	field := tuiinput.NewField()
	field.Editor.Replace(spec.Text)
	field.Editor.SetCursor(byteOffsetForRuneCursor(spec.Text, spec.CursorIndex))
	field.Prefix = spec.Prefix
	field.MaxLines = maxContentLines
	field.Cursor = spec.RenderCursor
	field.Mask = spec.Mask
	field.Placeholder = spec.Placeholder
	rendered := field.Render(width)
	return renderFramedLines(width, tuiinput.RenderSoftCursorLines(width, rendered, lineStyle), borderStyle)
}

func renderFramedLines(width int, lines []string, borderStyle lipgloss.Style) []string {
	border := borderStyle.Render(strings.Repeat("─", width))
	out := make([]string, 0, len(lines)+2)
	out = append(out, border)
	out = append(out, lines...)
	out = append(out, border)
	return out
}

func splitPlainLines(v string) []string {
	if strings.TrimSpace(v) == "" {
		return []string{""}
	}
	return strings.Split(v, "\n")
}

func wrapLine(line string, width int) []string {
	if width <= 0 {
		return []string{line}
	}
	if runewidth.StringWidth(line) <= width {
		return []string{line}
	}
	parts := make([]string, 0, 4)
	remaining := []rune(line)
	for len(remaining) > 0 {
		w := 0
		cut := 0
		for i, r := range remaining {
			rw := runewidth.RuneWidth(r)
			if w+rw > width {
				break
			}
			w += rw
			cut = i + 1
		}
		if cut == 0 {
			cut = 1
		}
		parts = append(parts, string(remaining[:cut]))
		remaining = remaining[cut:]
	}
	return parts
}

func truncateQueuedMessageLine(message string, width int) string {
	if width < 1 {
		return ""
	}
	firstLine := message
	hasMoreContent := false
	if idx := strings.IndexRune(message, '\n'); idx >= 0 {
		firstLine = message[:idx]
		hasMoreContent = true
	}
	if !hasMoreContent && runewidth.StringWidth(firstLine) <= width {
		return firstLine
	}
	if width == 1 {
		return "…"
	}
	maxWidth := width - 1
	runes := []rune(firstLine)
	cut := 0
	w := 0
	for i, r := range runes {
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		if w+rw > maxWidth {
			break
		}
		w += rw
		cut = i + 1
	}
	if cut == 0 {
		return "…"
	}
	return string(runes[:cut]) + "…"
}

func padRight(line string, width int) string {
	if width <= 0 {
		return ""
	}
	current := runewidth.StringWidth(line)
	if current == width {
		return line
	}
	if current > width {
		return line
	}
	return line + strings.Repeat(" ", width-current)
}

func padANSIRight(line string, width int) string {
	if width <= 0 {
		return ""
	}
	current := lipgloss.Width(line)
	if current >= width {
		return line
	}
	return line + strings.Repeat(" ", width-current)
}

func truncateANSIRight(line string, width int) string {
	if width <= 0 {
		return ""
	}
	if line == "" || lipgloss.Width(line) <= width {
		return line
	}
	truncationSuffix := xansi.ResetHyperlink() + "…" + "\x1b[0m"
	if width == 1 {
		return truncationSuffix
	}
	parser := xansi.GetParser()
	defer xansi.PutParser(parser)

	visibleLimit := width - 1
	if visibleLimit < 0 {
		visibleLimit = 0
	}
	state := byte(0)
	input := line
	consumedWidth := 0
	var out strings.Builder
	for len(input) > 0 {
		seq, seqWidth, n, newState := xansi.GraphemeWidth.DecodeSequenceInString(input, state, parser)
		if n <= 0 {
			break
		}
		state = newState
		if seqWidth == 0 {
			out.WriteString(seq)
			input = input[n:]
			continue
		}
		if consumedWidth+seqWidth > visibleLimit {
			break
		}
		out.WriteString(seq)
		consumedWidth += seqWidth
		input = input[n:]
	}
	out.WriteString(truncationSuffix)
	return out.String()
}

type uiStyles struct {
	brand         lipgloss.Style
	modeChip      lipgloss.Style
	panel         lipgloss.Style
	chat          lipgloss.Style
	input         lipgloss.Style
	inputDisabled lipgloss.Style
	meta          lipgloss.Style
	ask           lipgloss.Style
}

func uiThemeStyles(theme string) uiStyles {
	p := uiPalette(theme)
	return uiStyles{
		brand: lipgloss.NewStyle().Foreground(p.primary).Bold(true),
		modeChip: lipgloss.NewStyle().
			Foreground(p.modeText).
			Background(p.modeBg).
			Padding(0, 1).
			Bold(true),
		panel: lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(p.border).
			Padding(0, 1),
		chat: lipgloss.NewStyle().
			Foreground(p.foreground),
		input: lipgloss.NewStyle().
			Foreground(p.foreground),
		inputDisabled: lipgloss.NewStyle().
			Foreground(p.muted).
			Faint(true),
		meta: lipgloss.NewStyle().Foreground(p.muted).Faint(true),
		ask: lipgloss.NewStyle().
			BorderStyle(lipgloss.ThickBorder()).
			BorderForeground(p.secondary).
			Foreground(p.foreground).
			Padding(0, 1),
	}
}

type uiColors struct {
	primary    lipgloss.TerminalColor
	secondary  lipgloss.TerminalColor
	foreground lipgloss.TerminalColor
	muted      lipgloss.TerminalColor
	border     lipgloss.TerminalColor
	modeBg     lipgloss.TerminalColor
	modeText   lipgloss.TerminalColor
	background lipgloss.TerminalColor
	inputBg    lipgloss.TerminalColor
}

func uiPalette(themeName string) uiColors {
	palette := theme.ResolvePalette(themeName).App
	return uiColors{
		primary:    palette.Primary.Lipgloss(),
		secondary:  palette.Secondary.Lipgloss(),
		foreground: palette.Foreground.Lipgloss(),
		muted:      palette.Muted.Lipgloss(),
		border:     palette.Border.Lipgloss(),
		modeBg:     palette.ModeBg.Lipgloss(),
		modeText:   palette.ModeText.Lipgloss(),
		background: palette.Background.Lipgloss(),
		inputBg:    palette.InputBg.Lipgloss(),
	}
}
