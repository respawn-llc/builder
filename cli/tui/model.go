package tui

import (
	"fmt"
	"strings"

	"core/shared/clientui"
	"core/shared/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type Mode string

const (
	ModeOngoing Mode = "ongoing"
	ModeDetail  Mode = "detail"
)

type ToggleModeMsg struct {
	SkipDetailWarmup bool
}

type SetModeMsg struct {
	Mode             Mode
	SkipDetailWarmup bool
}

type SetViewportLinesMsg struct {
	Lines int
}

type SetViewportSizeMsg struct {
	Lines int
	Width int
}

type SetDetailTranscriptPageMsg struct {
	Page                  clientui.TranscriptPage
	Anchor                DetailTranscriptPageAnchor
	PrependedEntriesCount int
	TrimmedFrontEntries   []clientui.TranscriptCommittedRow
}

type ResetDetailTranscriptMsg struct{}

type DetailTranscriptPageDirection uint8

const (
	DetailTranscriptPageOlder DetailTranscriptPageDirection = iota + 1
	DetailTranscriptPageNewer
)

type RequestDetailTranscriptPageMsg struct {
	Direction DetailTranscriptPageDirection
}

type DetailTranscriptPageAnchor uint8

const (
	DetailTranscriptAnchorDefault DetailTranscriptPageAnchor = iota
	DetailTranscriptAnchorTop
	DetailTranscriptAnchorBottom
	DetailTranscriptAnchorPreserve
	DetailTranscriptAnchorRefresh
)

type DetailSelectionAction uint8

const (
	DetailSelectionActionNone DetailSelectionAction = iota
	DetailSelectionActionExpand
	DetailSelectionActionCollapse
)

type Option func(*Model)

func WithTheme(themeName string) Option {
	return func(m *Model) {
		m.theme = theme.Resolve(themeName)
	}
}

type Model struct {
	mode             Mode
	viewportLines    int
	viewportWidth    int
	theme            string
	detailScroll     int
	detailPageLoaded bool
	detailProjection detailProjection
	expanded         map[int]struct{}
	selected         *int
}

func NewModel(opts ...Option) Model {
	m := Model{
		mode:          ModeOngoing,
		viewportLines: 24,
		viewportWidth: 80,
		theme:         theme.Resolve(""),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&m)
		}
	}
	m.detailProjection = newDetailProjection(m.detailContentWidth(), m.theme)
	return m
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ToggleModeMsg:
		if m.mode == ModeDetail {
			m.mode = ModeOngoing
		} else {
			m.mode = ModeDetail
		}
	case SetModeMsg:
		if msg.Mode == ModeOngoing || msg.Mode == ModeDetail {
			m.mode = msg.Mode
		}
	case SetViewportLinesMsg:
		if msg.Lines > 0 {
			m.viewportLines = msg.Lines
			m.clampDetailScroll()
		}
	case SetViewportSizeMsg:
		previousContentWidth := m.detailContentWidth()
		if msg.Lines > 0 {
			m.viewportLines = msg.Lines
		}
		if msg.Width > 0 {
			m.viewportWidth = msg.Width
		}
		if m.detailContentWidth() != previousContentWidth {
			m.reflowDetailProjection()
		} else {
			m.clampDetailScroll()
		}
	case SetDetailTranscriptPageMsg:
		m.applyDetailTranscriptPage(msg.Page, msg.Anchor, msg.PrependedEntriesCount, msg.TrimmedFrontEntries)
	case ResetDetailTranscriptMsg:
		m.resetDetailTranscript()
	case tea.KeyMsg:
		if m.mode == ModeDetail {
			switch msg.Type {
			case tea.KeyUp:
				if !m.navigateDetail(-1) {
					return m, m.detailPageRequestCmd(DetailTranscriptPageOlder)
				}
			case tea.KeyDown:
				if !m.navigateDetail(1) {
					return m, m.detailPageRequestCmd(DetailTranscriptPageNewer)
				}
			case tea.KeyPgUp:
				if !m.navigateDetail(-maxInt(1, m.viewportLines-1)) {
					return m, m.detailPageRequestCmd(DetailTranscriptPageOlder)
				}
			case tea.KeyPgDown:
				if !m.navigateDetail(maxInt(1, m.viewportLines-1)) {
					return m, m.detailPageRequestCmd(DetailTranscriptPageNewer)
				}
			case tea.KeyEnter:
				m.toggleSelectedDetailEntry()
			case tea.KeyTab:
				m.mode = ModeOngoing
			}
			break
		}
		if msg.String() == "tab" {
			if m.mode == ModeDetail {
				m.mode = ModeOngoing
			} else {
				m.mode = ModeDetail
			}
		}
	case tea.MouseMsg:
		if m.mode == ModeDetail {
			switch msg.Button {
			case tea.MouseButtonWheelUp:
				if !m.navigateDetail(-1) {
					return m, m.detailPageRequestCmd(DetailTranscriptPageOlder)
				}
			case tea.MouseButtonWheelDown:
				if !m.navigateDetail(1) {
					return m, m.detailPageRequestCmd(DetailTranscriptPageNewer)
				}
			}
		}
	}
	return m, nil
}

