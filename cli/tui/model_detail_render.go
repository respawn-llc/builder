package tui

import (
	"fmt"
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
	EntryIndex   int
	Kind         detailLineKind
	Rail         detailRail
	SelectedFill bool
	TargetWidth  int
	ContentWidth int
}

type detailRenderedLine struct {
	Text       string
	EntryIndex int
}

type detailProjectedViewport struct {
	Lines     []detailProjectedLine
	Scroll    int
	MaxScroll int
}

func (m Model) detailProjectedLines() []detailProjectedLine {
	return m.detailProjection.lines
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
	viewport := m.detailProjectedCameraViewport()
	if len(viewport.Lines) == 0 {
		return nil
	}
	visible := append([]detailProjectedLine(nil), viewport.Lines...)
	m.decorateVisibleDetailSelection(visible, viewport.Scroll)
	visible = m.withDetailSelectionSpacers(visible, viewport.Scroll, viewport.MaxScroll)
	limit := maxInt(1, m.viewportLines)
	if overflow := len(visible) - limit; overflow > 0 {
		if viewport.Scroll > 0 && viewport.Scroll == viewport.MaxScroll {
			visible = visible[overflow:]
		} else {
			visible = visible[:limit]
		}
	}
	return visible
}

func (m Model) detailProjectedCameraViewport() detailProjectedViewport {
	lines := m.detailProjection.lines
	if len(lines) == 0 {
		return detailProjectedViewport{}
	}
	maxScroll := m.maxScrollForProjectedLines(lines)
	scroll := clampInt(m.detailScroll, 0, maxScroll)
	end := minInt(len(lines), scroll+maxInt(1, m.viewportLines))
	return detailProjectedViewport{
		Lines:     lines[scroll:end],
		Scroll:    scroll,
		MaxScroll: maxScroll,
	}
}

func (m Model) decorateVisibleDetailSelection(lines []detailProjectedLine, scroll int) {
	selected, hasSelection := m.selectedDetailIndex()
	if !hasSelection || selected < 0 || selected >= len(m.detailProjection.entries) {
		return
	}
	presentation := m.detailProjection.entries[selected].presentation()
	lineRange, hasRange := m.detailEntryLineRange(selected)
	for index := range lines {
		line := &lines[index]
		if line.Kind != detailLineContent || line.EntryIndex != selected {
			continue
		}
		line.Rail = detailRailSelected
		line.SelectedFill = true
		if hasRange && presentation.Expandable && scroll+index == lineRange.first {
			symbol := transcriptrender.DetailCollapsedAffordance
			if _, expanded := m.expanded[selected]; expanded {
				symbol = transcriptrender.DetailExpandedAffordance
			}
			line.Content = line.Content.WithLeadingSymbolText(symbol)
		}
	}
}

func (m Model) withDetailSelectionSpacers(lines []detailProjectedLine, scroll int, maxScroll int) []detailProjectedLine {
	selected, hasSelection := m.selectedDetailIndex()
	if !hasSelection || len(lines) == 0 {
		return lines
	}
	firstSelected, lastSelected := -1, -1
	for index, line := range lines {
		if line.Kind != detailLineContent || line.EntryIndex != selected {
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
	return index >= 0 && index < len(lines) && lines[index].Kind == detailLineContent
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
	symbol := transcriptrender.DetailCollapsedAffordance
	if _, ok := m.expanded[entryIndex]; ok {
		symbol = transcriptrender.DetailExpandedAffordance
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
	background := resolveDetailLineBackground(line.Content.Background, palette, line.SelectedFill)
	if !background.present {
		return rail + content
	}
	paddingWidth := maxInt(0, line.ContentWidth-lipgloss.Width(content))
	padding := lipgloss.NewStyle().
		Background(background.color.Lipgloss()).
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
	palette := theme.ResolvePalette(themeName)
	background := resolveDetailLineBackground(line.Background, palette, selected)
	var out strings.Builder
	if line.LeadingSymbol != nil {
		out.WriteString(renderDetailSpan(*line.LeadingSymbol, themeName, background))
	}
	for _, span := range line.Spans {
		out.WriteString(renderDetailSpan(span, themeName, background))
	}
	return out.String()
}

type detailResolvedBackground struct {
	color   theme.Color
	present bool
}

const detailDiffForegroundPercent uint8 = 20

func resolveDetailLineBackground(
	background transcriptrender.LineBackground,
	palette theme.ResolvedPalette,
	selected bool,
) detailResolvedBackground {
	surfaceBackground := palette.App.ChatBg
	if selected {
		surfaceBackground = palette.App.ModeBg
	}
	switch background {
	case transcriptrender.LineBackgroundDefault:
		if selected {
			return detailResolvedBackground{color: palette.App.ModeBg, present: true}
		}
		return detailResolvedBackground{}
	case transcriptrender.LineBackgroundDiffAdded:
		return detailResolvedBackground{
			color:   theme.BlendColor(palette.Transcript.Success, surfaceBackground, detailDiffForegroundPercent),
			present: true,
		}
	case transcriptrender.LineBackgroundDiffRemoved:
		return detailResolvedBackground{
			color:   theme.BlendColor(palette.Transcript.Error, surfaceBackground, detailDiffForegroundPercent),
			present: true,
		}
	default:
		panic(fmt.Sprintf("render detail line with invalid background semantic %d", background))
	}
}

func renderDetailSpan(
	span transcriptrender.Span,
	themeName string,
	background detailResolvedBackground,
) string {
	style := transcriptSpanStyle(span, themeName)
	if background.present {
		style = style.Background(background.color.Lipgloss())
	}
	return style.Render(span.Text)
}
