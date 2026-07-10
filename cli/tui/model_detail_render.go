package tui

import (
	"strings"

	"core/cli/tui/transcriptrender"
	"core/shared/theme"

	"github.com/charmbracelet/lipgloss"
)

type detailRail uint8

const (
	detailRailBlank detailRail = iota
	detailRailSelected
)

type detailLineKind uint8

const (
	detailLineContent detailLineKind = iota
	detailLineGroupSpacer
	detailLineVisualSpacer
)

type detailProjectedLine struct {
	Content      transcriptrender.Line
	EntryIndex   *int
	Kind         detailLineKind
	Rail         detailRail
	SelectedFill bool
	VisualSpacer bool
	TargetWidth  int
	ContentWidth int
}

type detailRenderedLine struct {
	Text       string
	EntryIndex *int
}

func (m Model) detailLines() []string {
	return detailRenderedText(m.detailRenderedLines())
}

func (m Model) detailProjectedLines() []detailProjectedLine {
	targetWidth := maxInt(1, m.viewportWidth)
	contentWidth := maxInt(0, targetWidth-1)
	renderWidth := maxInt(1, contentWidth)
	out := make([]detailProjectedLine, 0, (len(m.detailEntries)+1)*2)
	selected, hasSelection := m.selectedDetailIndex()
	for entryIndex, entry := range m.detailEntries {
		if entryIndex > 0 && !sameDetailGroup(m.detailEntries[entryIndex-1], entry) {
			out = append(out, detailProjectedLine{
				Kind:         detailLineGroupSpacer,
				Rail:         detailRailBlank,
				TargetWidth:  targetWidth,
				ContentWidth: contentWidth,
			})
		}
		presentation := entry.presentation(renderWidth, m.theme)
		lines := presentation.Collapsed
		if _, expanded := m.expanded[entryIndex]; expanded && presentation.Expandable {
			lines = presentation.Expanded
		}
		entrySelected := hasSelection && entryIndex == selected
		if entrySelected && presentation.Expandable {
			lines = m.decorateSelectedDetailLines(lines, entryIndex, renderWidth)
		}
		for _, line := range lines {
			indexCopy := entryIndex
			out = append(out, detailProjectedLine{
				Content:      transcriptrender.TruncateLine(line, renderWidth, false),
				EntryIndex:   &indexCopy,
				Kind:         detailLineContent,
				Rail:         detailRailForSelection(entrySelected),
				SelectedFill: entrySelected,
				TargetWidth:  targetWidth,
				ContentWidth: contentWidth,
			})
		}
	}
	return out
}

func detailRailForSelection(selected bool) detailRail {
	if selected {
		return detailRailSelected
	}
	return detailRailBlank
}

func (m Model) detailRenderedLines() []detailRenderedLine {
	projected := m.detailProjectedLines()
	out := make([]detailRenderedLine, 0, len(projected))
	for _, line := range projected {
		out = append(out, detailRenderedLine{
			Text:       renderDetailProjectedLine(line, m.theme),
			EntryIndex: line.EntryIndex,
		})
	}
	return out
}

func (m Model) detailVisibleProjectedLines() []detailProjectedLine {
	lines := m.detailProjectedLines()
	if len(lines) == 0 {
		return nil
	}
	scroll := clampInt(m.detailScroll, 0, m.maxScrollForProjectedLines(lines))
	end := minInt(len(lines), scroll+maxInt(1, m.viewportLines))
	visible := append([]detailProjectedLine(nil), lines[scroll:end]...)
	visible = m.withDetailSelectionSpacers(visible, scroll, m.maxScrollForProjectedLines(lines))
	limit := maxInt(1, m.viewportLines)
	if overflow := len(visible) - limit; overflow > 0 {
		if scroll > 0 && scroll == m.maxScrollForProjectedLines(lines) {
			visible = visible[overflow:]
		} else {
			visible = visible[:limit]
		}
	}
	return visible
}