func (m Model) View() string {
	if m.mode != ModeDetail {
		return ""
	}
	lines := renderDetailProjectedLines(m.detailVisibleProjectedLines(), m.theme)
	if len(lines) == 0 {
		lines = []string{m.detailEmptyLine()}
	}
	return strings.Join(lines, "\n")
}

func (m Model) Mode() Mode {
	return m.mode
}

func (m Model) DetailSelectionAction() DetailSelectionAction {
	if m.mode != ModeDetail {
		return DetailSelectionActionNone
	}
	selected, ok := m.selectedDetailIndex()
	if !ok || selected < 0 || selected >= len(m.detailProjection.entries) {
		return DetailSelectionActionNone
	}
	if !m.detailProjection.entries[selected].presentation().Expandable {
		return DetailSelectionActionNone
	}
	if _, ok := m.expanded[selected]; ok {
		return DetailSelectionActionCollapse
	}
	return DetailSelectionActionExpand
}

func (m *Model) applyDetailTranscriptPage(page clientui.TranscriptPage, anchor DetailTranscriptPageAnchor, prependedEntries int, trimmedFrontEntries []clientui.TranscriptCommittedRow) {
	if !anchor.valid() {
		panic(fmt.Sprintf("invalid detail transcript page anchor: %d", anchor))
	}
	previousScroll := m.detailScroll
	previousSelected, previousSelectedOK := m.selectedDetailIndex()
	previousExpanded := m.expanded
	var previousSelectedRow clientui.TranscriptCommittedRow
	previousSelectedFirstLine := 0
	if previousSelectedOK && previousSelected >= 0 && previousSelected < len(m.detailProjection.entries) {
		previousSelectedRow = m.detailProjection.entries[previousSelected].row()
		if lineRange, ok := m.detailEntryLineRange(previousSelected); ok {
			previousSelectedFirstLine = lineRange.first
		}
	}
	prependedEntries = maxInt(0, prependedEntries)
	visiblePrependedEntries := visibleDetailEntryCount(page.Entries, prependedEntries)
	visibleTrimmedFrontEntries := visibleDetailEntryCount(trimmedFrontEntries, len(trimmedFrontEntries))
	trimmedFrontLineOffset := m.detailLineOffsetForEntryIndex(visibleTrimmedFrontEntries)
	preservedEntryIndexShift := visiblePrependedEntries - visibleTrimmedFrontEntries
	m.detailPageLoaded = true
	if anchor == DetailTranscriptAnchorPreserve {
		if preservedEntryIndexShift != 0 {
			m.expanded = shiftExpandedDetailEntries(previousExpanded, preservedEntryIndexShift)
		} else {
			m.expanded = previousExpanded
		}
	} else {
		m.expanded = nil
	}
	m.detailProjection.replaceSnapshot(page.Entries, m.detailContentWidth(), m.theme, m.expanded)
	if len(m.detailProjection.entries) == 0 {
		m.clearSelectedDetailIndex()
		m.detailScroll = 0
		return
	}
	switch {
	case anchor == DetailTranscriptAnchorRefresh && previousSelectedOK:
		refreshedSelected, ok := m.detailProjection.indexOfRow(previousSelectedRow)
		if !ok {
			m.setSelectedDetailIndex(len(m.detailProjection.entries) - 1)
			m.detailScroll = m.maxDetailScroll()
			return
		}
		m.setSelectedDetailIndex(refreshedSelected)
	case anchor == DetailTranscriptAnchorPreserve && previousSelectedOK:
		m.setSelectedDetailIndex(clampInt(previousSelected+preservedEntryIndexShift, 0, len(m.detailProjection.entries)-1))
	case anchor == DetailTranscriptAnchorTop:
		m.setSelectedDetailIndex(0)
	case anchor == DetailTranscriptAnchorBottom:
		m.setSelectedDetailIndex(len(m.detailProjection.entries) - 1)
	default:
		m.setSelectedDetailIndex(len(m.detailProjection.entries) - 1)
	}
	switch anchor {
	case DetailTranscriptAnchorRefresh:
		refreshedSelected, _ := m.selectedDetailIndex()
		refreshedRange, _ := m.detailEntryLineRange(refreshedSelected)
		m.detailScroll = clampInt(
			refreshedRange.first-(previousSelectedFirstLine-previousScroll),
			0,
			m.maxDetailScroll(),
		)
	case DetailTranscriptAnchorPreserve:
		m.detailScroll = clampInt(previousScroll+m.detailLineOffsetForEntryIndex(visiblePrependedEntries)-trimmedFrontLineOffset, 0, m.maxDetailScroll())
	case DetailTranscriptAnchorTop:
		m.detailScroll = 0
	case DetailTranscriptAnchorBottom:
		m.detailScroll = m.maxDetailScroll()
	default:
		m.detailScroll = m.maxDetailScroll()
	}
}

