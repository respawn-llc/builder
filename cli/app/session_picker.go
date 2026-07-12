package app

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"core/shared/clientui"
	sharedtheme "core/shared/theme"

	"charm.land/glamour/v2"
	glamouransi "charm.land/glamour/v2/ansi"
	glamourstyles "charm.land/glamour/v2/styles"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	sessionPickerCreateLabel = "Create a new session"
	defaultPickerWidth       = 80
	defaultPickerHeight      = 24
)

var runSessionPickerFlow = runSessionPicker

type sessionPickerResult struct {
	CreateNew bool
	Session   *clientui.SessionSummary
	Canceled  bool
}

type sessionPickerStyles struct {
	headerFallback lipgloss.Style
	headerBox      lipgloss.Style
	headerTitle    lipgloss.Style
	headerText     lipgloss.Style
	headerWarning  lipgloss.Style
	headerSuccess  lipgloss.Style
	row            lipgloss.Style
	rowSelected    lipgloss.Style
	marker         lipgloss.Style
	markerSelected lipgloss.Style
	preview        lipgloss.Style
	timestamp      lipgloss.Style
}

type sessionPickerModel struct {
	sessions []clientui.SessionSummary
	header   sessionPickerHeaderInfo
	cursor   int
	offset   int
	width    int
	height   int
	theme    string
	styles   sessionPickerStyles
	result   sessionPickerResult
}

func newSessionPickerModel(summaries []clientui.SessionSummary, theme string, header sessionPickerHeaderInfo) *sessionPickerModel {
	items := append([]clientui.SessionSummary(nil), summaries...)
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	m := &sessionPickerModel{
		sessions: items,
		header:   header,
		width:    defaultPickerWidth,
		height:   defaultPickerHeight,
		theme:    theme,
		styles:   newSessionPickerStyles(theme),
	}
	return m
}

func (m *sessionPickerModel) Init() tea.Cmd {
	return collectSessionPickerStatusCmd(m.header)
}

