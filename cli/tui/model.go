package tui

import (
	"strings"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/theme"
	"core/shared/transcript"

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
	Page   clientui.TranscriptPage
	Anchor DetailTranscriptPageAnchor
}

type RequestDetailTranscriptPageMsg struct {
	Request clientui.TranscriptPageRequest
}

type DetailTranscriptPageAnchor uint8

const (
	DetailTranscriptAnchorDefault DetailTranscriptPageAnchor = iota
	DetailTranscriptAnchorTop
	DetailTranscriptAnchorBottom
	DetailTranscriptAnchorPreserve
)

type Option func(*Model)

func WithTheme(themeName string) Option {
	return func(m *Model) {
		m.theme = theme.Resolve(themeName)
	}
}

type Model struct {
	mode               Mode
	viewportLines      int
	viewportWidth      int
	theme              string
	detailScroll       int
	detailPageLoaded   bool
	detailOlderCursor  *int64
	detailHasMoreAbove bool
	detailNewerCursor  *int64
	detailHasMoreBelow bool
	detailEntries      []detailEntry
	expanded           map[int]struct{}
	selected           int
}

func NewModel(opts ...Option) Model {
	m := Model{
		mode:          ModeOngoing,
		viewportLines: 24,
		viewportWidth: 80,
		theme:         theme.Resolve(""),
		selected:      -1,
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
		}
	case SetViewportSizeMsg:
		if msg.Lines > 0 {
			m.viewportLines = msg.Lines
		}
		if msg.Width > 0 {
			m.viewportWidth = msg.Width
		}
	case SetDetailTranscriptPageMsg:
		m.applyDetailTranscriptPage(msg.Page, msg.Anchor)
	case tea.KeyMsg:
		if m.mode == ModeDetail {
			switch msg.Type {
			case tea.KeyUp:
				if m.detailScroll == 0 {
					return m, m.detailPageRequestCmd(false)
				}
				m.detailScroll = clampInt(m.detailScroll-1, 0, m.maxDetailScroll())
				m.selectDetailViewportCenter()
			case tea.KeyDown:
				if m.detailScroll >= m.maxDetailScroll() {
					return m, m.detailPageRequestCmd(true)
				}
				m.detailScroll = clampInt(m.detailScroll+1, 0, m.maxDetailScroll())
				m.selectDetailViewportCenter()
			case tea.KeyPgUp:
				if m.detailScroll == 0 {
					return m, m.detailPageRequestCmd(false)
				}
				m.detailScroll = clampInt(m.detailScroll-maxInt(1, m.viewportLines-1), 0, m.maxDetailScroll())
				m.selectDetailViewportCenter()
			case tea.KeyPgDown:
				if m.detailScroll >= m.maxDetailScroll() {
					return m, m.detailPageRequestCmd(true)
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
	lines := detailRenderedText(m.detailRenderedLines())
	if len(lines) == 0 {
		lines = []string{m.detailEmptyLine()}
	}
	if m.detailScroll > m.maxScrollForLines(lines) {
		m.detailScroll = m.maxScrollForLines(lines)
	}
	end := minInt(len(lines), m.detailScroll+maxInt(1, m.viewportLines))
	return strings.Join(lines[m.detailScroll:end], "\n")
}

func (m Model) Mode() Mode {
	return m.mode
}

func (m *Model) applyDetailTranscriptPage(page clientui.TranscriptPage, anchor DetailTranscriptPageAnchor) {
	if !anchor.valid() {
		panic("invalid detail transcript page anchor")
	}
	previousScroll := m.detailScroll
	previousSelected := m.selected
	previousExpanded := m.expanded
	m.detailPageLoaded = true
	m.detailOlderCursor = page.OlderCursor
	m.detailHasMoreAbove = page.HasMoreAbove
	m.detailNewerCursor = page.NewerCursor
	m.detailHasMoreBelow = page.HasMoreBelow
	m.detailEntries = m.detailEntries[:0]
	for _, entry := range page.Entries {
		if row, ok := detailRowFromChatEntry(entry); ok {
			m.detailEntries = append(m.detailEntries, newDetailEntry(row))
		}
	}
	if anchor == DetailTranscriptAnchorPreserve {
		m.expanded = previousExpanded
	} else {
		m.expanded = nil
	}
	if len(m.detailEntries) == 0 {
		m.selected = -1
		m.detailScroll = 0
		return
	}
	if anchor == DetailTranscriptAnchorPreserve && previousSelected >= 0 && previousSelected < len(m.detailEntries) {
		m.selected = previousSelected
	} else if m.selected < 0 || m.selected >= len(m.detailEntries) {
		m.selected = len(m.detailEntries) - 1
	}
	switch anchor {
	case DetailTranscriptAnchorPreserve:
		m.detailScroll = clampInt(previousScroll, 0, m.maxDetailScroll())
	case DetailTranscriptAnchorTop:
		m.detailScroll = 0
	case DetailTranscriptAnchorBottom:
		m.detailScroll = m.maxDetailScroll()
	default:
		if !page.HasMoreBelow || page.NewerCursor == nil {
			m.detailScroll = m.maxDetailScroll()
		} else {
			m.detailScroll = 0
		}
	}
}

func (anchor DetailTranscriptPageAnchor) valid() bool {
	return anchor == DetailTranscriptAnchorDefault ||
		anchor == DetailTranscriptAnchorTop ||
		anchor == DetailTranscriptAnchorBottom ||
		anchor == DetailTranscriptAnchorPreserve
}

func (m Model) detailPageRequestCmd(newer bool) tea.Cmd {
	req := clientui.TranscriptPageRequest{}
	if newer {
		if !m.detailHasMoreBelow || m.detailNewerCursor == nil {
			return nil
		}
		req.NewerCursor = m.detailNewerCursor
	} else {
		if !m.detailHasMoreAbove || m.detailOlderCursor == nil {
			return nil
		}
		req.Cursor = m.detailOlderCursor
	}
	return func() tea.Msg { return RequestDetailTranscriptPageMsg{Request: req} }
}

func (m Model) detailLines() []string {
	return detailRenderedText(m.detailRenderedLines())
}

func (m Model) detailRenderedLines() []detailRenderedLine {
	width := maxInt(1, m.viewportWidth)
	out := make([]detailRenderedLine, 0, (len(m.detailEntries)+1)*2)
	for idx, entry := range m.detailEntries {
		if idx > 0 && !sameDetailGroup(m.detailEntries[idx-1], entry) {
			out = append(out, detailRenderedLine{EntryIndex: -1})
		}
		mode := transcriptrender.ModeDetailCollapsed
		if _, ok := m.expanded[idx]; ok {
			mode = transcriptrender.ModeDetailExpanded
		}
		rendered := transcriptrender.RenderCommittedRow(entry.row(), width, m.theme, mode)
		lines := rendered.Lines
		if idx == m.selected {
			lines = m.decorateSelectedDetailLines(lines, idx)
		}
		out = append(out, detailRenderLines(lines, m.theme, idx == m.selected, idx, width)...)
	}
	return out
}

func (m Model) decorateSelectedDetailLines(lines []transcriptrender.Line, entryIndex int) []transcriptrender.Line {
	if len(lines) == 0 {
		return lines
	}
	out := append([]transcriptrender.Line(nil), lines...)
	symbol := "▶"
	if _, ok := m.expanded[entryIndex]; ok {
		symbol = "▼"
	}
	out[0] = replaceFirstVisibleSymbol(out[0], transcriptrender.Span{Text: symbol, Role: transcriptrender.StyleRoleNotice})
	for idx, line := range out {
		out[idx] = transcriptrender.TruncateLine(line, maxInt(1, m.viewportWidth), false)
	}
	return out
}

func replaceFirstVisibleSymbol(line transcriptrender.Line, symbol transcriptrender.Span) transcriptrender.Line {
	if len(line.Spans) == 0 {
		return transcriptrender.Line{Spans: []transcriptrender.Span{symbol}}
	}
	out := transcriptrender.Line{Spans: append([]transcriptrender.Span(nil), line.Spans...)}
	for idx, span := range out.Spans {
		if strings.TrimSpace(span.Text) == "" {
			continue
		}
		runes := []rune(span.Text)
		if len(runes) == 0 {
			continue
		}
		out.Spans[idx].Text = symbol.Text + string(runes[1:])
		out.Spans[idx].Role = symbol.Role
		out.Spans[idx].Faint = symbol.Faint
		return out
	}
	out.Spans = append([]transcriptrender.Span{symbol}, out.Spans...)
	return out
}

func detailRenderLines(lines []transcriptrender.Line, themeName string, selected bool, entryIndex int, width int) []detailRenderedLine {
	out := make([]detailRenderedLine, 0, len(lines))
	for _, line := range lines {
		out = append(out, detailRenderedLine{
			Text:       renderDetailLine(line, themeName, selected, width),
			EntryIndex: entryIndex,
		})
	}
	return out
}

func renderDetailLine(line transcriptrender.Line, themeName string, selected bool, width int) string {
	out := strings.Builder{}
	for _, span := range line.Spans {
		style := lipgloss.NewStyle().Foreground(detailRoleColor(span.Role, themeName))
		if span.Faint {
			style = style.Faint(true)
		}
		out.WriteString(style.Render(span.Text))
	}
	rendered := out.String()
	if !selected {
		return rendered
	}
	tokens := theme.ResolvePalette(themeName)
	return lipgloss.NewStyle().
		Background(tokens.Transcript.SelectionBackground.Lipgloss()).
		Width(maxInt(1, width)).
		Render(rendered)
}

func detailRoleColor(role transcriptrender.StyleRole, themeName string) lipgloss.TerminalColor {
	return transcriptrender.ColorForRole(transcriptrender.ColorRoleForStyle(role), themeName).Lipgloss()
}

func (m Model) detailEmptyLine() string {
	tokens := theme.ResolvePalette(m.theme)
	if m.detailPageLoaded {
		return lipgloss.NewStyle().Foreground(tokens.App.Muted.Lipgloss()).Render("Transcript detail has no committed rows")
	}
	return lipgloss.NewStyle().Foreground(tokens.App.Muted.Lipgloss()).Render("Transcript detail is waiting for committed rows")
}

func (m *Model) toggleSelectedDetailEntry() {
	if m.selected < 0 || m.selected >= len(m.detailEntries) {
		return
	}
	if m.expanded == nil {
		m.expanded = make(map[int]struct{})
	}
	if _, ok := m.expanded[m.selected]; ok {
		delete(m.expanded, m.selected)
	} else {
		m.expanded[m.selected] = struct{}{}
	}
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

func sameDetailGroup(left, right detailEntry) bool {
	return left.Kind == right.Kind
}

type detailRenderedLine struct {
	Text       string
	EntryIndex int
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

func (m *Model) selectDetailViewportCenter() {
	if len(m.detailEntries) == 0 {
		m.selected = -1
		return
	}
	lines := m.detailRenderedLines()
	if len(lines) == 0 {
		m.selected = -1
		return
	}
	center := clampInt(m.detailScroll+(maxInt(1, m.viewportLines)-1)/2, 0, len(lines)-1)
	selected := -1
	bestDistance := len(lines) + 1
	for idx, line := range lines {
		if line.EntryIndex < 0 {
			continue
		}
		distance := absInt(idx - center)
		if distance < bestDistance {
			selected = line.EntryIndex
			bestDistance = distance
		}
		if distance == 0 {
			break
		}
	}
	if selected >= 0 {
		m.selected = selected
	}
}

type detailEntry struct {
	Kind      clientui.TranscriptRowKind
	User      *clientui.TranscriptUserRow
	Assistant *clientui.TranscriptAssistantRow
	Tool      *clientui.TranscriptToolRow
	Notice    *clientui.TranscriptNoticeRow
}

func newDetailEntry(row clientui.TranscriptCommittedRow) detailEntry {
	return detailEntry{
		Kind:      row.Kind,
		User:      row.User,
		Assistant: row.Assistant,
		Tool:      row.Tool,
		Notice:    row.Notice,
	}
}

func detailRowFromChatEntry(entry clientui.ChatEntry) (clientui.TranscriptCommittedRow, bool) {
	visibility := transcript.NormalizeEntryVisibility(transcript.EntryVisibility(entry.Visibility))
	if visibility == transcript.EntryVisibilityHidden {
		return clientui.TranscriptCommittedRow{}, false
	}
	switch strings.TrimSpace(entry.Role) {
	case "user":
		if strings.TrimSpace(entry.Text) == "" {
			return clientui.TranscriptCommittedRow{}, false
		}
		return clientui.TranscriptCommittedRow{Visibility: clientui.EntryVisibility(visibility), Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: entry.Text}}, true
	case "assistant":
		if strings.TrimSpace(entry.Text) == "" {
			return clientui.TranscriptCommittedRow{}, false
		}
		return clientui.TranscriptCommittedRow{Visibility: clientui.EntryVisibility(visibility), Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: entry.Text, Phase: entry.Phase}}, true
	case "tool_call":
		return clientui.TranscriptCommittedRow{}, false
	case "tool_result_ok", "tool_result_error":
		return clientui.TranscriptCommittedRow{Visibility: clientui.EntryVisibility(visibility), Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{
			ToolCallID:       strings.TrimSpace(entry.ToolCallID),
			ToolName:         detailToolName(entry),
			Text:             entry.Text,
			IsError:          strings.TrimSpace(entry.Role) == "tool_result_error",
			ResultSummary:    strings.TrimSpace(entry.ToolResultSummary),
			CondensedText:    strings.TrimSpace(entry.CondensedText),
			ToolPresentation: entry.ToolCall,
		}}, true
	default:
		return clientui.TranscriptCommittedRow{Visibility: clientui.EntryVisibility(visibility), Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{
			Reason:   clientui.TranscriptNoticeLegacyUntypedNotice,
			Severity: clientui.TranscriptNoticeInfo,
			Data: clientui.TranscriptNoticeData{
				LegacyText:    stringPtr(firstNonEmptyString(entry.CondensedText, entry.Text, entry.CompactLabel, entry.Role)),
				NoticeID:      stringPtr(strings.TrimSpace(entry.NoticeID)),
				MessageType:   entry.MessageType,
				SourcePath:    entry.SourcePath,
				CondensedText: entry.CondensedText,
				CompactLabel:  entry.CompactLabel,
			},
		}}, true
	}
}

func detailToolName(entry clientui.ChatEntry) string {
	if entry.ToolCall != nil && strings.TrimSpace(entry.ToolCall.ToolName) != "" {
		return strings.TrimSpace(entry.ToolCall.ToolName)
	}
	return firstNonEmptyString(entry.ToolCallID, "tool")
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func (entry detailEntry) row() clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Kind:      entry.Kind,
		User:      entry.User,
		Assistant: entry.Assistant,
		Tool:      entry.Tool,
		Notice:    entry.Notice,
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
