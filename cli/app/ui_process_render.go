package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	appprocessview "core/cli/app/internal/status"
	"core/shared/clientui"
	sharedtheme "core/shared/theme"

	"github.com/charmbracelet/lipgloss"
)

const (
	processListHeaderLines = 1
	processListEntryLines  = 4
	processListFooterLines = 1
)

var (
	processListRailGlyph = sharedtheme.SelectionRailGlyph
	processListRailBlank = sharedtheme.SelectionRailBlank
)

func (l uiViewLayout) renderProcessList(width, height int, style uiStyles) []string {
	m := l.model
	if height < 1 {
		return []string{padRight("", width)}
	}
	headerLines := []string{renderProcessListHeader(m.processList.entries, width, style)}
	remainingHeight := height - len(headerLines)
	if remainingHeight < 0 {
		remainingHeight = 0
	}
	footerLines := []string{}
	if remainingHeight >= processListEntryLines+processListFooterLines {
		footerLines = []string{renderProcessListFooter(width, style)}
		remainingHeight -= len(footerLines)
	}
	contentHeight := remainingHeight
	content := make([]string, 0, max(0, contentHeight))
	if contentHeight > 0 {
		if len(m.processList.entries) == 0 {
			content = append(content, renderEmptyProcessListMessage(m.processList, style))
		} else {
			start := processListStartRow(m.processList.selection, len(m.processList.entries), contentHeight)
			startEntry := start / processListEntryLines
			rowOffset := start % processListEntryLines
			visibleEntries := (contentHeight + rowOffset + processListEntryLines - 1) / processListEntryLines
			if startEntry+visibleEntries > len(m.processList.entries) {
				visibleEntries = len(m.processList.entries) - startEntry
			}
			visibleRows := make([]string, 0, max(0, visibleEntries)*processListEntryLines)
			for idx := startEntry; idx < startEntry+visibleEntries; idx++ {
				entry := m.processList.entries[idx]
				visibleRows = append(visibleRows, renderProcessListEntry(entry, idx == m.processList.selection, width, m.theme, m.spinnerFrame, style)...)
			}
			if rowOffset > 0 && rowOffset < len(visibleRows) {
				visibleRows = visibleRows[rowOffset:]
			}
			end := min(contentHeight, len(visibleRows))
			content = append(content, visibleRows[:end]...)
		}
		for len(content) < contentHeight {
			content = append(content, "")
		}
	}
	lines := make([]string, 0, len(headerLines)+len(content)+len(footerLines))
	lines = append(lines, headerLines...)
	lines = append(lines, content...)
	lines = append(lines, footerLines...)
	return l.renderChatContentLines(lines, width, style)
}

func renderEmptyProcessListMessage(state uiProcessListState, style uiStyles) string {
	if state.loading {
		return style.meta.Render("○ Loading background processes...")
	}
	if errText := strings.TrimSpace(state.errorText); errText != "" {
		return style.meta.Render("○ Failed to load background processes: " + errText)
	}
	return style.meta.Render("○ No background processes.")
}

func renderProcessListHeader(entries []clientui.BackgroundProcess, width int, style uiStyles) string {
	running := 0
	for _, entry := range entries {
		state := strings.TrimSpace(entry.State)
		if entry.Running || state == "starting" || state == "running" {
			running++
		}
	}
	title := fmt.Sprintf("Background Processes (%d)", len(entries))
	if len(entries) > 0 {
		title = fmt.Sprintf("%s  %d running", title, running)
	}
	return style.brand.Render(truncateQueuedMessageLine(title, width))
}

func renderProcessListFooter(width int, style uiStyles) string {
	controls := "Esc/q close | Enter/i paste | k kill | o logs | PgUp/PgDn/Home/End move | r refresh"
	return style.meta.Render(truncateQueuedMessageLine(controls, width))
}

