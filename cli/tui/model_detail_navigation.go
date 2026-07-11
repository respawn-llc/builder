package tui

import "maps"

import tea "github.com/charmbracelet/bubbletea"

type detailLineRange struct {
	first int
	last  int
}

func (m Model) detailPageRequestCmd(direction DetailTranscriptPageDirection) tea.Cmd {
	if direction != DetailTranscriptPageOlder && direction != DetailTranscriptPageNewer {
		return nil
	}
	return func() tea.Msg { return RequestDetailTranscriptPageMsg{Direction: direction} }
}

func (m *Model) toggleSelectedDetailEntry() {
	selected, ok := m.selectedDetailIndex()
	if !ok || selected >= len(m.detailProjection.entries) {
		return
	}
	if !m.detailProjection.entries[selected].presentation().Expandable {
		return
	}
	m.expanded = maps.Clone(m.expanded)
	if _, ok := m.expanded[selected]; ok {
		delete(m.expanded, selected)
	} else {
		if m.expanded == nil {
			m.expanded = make(map[int]struct{})
		}
		m.expanded[selected] = struct{}{}
	}
	m.detailProjection.rebuildLines(m.detailContentWidth(), m.expanded)
	m.scrollSelectedDetailEntryIntoView()
}

func (m *Model) clampDetailScroll() {
	if m == nil {
		return
	}
	m.detailScroll = clampInt(m.detailScroll, 0, m.maxDetailScroll())
}

func (m Model) maxDetailScroll() int {
	return m.maxScrollForProjectedLines(m.detailProjection.lines)
}

func (m *Model) navigateDetail(delta int) bool {
	if m == nil || delta == 0 {
		return false
	}
	if m.moveDetailSelectionTowardCenter(delta) {
		return true
	}
	beforeScroll := m.detailScroll
	m.detailScroll = clampInt(m.detailScroll+delta, 0, m.maxDetailScroll())
	if m.detailScroll != beforeScroll {
		m.selectDetailViewportCenter()
		return true
	}
	return m.moveDetailSelectionWithinViewport(delta)
}

func (m *Model) moveDetailSelectionTowardCenter(delta int) bool {
	if m == nil || (delta != -1 && delta != 1) {
		return false
	}
	selectedRange, ok := m.visibleDetailEntryLineRange()
	if !ok {
		return false
	}
	center, ok := m.centerVisibleDetailEntry()
	if !ok {
		return false
	}
	centerRange, ok := m.visibleDetailEntryLineRangeFor(center)
	if !ok {
		return false
	}
	centerLine := (centerRange.first + centerRange.last) / 2
	if delta < 0 && selectedRange.first <= centerLine {
		return false
	}
	if delta > 0 && selectedRange.last >= centerLine {
		return false
	}
	return m.moveDetailSelectionWithinViewport(delta)
}

func (m *Model) moveDetailSelectionWithinViewport(delta int) bool {
	if m == nil || delta == 0 {
		return false
	}
	selectedRange, ok := m.visibleDetailEntryLineRange()
	if !ok {
		before, hadSelection := m.selectedDetailIndex()
		m.selectDetailViewportCenter()
		after, hasSelection := m.selectedDetailIndex()
		return hadSelection != hasSelection || before != after
	}
	startLine := selectedRange.first - 1
	if delta > 0 {
		startLine = selectedRange.last + 1
	}
	lines := m.detailNavigationViewportLines()
	selected, _ := m.selectedDetailIndex()
	for lineIndex := startLine; lineIndex >= 0 && lineIndex < len(lines); lineIndex += delta {
		line := lines[lineIndex]
		if line.Kind != detailLineContent || line.EntryIndex == selected {
			continue
		}
		m.setSelectedDetailIndex(line.EntryIndex)
		return true
	}
	return false
}

func (m Model) visibleDetailEntryLineRange() (detailLineRange, bool) {
	selected, ok := m.selectedDetailIndex()
	if !ok {
		return detailLineRange{}, false
	}
	return m.visibleDetailEntryLineRangeFor(selected)
}

func (m Model) visibleDetailEntryLineRangeFor(entryIndex int) (detailLineRange, bool) {
	return detailEntryLineRangeIn(m.detailNavigationViewportLines(), entryIndex)
}

func detailEntryLineRangeIn(lines []detailProjectedLine, entryIndex int) (detailLineRange, bool) {
	lineRange := detailLineRange{}
	found := false
	for lineIndex, line := range lines {
		if line.Kind != detailLineContent || line.EntryIndex != entryIndex {
			continue
		}
		if !found {
			lineRange.first = lineIndex
			found = true
		}
		lineRange.last = lineIndex
	}
	return lineRange, found
}

func (m Model) centerVisibleDetailEntry() (int, bool) {
	lines := m.detailNavigationViewportLines()
	if len(lines) == 0 {
		return 0, false
	}
	anchor := minInt(len(lines)-1, maxInt(1, m.viewportLines)/2)
	bestEntry := 0
	bestDistance := len(lines) + 1
	found := false
	for lineIndex, line := range lines {
		if line.Kind != detailLineContent {
			continue
		}
		distance := absInt(lineIndex - anchor)
		if distance >= bestDistance {
			continue
		}
		bestEntry = line.EntryIndex
		bestDistance = distance
		found = true
	}
	return bestEntry, found
}

func (m *Model) selectDetailViewportCenter() {
	if m == nil {
		return
	}
	center, ok := m.centerVisibleDetailEntry()
	if !ok {
		m.clearSelectedDetailIndex()
		return
	}
	m.setSelectedDetailIndex(center)
}

func (m Model) selectedDetailIndex() (int, bool) {
	if m.selected == nil {
		return 0, false
	}
	return *m.selected, true
}

func (m *Model) setSelectedDetailIndex(index int) {
	indexCopy := index
	m.selected = &indexCopy
}

func (m *Model) clearSelectedDetailIndex() {
	m.selected = nil
}

func (m Model) detailNavigationViewportLines() []detailProjectedLine {
	return m.detailProjectedCameraViewport().Lines
}

func (m *Model) scrollSelectedDetailEntryIntoView() {
	if m == nil {
		return
	}
	selected, ok := m.selectedDetailIndex()
	if !ok {
		return
	}
	lineRange, ok := m.detailEntryLineRange(selected)
	if !ok {
		return
	}
	if lineRange.first < m.detailScroll {
		m.detailScroll = lineRange.first
	} else if lineRange.last >= m.detailScroll+maxInt(1, m.viewportLines) {
		m.detailScroll = maxInt(0, lineRange.last-maxInt(1, m.viewportLines)+1)
	}
	m.clampDetailScroll()
}

func (m Model) detailEntryLineRange(entryIndex int) (detailLineRange, bool) {
	if entryIndex < 0 || entryIndex >= len(m.detailProjection.ranges) {
		return detailLineRange{}, false
	}
	return m.detailProjection.ranges[entryIndex], true
}
