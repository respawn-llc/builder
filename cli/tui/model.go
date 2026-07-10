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
	TrimmedFrontEntries   []clientui.ChatEntry
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
	detailEntries    []detailEntry
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
		if msg.Lines > 0 {
			m.viewportLines = msg.Lines
		}
		if msg.Width > 0 {
			m.viewportWidth = msg.Width
		}
		m.clampDetailScroll()
	case SetDetailTranscriptPageMsg:
		m.applyDetailTranscriptPage(msg.Page, msg.Anchor, msg.PrependedEntriesCount, msg.TrimmedFrontEntries)
	case ResetDetailTranscriptMsg:
		m.resetDetailTranscript()
	case tea.KeyMsg:
		if m.mode == ModeDetail {
			switch msg.Type {
			case tea.KeyUp:
				if m.detailScroll == 0 {
					return m, m.detailPageRequestCmd(DetailTranscriptPageOlder)
				}
				m.detailScroll = clampInt(m.detailScroll-1, 0, m.maxDetailScroll())
				m.selectDetailViewportCenter()
			case tea.KeyDown:
				if m.detailScroll >= m.maxDetailScroll() {
					return m, m.detailPageRequestCmd(DetailTranscriptPageNewer)
				}
				m.detailScroll = clampInt(m.detailScroll+1, 0, m.maxDetailScroll())
				m.selectDetailViewportCenter()
			case tea.KeyPgUp:
				if m.detailScroll == 0 {
					return m, m.detailPageRequestCmd(DetailTranscriptPageOlder)
				}
				m.detailScroll = clampInt(m.detailScroll-maxInt(1, m.viewportLines-1), 0, m.maxDetailScroll())
				m.selectDetailViewportCenter()
			case tea.KeyPgDown:
				if m.detailScroll >= m.maxDetailScroll() {
					return m, m.detailPageRequestCmd(DetailTranscriptPageNewer)
				}
				m.detailScroll = clampInt(m.detailScroll+maxInt(1, m.viewportLines-1), 0, m.maxDetailScroll())
				m.selectDetailViewportCenter()
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
	if !ok || selected < 0 || selected >= len(m.detailEntries) {
		return DetailSelectionActionNone
	}
	if !m.detailEntries[selected].presentation(maxInt(1, m.viewportWidth), m.theme).Expandable {
		return DetailSelectionActionNone
	}
	if _, ok := m.expanded[selected]; ok {
		return DetailSelectionActionCollapse
	}
	return DetailSelectionActionExpand
}

func (m *Model) applyDetailTranscriptPage(page clientui.TranscriptPage, anchor DetailTranscriptPageAnchor, prependedEntries int, trimmedFrontEntries []clientui.ChatEntry) {
	if !anchor.valid() {
		panic(fmt.Sprintf("invalid detail transcript page anchor: %d", anchor))
	}
	previousScroll := m.detailScroll
	previousSelected, previousSelectedOK := m.selectedDetailIndex()
	previousExpanded := m.expanded
	prependedEntries = maxInt(0, prependedEntries)
	visiblePrependedEntries := visibleDetailEntryCount(page.Entries, prependedEntries)
	visibleTrimmedFrontEntries := visibleDetailEntryCount(trimmedFrontEntries, len(trimmedFrontEntries))
	trimmedFrontLineOffset := m.detailLineOffsetForEntryIndex(visibleTrimmedFrontEntries)
	preservedEntryIndexShift := visiblePrependedEntries - visibleTrimmedFrontEntries
	m.detailPageLoaded = true
	m.detailEntries = m.detailEntries[:0]
	for _, entry := range page.Entries {
		if detail, ok := detailEntryFromChatEntry(entry); ok {
			m.detailEntries = append(m.detailEntries, detail)
		}
	}
	if anchor == DetailTranscriptAnchorPreserve {
		if preservedEntryIndexShift != 0 {
			m.expanded = shiftExpandedDetailEntries(previousExpanded, preservedEntryIndexShift)
		} else {
			m.expanded = previousExpanded
		}
	} else {
		m.expanded = nil
	}
	if len(m.detailEntries) == 0 {
		m.clearSelectedDetailIndex()
		m.detailScroll = 0
		return
	}
	switch {
	case anchor == DetailTranscriptAnchorPreserve && previousSelectedOK:
		m.setSelectedDetailIndex(clampInt(previousSelected+preservedEntryIndexShift, 0, len(m.detailEntries)-1))
	case anchor == DetailTranscriptAnchorTop:
		m.setSelectedDetailIndex(0)
	case anchor == DetailTranscriptAnchorBottom:
		m.setSelectedDetailIndex(len(m.detailEntries) - 1)
	default:
		m.setSelectedDetailIndex(len(m.detailEntries) - 1)
	}
	switch anchor {
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
	m.detailEntries = nil
	m.expanded = nil
	m.detailScroll = 0
	m.clearSelectedDetailIndex()
}

func visibleDetailEntryCount(entries []clientui.ChatEntry, limit int) int {
	count := 0
	for _, entry := range entries[:minInt(maxInt(0, limit), len(entries))] {
		if _, ok := detailEntryFromChatEntry(entry); ok {
			count++
		}
	}
	return count
}

func (anchor DetailTranscriptPageAnchor) valid() bool {
	return anchor == DetailTranscriptAnchorDefault ||
		anchor == DetailTranscriptAnchorTop ||
		anchor == DetailTranscriptAnchorBottom ||
		anchor == DetailTranscriptAnchorPreserve
}

func (m Model) detailPageRequestCmd(direction DetailTranscriptPageDirection) tea.Cmd {
	if direction != DetailTranscriptPageOlder && direction != DetailTranscriptPageNewer {
		return nil
	}
	return func() tea.Msg { return RequestDetailTranscriptPageMsg{Direction: direction} }
}

func (m Model) detailEmptyLine() string {
	tokens := theme.ResolvePalette(m.theme)
	if m.detailPageLoaded {
		return lipgloss.NewStyle().Foreground(tokens.App.Muted.Lipgloss()).Render("Transcript detail has no committed rows")
	}
	return lipgloss.NewStyle().Foreground(tokens.App.Muted.Lipgloss()).Render("Transcript detail is waiting for committed rows")
}

func (m *Model) toggleSelectedDetailEntry() {
	selected, ok := m.selectedDetailIndex()
	if !ok || selected >= len(m.detailEntries) {
		return
	}
	if !m.detailEntries[selected].presentation(maxInt(1, m.viewportWidth), m.theme).Expandable {
		return
	}
	if m.expanded == nil {
		m.expanded = make(map[int]struct{})
	}
	if _, ok := m.expanded[selected]; ok {
		delete(m.expanded, selected)
	} else {
		m.expanded[selected] = struct{}{}
	}
	m.clampDetailScroll()
}

func (m *Model) clampDetailScroll() {
	if m == nil {
		return
	}
	m.detailScroll = clampInt(m.detailScroll, 0, m.maxDetailScroll())
}

func (m Model) maxDetailScroll() int {
	return m.maxScrollForLines(m.detailLines())
}

func (m Model) maxScrollForLines(lines []string) int {
	return maxInt(0, len(lines)-maxInt(1, m.viewportLines))
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
	for idx, line := range m.detailRenderedLines() {
		if line.EntryIndex != nil && *line.EntryIndex >= entryIndex {
			return idx
		}
	}
	return len(m.detailRenderedLines())
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

func (m *Model) selectDetailViewportCenter() {
	if len(m.detailEntries) == 0 {
		m.clearSelectedDetailIndex()
		return
	}
	lines := m.detailRenderedLines()
	if len(lines) == 0 {
		m.clearSelectedDetailIndex()
		return
	}
	center := clampInt(m.detailScroll+(maxInt(1, m.viewportLines)-1)/2, 0, len(lines)-1)
	var selected *int
	bestDistance := len(lines) + 1
	for idx, line := range lines {
		if line.EntryIndex == nil {
			continue
		}
		distance := absInt(idx - center)
		if distance < bestDistance {
			selectedCopy := *line.EntryIndex
			selected = &selectedCopy
			bestDistance = distance
		}
		if distance == 0 {
			break
		}
	}
	if selected != nil {
		m.setSelectedDetailIndex(*selected)
	}
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