func renderProcessListEntry(entry clientui.BackgroundProcess, selected bool, width int, theme string, spinnerFrame int, style uiStyles) []string {
	palette := uiPalette(theme)
	entryStyles := newProcessListEntryStyles(theme, selected, processStateColor(entry, palette))
	railGlyph := processListRailBlank
	separatorGlyph := ""
	if selected {
		railGlyph = processListRailGlyph
		separatorGlyph = processListRailGlyph
	}
	indicator := renderProcessStateIndicator(entry, spinnerFrame)
	stateMeta := []string{processStateLabel(entry)}
	if age := humanAge(entry.StartedAt); age != "--" {
		stateMeta = append(stateMeta, age)
	}
	if workdir := processListWorkdirLabel(entry.Workdir); workdir != "" {
		stateMeta = append(stateMeta, workdir)
	}
	line1Parts := []string{entryStyles.rail.Render(railGlyph), entryStyles.line.Render(" "), entryStyles.indicator.Render(indicator), entryStyles.line.Render(" "), entryStyles.id.Render(entry.ID)}
	if meta := strings.Join(stateMeta, "  "); meta != "" {
		prefixWidth := processListVisibleWidth(line1Parts)
		line1Parts = append(line1Parts, entryStyles.line.Render(" "), entryStyles.meta.Render(truncateQueuedMessageLine(meta, max(1, width-prefixWidth-1))))
	}

	command := compactProcessCommandPreview(entry.Command)
	line2Parts := []string{entryStyles.rail.Render(railGlyph), entryStyles.line.Render("   "), entryStyles.prompt.Render("$"), entryStyles.line.Render(" "), entryStyles.text.Render(truncateQueuedMessageLine(command, max(1, width-processListContentIndentWidth(railGlyph, entryStyles, entryStyles.prompt.Render("$"), entryStyles.line.Render(" ")))))}

	output := processListOutputPreview(entry.RecentOutput)
	if output == "" {
		output = "<no output yet>"
		line3Parts := []string{entryStyles.rail.Render(railGlyph), entryStyles.line.Render("   "), entryStyles.output.Render(truncateQueuedMessageLine(output, max(1, width-processListContentIndentWidth(railGlyph, entryStyles))))}
		return []string{
			processListPadLine(line1Parts, width, entryStyles.line),
			processListPadLine(line2Parts, width, entryStyles.line),
			processListPadLine(line3Parts, width, entryStyles.line),
			processListPadLine([]string{entryStyles.rail.Render(separatorGlyph)}, width, entryStyles.line),
		}
	}
	line3Parts := []string{entryStyles.rail.Render(railGlyph), entryStyles.line.Render("   "), entryStyles.output.Render(truncateQueuedMessageLine(output, max(1, width-processListContentIndentWidth(railGlyph, entryStyles))))}
	return []string{
		processListPadLine(line1Parts, width, entryStyles.line),
		processListPadLine(line2Parts, width, entryStyles.line),
		processListPadLine(line3Parts, width, entryStyles.line),
		processListPadLine([]string{entryStyles.rail.Render(separatorGlyph)}, width, entryStyles.line),
	}
}

type processListEntryStyles struct {
	rail      lipgloss.Style
	line      lipgloss.Style
	indicator lipgloss.Style
	id        lipgloss.Style
	meta      lipgloss.Style
	prompt    lipgloss.Style
	text      lipgloss.Style
	output    lipgloss.Style
}

func newProcessListEntryStyles(theme string, selected bool, stateColor lipgloss.TerminalColor) processListEntryStyles {
	palette := uiPalette(theme)
	line := lipgloss.NewStyle().Foreground(palette.foreground)
	if selected {
		line = line.Background(palette.modeBg).Foreground(palette.foreground)
	}
	meta := line.Copy().Foreground(palette.muted).Faint(true)
	return processListEntryStyles{
		rail:      line.Copy().Foreground(palette.primary).Bold(true).Faint(false),
		line:      line,
		indicator: line.Copy().Foreground(stateColor).Bold(true).Faint(false),
		id:        line.Copy().Bold(true).Faint(false),
		meta:      meta,
		prompt:    line.Copy().Foreground(palette.primary).Bold(true).Faint(false),
		text:      line.Copy().Faint(false),
		output:    meta,
	}
}

func processListVisibleWidth(parts []string) int {
	width := 0
	for _, part := range parts {
		width += lipgloss.Width(part)
	}
	return width
}

func processListContentIndentWidth(railGlyph string, entryStyles processListEntryStyles, extraParts ...string) int {
	parts := []string{entryStyles.rail.Render(railGlyph), entryStyles.line.Render("   ")}
	parts = append(parts, extraParts...)
	return processListVisibleWidth(parts)
}