func (m *sessionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch key := msg.(type) {
	case sessionPickerStatusMsg:
		if strings.TrimSpace(key.cwd) != "" {
			m.header.CWD = strings.TrimSpace(key.cwd)
		}
		if strings.TrimSpace(key.branch) != "" {
			m.header.Branch = strings.TrimSpace(key.branch)
		}
		if strings.TrimSpace(key.auth) != "" {
			m.header.Auth = strings.TrimSpace(key.auth)
		}
		if strings.TrimSpace(key.model) != "" {
			m.header.Model = strings.TrimSpace(key.model)
		}
		m.ensureCursorVisible()
		return m, nil
	case tea.WindowSizeMsg:
		if key.Width > 0 {
			m.width = key.Width
		}
		if key.Height > 0 {
			m.height = key.Height
		}
		m.ensureCursorVisible()
		return m, nil
	case tea.KeyMsg:
		switch key.Type {
		case tea.KeyUp:
			m.moveCursor(-1)
		case tea.KeyDown:
			m.moveCursor(1)
		case tea.KeyRunes:
			filtered, _ := stripMouseSGRRunes(key.Runes)
			if len(filtered) == 1 {
				switch filtered[0] {
				case 'k':
					m.moveCursor(-1)
				case 'j':
					m.moveCursor(1)
				case 'n':
					m.result = sessionPickerResult{CreateNew: true}
					return m, tea.Quit
				}
			}
		case tea.KeyEnter:
			if m.cursor == 0 {
				m.result = sessionPickerResult{CreateNew: true}
				return m, tea.Quit
			}
			picked := m.sessions[m.cursor-1]
			m.result = sessionPickerResult{Session: &picked}
			return m, tea.Quit
		case tea.KeyCtrlC:
			m.result = sessionPickerResult{Canceled: true}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *sessionPickerModel) View() string {
	var out strings.Builder
	out.WriteString(m.renderHeader())
	out.WriteString("\n\n")
	visible := m.visibleRowsFromOffset(m.offset)
	for i, row := range visible {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(m.renderRow(row.index, row.showPreview))
		if i+1 < len(visible) {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func (m *sessionPickerModel) moveCursor(delta int) {
	totalItems := m.itemCount()
	if totalItems == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= totalItems {
		m.cursor = totalItems - 1
	}
	m.ensureCursorVisible()
}

func (m *sessionPickerModel) ensureCursorVisible() {
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	for m.offset < m.cursor && !m.rowVisibleFromOffset(m.offset, m.cursor) {
		m.offset++
	}
	if m.offset < 0 {
		m.offset = 0
	}
	for m.offset > 0 && m.rowVisibleFromOffset(m.offset-1, m.cursor) {
		m.offset--
	}
	maxOffset := m.itemCount() - 1
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
}

func (m *sessionPickerModel) visibleLineBudget() int {
	rows := m.height - lipgloss.Height(m.renderHeader()) - 2
	if rows < 1 {
		return 1
	}
	return rows
}

type sessionPickerVisibleRow struct {
	index       int
	showPreview bool
}

func (m *sessionPickerModel) visibleRowsFromOffset(offset int) []sessionPickerVisibleRow {
	budget := m.visibleLineBudget()
	if budget <= 0 {
		return nil
	}
	visible := make([]sessionPickerVisibleRow, 0, m.itemCount())
	for i := offset; i < m.itemCount(); i++ {
		separator := 0
		if len(visible) > 0 {
			separator = 1
		}
		available := budget - separator
		if available < 1 {
			break
		}
		showPreview := m.hasPreview(i) && available >= 2
		rowLines := 1
		if showPreview {
			rowLines = 2
		}
		if rowLines > available {
			if len(visible) == 0 {
				return []sessionPickerVisibleRow{{index: i, showPreview: false}}
			}
			break
		}
		visible = append(visible, sessionPickerVisibleRow{index: i, showPreview: showPreview})
		budget -= separator + rowLines
		if budget == 0 {
			break
		}
	}
	return visible
}

func (m *sessionPickerModel) rowVisibleFromOffset(offset, index int) bool {
	for _, row := range m.visibleRowsFromOffset(offset) {
		if row.index == index {
			return true
		}
	}
	return false
}

func (m *sessionPickerModel) itemCount() int {
	return len(m.sessions) + 1
}

func (m *sessionPickerModel) renderRow(index int, showPreview bool) string {
	selected := index == m.cursor
	title := sessionPickerCreateLabel
	preview := ""
	var timestamp string
	if index > 0 {
		item := m.sessions[index-1]
		title = sessionPickerTitle(item)
		preview = strings.TrimSpace(item.FirstPromptPreview)
		timestamp = humanTime(item.UpdatedAt)
	}

	markerStyle := m.styles.marker
	rowStyle := m.styles.row
	marker := "◈"
	if selected {
		markerStyle = m.styles.markerSelected
		rowStyle = m.styles.rowSelected
	}
	left := markerStyle.Render(marker) + " " + rowStyle.Render(title)
	if timestamp == "" {
		return left
	}
	right := m.styles.timestamp.Render(timestamp)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	titleLine := left + strings.Repeat(" ", gap) + right
	if preview == "" || !showPreview {
		return titleLine
	}
	previewWidth := m.width - 2
	if previewWidth < 1 {
		previewWidth = 1
	}
	previewLine := "  " + m.styles.preview.Render(truncateQueuedMessageLine(preview, previewWidth))
	return titleLine + "\n" + previewLine
}

func sessionPickerTitle(item clientui.SessionSummary) string {
	if title := strings.TrimSpace(item.Name); title != "" {
		return title
	}
	return item.SessionID
}

func (m *sessionPickerModel) hasPreview(index int) bool {
	if index <= 0 {
		return false
	}
	return strings.TrimSpace(m.sessions[index-1].FirstPromptPreview) != ""
}

func newSessionPickerStyles(theme string) sessionPickerStyles {
	palette := uiPalette(theme)
	return sessionPickerStyles{
		headerFallback: lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		headerBox: lipgloss.NewStyle().
			BorderStyle(lipgloss.NormalBorder()).
			BorderForeground(palette.muted),
		headerTitle:    lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		headerText:     lipgloss.NewStyle().Foreground(palette.foreground),
		headerWarning:  lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Warning.Adaptive()).Bold(true),
		headerSuccess:  lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Success.Adaptive()).Bold(true),
		row:            lipgloss.NewStyle().Foreground(palette.foreground),
		rowSelected:    lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		marker:         lipgloss.NewStyle().Foreground(palette.muted),
		markerSelected: lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		preview:        lipgloss.NewStyle().Foreground(palette.muted).Faint(true),
		timestamp:      lipgloss.NewStyle().Foreground(palette.muted).Faint(true),
	}
}

func newStartupMarkdownRendererWithWordWrap(theme string, width int) *glamour.TermRenderer {
	if width < 0 {
		width = 0
	}
	style := startupMarkdownStyle(theme)
	renderer, err := glamour.NewTermRenderer(
		glamour.WithWordWrap(width),
		glamour.WithStyles(style),
	)
	if err != nil {
		return nil
	}
	return renderer
}

func startupMarkdownStyle(theme string) glamouransi.StyleConfig {
	style := glamourstyles.DarkStyleConfig
	if strings.EqualFold(strings.TrimSpace(theme), "light") {
		style = glamourstyles.LightStyleConfig
	}
	zero := uint(0)
	style.Document.Margin = &zero
	return style
}

func humanTime(ts time.Time) string {
	if ts.IsZero() {
		return "unknown"
	}
	return ts.Local().Format("2006-01-02 15:04")
}

func runSessionPicker(summaries []clientui.SessionSummary, theme string, header sessionPickerHeaderInfo) (sessionPickerResult, error) {
	model := newSessionPickerModel(summaries, theme, header)
	program := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return sessionPickerResult{}, err
	}
	picked, ok := finalModel.(*sessionPickerModel)
	if !ok {
		return sessionPickerResult{}, fmt.Errorf("unexpected picker model type %T", finalModel)
	}
	return picked.result, nil
}