func (m *Model) resetDetailTranscript() {
	m.detailPageLoaded = false
	m.detailProjection.clear(m.detailContentWidth(), m.theme)
	m.expanded = nil
	m.detailScroll = 0
	m.clearSelectedDetailIndex()
}

func visibleDetailEntryCount(entries []clientui.TranscriptCommittedRow, limit int) int {
	count := 0
	for _, row := range entries[:minInt(maxInt(0, limit), len(entries))] {
		if detailCommittedRowVisible(row) {
			count++
		}
	}
	return count
}

func (anchor DetailTranscriptPageAnchor) valid() bool {
	return anchor == DetailTranscriptAnchorDefault ||
		anchor == DetailTranscriptAnchorTop ||
		anchor == DetailTranscriptAnchorBottom ||
		anchor == DetailTranscriptAnchorPreserve ||
		anchor == DetailTranscriptAnchorRefresh
}

func (m Model) detailEmptyLine() string {
	tokens := theme.ResolvePalette(m.theme)
	if m.detailPageLoaded {
		return lipgloss.NewStyle().Foreground(tokens.App.Muted.Lipgloss()).Render("Transcript detail has no committed rows")
	}
	return lipgloss.NewStyle().Foreground(tokens.App.Muted.Lipgloss()).Render("Transcript detail is waiting for committed rows")
}

func clampInt(value int, minValue int, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func (m Model) detailLineOffsetForEntryIndex(entryIndex int) int {
	if entryIndex <= 0 {
		return 0
	}
	if entryIndex < len(m.detailProjection.ranges) {
		return m.detailProjection.ranges[entryIndex].first
	}
	return len(m.detailProjection.lines)
}

func (m Model) detailContentWidth() int {
	return maxInt(0, maxInt(1, m.viewportWidth)-1)
}

func (m *Model) reflowDetailProjection() {
	if m == nil {
		return
	}
	previousScroll := m.detailScroll
	m.detailProjection.recompile(m.detailContentWidth(), m.theme, m.expanded)
	m.detailScroll = clampInt(previousScroll, 0, m.maxDetailScroll())
	selected, ok := m.selectedDetailIndex()
	if !ok {
		return
	}
	lineRange, ok := m.detailEntryLineRange(selected)
	if !ok {
		return
	}
	viewportEnd := m.detailScroll + maxInt(1, m.viewportLines) - 1
	switch {
	case lineRange.last < m.detailScroll:
		m.detailScroll = lineRange.last
	case lineRange.first > viewportEnd:
		m.detailScroll = lineRange.first - maxInt(1, m.viewportLines) + 1
	}
	m.clampDetailScroll()
}

func shiftExpandedDetailEntries(expanded map[int]struct{}, offset int) map[int]struct{} {
	if len(expanded) == 0 || offset == 0 {
		return expanded
	}
	shifted := make(map[int]struct{}, len(expanded))
	for entryIndex := range expanded {
		nextEntryIndex := entryIndex + offset
		if nextEntryIndex < 0 {
			continue
		}
		shifted[nextEntryIndex] = struct{}{}
	}
	return shifted
}