func processListPadLine(parts []string, width int, fill lipgloss.Style) string {
	line := strings.Join(parts, "")
	remaining := width - lipgloss.Width(line)
	if remaining <= 0 {
		return line
	}
	return line + fill.Render(strings.Repeat(" ", remaining))
}

func processStateColor(entry clientui.BackgroundProcess, palette uiColors) lipgloss.TerminalColor {
	state := strings.TrimSpace(entry.State)
	switch state {
	case "completed":
		return sharedtheme.DefaultPalette().Status.Success.Adaptive()
	case "failed", "killed":
		return sharedtheme.DefaultPalette().Status.Error.Adaptive()
	case "starting", "running":
		return palette.primary
	default:
		if entry.Running {
			return palette.primary
		}
		if entry.ExitCode != nil && *entry.ExitCode == 0 {
			return sharedtheme.DefaultPalette().Status.Success.Adaptive()
		}
		if entry.ExitCode != nil {
			return sharedtheme.DefaultPalette().Status.Error.Adaptive()
		}
		return palette.muted
	}
}

func renderProcessStateIndicator(entry clientui.BackgroundProcess, spinnerFrame int) string {
	state := strings.TrimSpace(entry.State)
	if state == "starting" || state == "running" || (state == "" && entry.Running) {
		return pendingToolSpinnerFrame(spinnerFrame)
	}
	return padSpinnerIndicator(statusStateCircleGlyph)
}

func processStateLabel(entry clientui.BackgroundProcess) string {
	state := strings.TrimSpace(entry.State)
	if state != "" {
		return state
	}
	if entry.Running {
		return "running"
	}
	if entry.ExitCode != nil && *entry.ExitCode == 0 {
		return "completed"
	}
	if entry.ExitCode != nil {
		return "failed"
	}
	return "queued"
}

func processListWorkdirLabel(workdir string) string {
	trimmed := strings.TrimSpace(workdir)
	if trimmed == "" {
		return ""
	}
	base := filepath.Base(trimmed)
	if base == "." || base == string(filepath.Separator) {
		return trimmed
	}
	return base
}

func compactProcessCommandPreview(command string) string {
	preview := appprocessview.CompactCommandText(command)
	if preview == "" {
		preview = "<no command>"
	}
	previewLines := strings.Split(strings.ReplaceAll(preview, "\r\n", "\n"), "\n")
	preview = strings.TrimSpace(previewLines[0])
	if preview == "" {
		preview = "<no command>"
	}
	normalizedCommand := strings.ReplaceAll(strings.TrimSpace(command), "\r\n", "\n")
	truncated := len(previewLines) > 1 || (strings.Contains(normalizedCommand, "\n") && strings.TrimSpace(normalizedCommand) != preview)
	if truncated && !strings.HasSuffix(preview, " …") {
		preview += " …"
	}
	return preview
}

func processListOutputPreview(output string) string {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for idx := len(lines) - 1; idx >= 0; idx-- {
		line := strings.TrimSpace(lines[idx])
		if line != "" {
			return line
		}
	}
	return ""
}

func processListStartRow(selection, entryCount, contentHeight int) int {
	if selection < 0 || entryCount <= 0 || contentHeight <= 0 {
		return 0
	}
	visibleEntries := contentHeight / processListEntryLines
	if visibleEntries < 1 {
		visibleEntries = 1
	}
	startEntry := 0
	if selection >= visibleEntries {
		startEntry = selection - visibleEntries + 1
	}
	if startEntry >= entryCount {
		startEntry = entryCount - 1
	}
	if startEntry < 0 {
		startEntry = 0
	}
	return startEntry * processListEntryLines
}

func humanAge(t time.Time) string {
	if t.IsZero() {
		return "--"
	}
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func processCountLabel(entries []clientui.BackgroundProcess) string {
	if len(entries) == 0 {
		return ""
	}
	count := 0
	for _, entry := range entries {
		state := strings.TrimSpace(entry.State)
		if entry.Running || state == "starting" || state == "running" {
			count++
		}
	}
	if count == 0 {
		return ""
	}
	return lipgloss.NewStyle().Render(fmt.Sprintf("ps %d", count))
}