func (m Model) withDetailSelectionSpacers(lines []detailProjectedLine, scroll int, maxScroll int) []detailProjectedLine {
	selected, hasSelection := m.selectedDetailIndex()
	if !hasSelection || len(lines) == 0 {
		return lines
	}
	firstSelected, lastSelected := -1, -1
	for index, line := range lines {
		if line.EntryIndex == nil || *line.EntryIndex != selected {
			continue
		}
		if firstSelected < 0 {
			firstSelected = index
		}
		lastSelected = index
	}
	if firstSelected < 0 {
		return lines
	}

	spacer := detailSelectionSpacerLine(lines[firstSelected])
	insertBefore := scroll == 0 && detailLineHasOwner(lines, firstSelected-1)
	insertAfter := scroll == maxScroll && detailLineHasOwner(lines, lastSelected+1)
	out := make([]detailProjectedLine, 0, len(lines)+2)
	for index, line := range lines {
		if index == firstSelected && insertBefore {
			out = append(out, spacer)
		}
		replaceWithSpacer := (index == firstSelected-1 && !insertBefore) ||
			(index == lastSelected+1 && !insertAfter)
		if replaceWithSpacer {
			out = append(out, spacer)
			continue
		}
		out = append(out, line)
		if index == lastSelected && insertAfter {
			out = append(out, spacer)
		}
	}
	return out
}

func detailLineHasOwner(lines []detailProjectedLine, index int) bool {
	return index >= 0 && index < len(lines) && lines[index].EntryIndex != nil
}

func detailSelectionSpacerLine(selected detailProjectedLine) detailProjectedLine {
	return detailProjectedLine{
		Kind:         detailLineVisualSpacer,
		Rail:         detailRailSelected,
		SelectedFill: true,
		TargetWidth:  selected.TargetWidth,
		ContentWidth: selected.ContentWidth,
	}
}

func (m Model) maxScrollForProjectedLines(lines []detailProjectedLine) int {
	return maxInt(0, len(lines)-maxInt(1, m.viewportLines))
}

func (m Model) decorateSelectedDetailLines(lines []transcriptrender.Line, entryIndex int, width int) []transcriptrender.Line {
	if len(lines) == 0 || lines[0].LeadingSymbol == nil {
		return lines
	}
	out := append([]transcriptrender.Line(nil), lines...)
	symbol := "▶"
	if _, ok := m.expanded[entryIndex]; ok {
		symbol = "▼"
	}
	out[0] = out[0].WithLeadingSymbolText(symbol)
	for index, line := range out {
		out[index] = transcriptrender.TruncateLine(line, maxInt(1, width), false)
	}
	return out
}

func renderDetailProjectedLine(line detailProjectedLine, themeName string) string {
	palette := theme.ResolvePalette(themeName)
	railText := theme.SelectionRailBlank
	railStyle := lipgloss.NewStyle()
	if line.Rail == detailRailSelected {
		railText = theme.SelectionRailGlyph
		railStyle = railStyle.Foreground(palette.App.Primary.Lipgloss())
	}
	if line.SelectedFill {
		railStyle = railStyle.Background(palette.App.ModeBg.Lipgloss())
	}
	rail := railStyle.Render(railText)
	if line.TargetWidth <= 1 {
		return rail
	}

	content := ""
	if line.Kind == detailLineContent {
		content = renderDetailSemanticLine(line.Content, themeName, line.SelectedFill)
	}
	if !line.SelectedFill {
		return rail + content
	}
	paddingWidth := maxInt(0, line.ContentWidth-lipgloss.Width(content))
	padding := lipgloss.NewStyle().
		Background(palette.App.ModeBg.Lipgloss()).
		Render(strings.Repeat(" ", paddingWidth))
	return rail + content + padding
}

func renderDetailProjectedLines(lines []detailProjectedLine, themeName string) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, renderDetailProjectedLine(line, themeName))
	}
	return out
}

func renderDetailSemanticLine(line transcriptrender.Line, themeName string, selected bool) string {
	var out strings.Builder
	if line.LeadingSymbol != nil {
		out.WriteString(renderDetailSpan(*line.LeadingSymbol, themeName, selected))
	}
	for _, span := range line.Spans {
		out.WriteString(renderDetailSpan(span, themeName, selected))
	}
	return out.String()
}

func renderDetailSpan(span transcriptrender.Span, themeName string, selected bool) string {
	resolved := transcriptrender.ResolveSpanStyle(span, themeName)
	style := lipgloss.NewStyle().Foreground(resolved.Foreground.Lipgloss())
	if selected {
		style = style.Background(theme.ResolvePalette(themeName).App.ModeBg.Lipgloss())
	}
	if resolved.Faint {
		style = style.Faint(true)
	}
	if resolved.Bold {
		style = style.Bold(true)
	}
	if resolved.Italic {
		style = style.Italic(true)
	}
	if resolved.Underline {
		style = style.Underline(true)
	}
	return style.Render(span.Text)
}

func detailRenderedText(lines []detailRenderedLine) []string {
	if len(lines) == 0 {
		return nil
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, line.Text)
	}
	return out
}
