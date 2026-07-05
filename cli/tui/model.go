package tui

import (
	"strings"

	"core/cli/tui/transcriptrender"
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

type ApplyTranscriptMessageMsg struct {
	Message clientui.TranscriptMessage
}

type Option func(*Model)

func WithTheme(themeName string) Option {
	return func(m *Model) {
		m.theme = theme.Resolve(themeName)
	}
}

type Model struct {
	mode          Mode
	viewportLines int
	viewportWidth int
	theme         string
	detailScroll  int
	detailEntries []detailEntry
	expanded      map[int]struct{}
	selected      int
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
	case ApplyTranscriptMessageMsg:
		m.applyTranscriptMessage(msg.Message)
	case tea.KeyMsg:
		if m.mode == ModeDetail {
			switch msg.Type {
			case tea.KeyUp:
				m.detailScroll = clampInt(m.detailScroll-1, 0, m.maxDetailScroll())
			case tea.KeyDown:
				m.detailScroll = clampInt(m.detailScroll+1, 0, m.maxDetailScroll())
			case tea.KeyPgUp:
				m.detailScroll = clampInt(m.detailScroll-maxInt(1, m.viewportLines-1), 0, m.maxDetailScroll())
			case tea.KeyPgDown:
				m.detailScroll = clampInt(m.detailScroll+maxInt(1, m.viewportLines-1), 0, m.maxDetailScroll())
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
	lines := m.detailLines()
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

func (m *Model) applyTranscriptMessage(message clientui.TranscriptMessage) {
	switch message.Kind {
	case clientui.TranscriptMessageHydration:
		m.detailEntries = nil
		if message.Hydration != nil {
			for _, row := range message.Hydration.CommittedRows {
				m.detailEntries = append(m.detailEntries, newDetailEntry(row))
			}
		}
	case clientui.TranscriptMessageCommittedRow:
		if message.CommittedRow != nil {
			m.detailEntries = append(m.detailEntries, newDetailEntry(*message.CommittedRow))
		}
	default:
		return
	}
	if len(m.detailEntries) == 0 {
		m.selected = -1
		m.detailScroll = 0
		return
	}
	if m.selected < 0 || m.selected >= len(m.detailEntries) {
		m.selected = len(m.detailEntries) - 1
	}
	if m.mode == ModeDetail {
		m.detailScroll = m.maxDetailScroll()
	}
}

func (m Model) detailLines() []string {
	width := maxInt(1, m.viewportWidth)
	out := make([]string, 0, len(m.detailEntries)*2)
	for idx, entry := range m.detailEntries {
		if idx > 0 && !sameDetailGroup(m.detailEntries[idx-1], entry) {
			out = append(out, "")
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
		out = append(out, lines...)
	}
	return out
}

func (m Model) decorateSelectedDetailLines(lines []string, entryIndex int) []string {
	if len(lines) == 0 {
		return lines
	}
	out := append([]string(nil), lines...)
	symbol := "▶"
	if _, ok := m.expanded[entryIndex]; ok {
		symbol = "▼"
	}
	out[0] = replaceFirstVisibleSymbol(out[0], lipgloss.NewStyle().Foreground(theme.ResolvePalette(m.theme).App.Primary.Lipgloss()).Render(symbol))
	style := lipgloss.NewStyle().
		Background(theme.ResolvePalette(m.theme).Transcript.SelectionBackground.Lipgloss())
	for idx, line := range out {
		out[idx] = style.Render(transcriptrender.TruncateANSI(line, maxInt(1, m.viewportWidth), false))
	}
	return out
}

func replaceFirstVisibleSymbol(line string, symbol string) string {
	plain := transcriptrender.StripANSI(line)
	trimmed := strings.TrimLeft(plain, " ")
	if trimmed == "" {
		return symbol
	}
	runes := []rune(trimmed)
	if len(runes) <= 1 {
		return symbol
	}
	return symbol + string(runes[1:])
}

func (m Model) detailEmptyLine() string {
	return lipgloss.NewStyle().Faint(true).Render("Transcript detail is waiting for committed rows")
}

func (m Model) maxDetailScroll() int {
	return m.maxScrollForLines(m.detailLines())
}

func (m Model) maxScrollForLines(lines []string) int {
	return maxInt(0, len(lines)-maxInt(1, m.viewportLines))
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
		return
	}
	m.expanded[m.selected] = struct{}{}
}

func sameDetailGroup(left, right detailEntry) bool {
	return left.Kind == right.Kind
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

func (entry detailEntry) row() clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Kind:      entry.Kind,
		User:      entry.User,
		Assistant: entry.Assistant,
		Tool:      entry.Tool,
		Notice:    entry.Notice,
	}
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
