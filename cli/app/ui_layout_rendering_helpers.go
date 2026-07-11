package app

import (
	"strings"

	tuiinput "core/cli/tui/input"
	"core/shared/theme"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

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
	renderedField := renderEditableInputField(width, maxContentLines, spec)
	return renderFramedLines(width, tuiinput.RenderSoftCursorLines(width, renderedField, lineStyle), borderStyle)
}

func renderFramedLines(width int, lines []string, borderStyle lipgloss.Style) []string {
	border := borderStyle.Render(strings.Repeat("─", width))
	out := make([]string, 0, len(lines)+2)
	out = append(out, border)
	out = append(out, lines...)
	out = append(out, border)
	return out
}

func wrappedEditableInputLines(width int, spec uiEditableInputRenderSpec) []string {
	field := editableInputField(width, 0, spec)
	return field.Render(width).Lines
}

func visibleEditableInputViewport(width, maxContentLines int, spec uiEditableInputRenderSpec) ([]string, int, int) {
	rendered := renderEditableInputField(width, maxContentLines, spec)
	cursor := rendered.Cursor
	if !cursor.Visible {
		return rendered.Lines, -1, 0
	}
	return rendered.Lines, cursor.Row, cursor.Col
}

func renderEditableInputField(width, maxContentLines int, spec uiEditableInputRenderSpec) tuiinput.RenderResult {
	field := editableInputField(width, maxContentLines, spec)
	return field.Render(width)
}

func editableInputField(width, maxContentLines int, spec uiEditableInputRenderSpec) tuiinput.Field {
	if width < 1 {
		width = 1
	}
	editor := tuiinput.NewEditor()
	editor.Replace(spec.Text)
	editor.SetCursor(byteOffsetForRuneCursor(spec.Text, spec.CursorIndex))
	field := tuiinput.NewField()
	field.Editor = editor
	field.Prefix = spec.Prefix
	field.MaxLines = maxContentLines
	field.Cursor = spec.RenderCursor
	field.Mask = spec.Mask
	field.Placeholder = spec.Placeholder
	return field
}

func byteOffsetForRuneCursor(text string, cursor int) int {
	if cursor < 0 {
		return len(text)
	}
	if cursor == 0 {
		return 0
	}
	runeIndex := 0
	for byteIndex := range text {
		if runeIndex == cursor {
			return byteIndex
		}
		runeIndex++
	}
	return len(text)
}

func visibleWrappedLineStart(totalLines, maxContentLines, cursorLine int, trackCursor bool) int {
	if maxContentLines < 1 || totalLines <= maxContentLines {
		return 0
	}
	maxStart := totalLines - maxContentLines
	if !trackCursor || cursorLine < 0 {
		return maxStart
	}
	start := cursorLine - maxContentLines + 1
	if start < 0 {
		return 0
	}
	if start > maxStart {
		return maxStart
	}
	return start
}

func overlayCursorOnLine(line string, cursorCol, width int, cursorStyle lipgloss.Style) string {
	if width < 1 {
		return line
	}

	runes := []rune(line)
	displayCol := 0
	for i, r := range runes {
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		if cursorCol < displayCol+rw {
			return string(runes[:i]) + cursorStyle.Render(string(r)) + string(runes[i+1:])
		}
		displayCol += rw
	}

	if displayCol < width {
		cursorCol = min(max(cursorCol, displayCol), width-1)
		return line + strings.Repeat(" ", cursorCol-displayCol) + cursorStyle.Render(" ")
	}

	if len(runes) == 0 {
		return cursorStyle.Render(" ")
	}

	last := len(runes) - 1
	return string(runes[:last]) + cursorStyle.Render(string(runes[last]))
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
	chatBg     lipgloss.TerminalColor
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
		chatBg:     palette.ChatBg.Lipgloss(),
		inputBg:    palette.InputBg.Lipgloss(),
	}
}
