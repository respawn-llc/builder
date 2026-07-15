package app

import (
	"fmt"
	"strings"

	"core/cli/tui"
	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	sharedtheme "core/shared/theme"

	"github.com/charmbracelet/lipgloss"
)

const (
	sessionPickerCreateLabel = "Create a new session"
	defaultPickerWidth       = 80
	defaultPickerHeight      = 24
)

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

func (m *sessionPickerModel) View() string {
	var out strings.Builder
	out.WriteString(m.renderHeader())
	activeTab := m.activeTab
	m.startupStatus.activeTab = &activeTab
	if status := newSessionPickerStatusSurface(m.startupStatus).RenderStatus(m.width); status != "" {
		out.WriteString("\n\n")
		out.WriteString(status)
	}
	out.WriteString("\n\n")
	tabs := projectSessionPickerTabs(sessionPickerTabsProjectionInput{
		Width: m.width, ActiveTab: m.activeTab, Geometry: terminalGeometryKnown(m.width, m.height),
		Theme: m.theme,
	})
	for index, row := range tabs.Rows {
		if index > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(row)
	}
	out.WriteString("\n\n")

	tab := m.tab(m.activeTab)
	switch tab.bodyPhase {
	case sessionPickerBodyInitialLoading:
		out.WriteString(m.styles.row.Render(pendingToolSpinnerFrame(m.spinnerFrame) + " Loading sessions"))
	case sessionPickerBodyFailed:
		out.WriteString(m.styles.headerWarning.Render("Sessions unavailable. Press Enter to retry."))
	case sessionPickerBodyEmpty:
		if tab.category == sessioncontract.SessionCategorySubagent {
			out.WriteString(m.styles.preview.Render("No subagent sessions yet"))
			break
		}
		out.WriteString(m.renderRow(tab, 0, false))
	case sessionPickerBodyReady:
		visible := m.visibleRowsFromOffset(tab, tab.offset)
		if tab.directional != nil && tab.directional.position.Kind() == serverapi.SessionPagePositionNewer {
			out.WriteString(m.styles.row.Render(pendingToolSpinnerFrame(m.spinnerFrame) + " Loading newer sessions"))
			if len(visible) > 0 {
				out.WriteByte('\n')
			}
		}
		for index, row := range visible {
			if index > 0 {
				out.WriteString("\n\n")
			}
			out.WriteString(m.renderRow(tab, row.index, row.showPreview))
		}
		if tab.directional != nil && tab.directional.position.Kind() == serverapi.SessionPagePositionOlder {
			if len(visible) > 0 {
				out.WriteByte('\n')
			}
			out.WriteString(m.styles.row.Render(pendingToolSpinnerFrame(m.spinnerFrame) + " Loading older sessions"))
		}
	}
	return out.String()
}

func (m *sessionPickerModel) visibleLineBudget() int {
	tabLines := 1
	if m.width >= 40 && m.width < 48 {
		tabLines = 2
	}
	statusLines := 0
	activeTab := m.activeTab
	m.startupStatus.activeTab = &activeTab
	if newSessionPickerStatusSurface(m.startupStatus).RenderStatus(m.width) != "" {
		statusLines = 2
	}
	rows := m.height - lipgloss.Height(m.renderHeader()) - tabLines - 2 - statusLines
	if rows < 1 {
		return 1
	}
	return rows
}

func (m *sessionPickerModel) renderRow(tab *sessionPickerTab, index int, showPreview bool) string {
	selectedIndex := tab.selectedIndex()
	selected := selectedIndex != nil && index == *selectedIndex
	title := sessionPickerCreateLabel
	preview := ""
	var timestamp string
	if !tab.includesCreateRow() || index > 0 {
		sessionIndex := index
		if tab.includesCreateRow() {
			sessionIndex--
		}
		item := tab.sessions()[sessionIndex]
		title = sessionPickerTitle(item)
		preview = strings.TrimSpace(item.FirstPromptPreview)
		timestamp = relativeSessionAge(item.UpdatedAt, m.clock()).String()
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
	return item.SessionID.String()
}

func (m *sessionPickerModel) hasPreview(tab *sessionPickerTab, index int) bool {
	if tab.includesCreateRow() {
		if index == 0 {
			return false
		}
		index--
	}
	sessions := tab.sessions()
	return index >= 0 && index < len(sessions) && strings.TrimSpace(sessions[index].FirstPromptPreview) != ""
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

type startupMarkdownRenderer struct {
	theme            string
	linkPresentation transcriptrender.MarkdownLinkPresentation
}

func newStartupMarkdownRendererWithWordWrap(theme string) *startupMarkdownRenderer {
	return newStartupMarkdownRendererWithLinkPresentation(
		theme,
		currentTerminalCapabilities().MarkdownLinks,
	)
}

func newStartupMarkdownRendererWithLinkPresentation(
	theme string,
	linkPresentation transcriptrender.MarkdownLinkPresentation,
) *startupMarkdownRenderer {
	if !linkPresentation.Valid() {
		panic(fmt.Sprintf("create startup Markdown renderer with invalid link presentation %d", linkPresentation))
	}
	return &startupMarkdownRenderer{
		theme:            theme,
		linkPresentation: linkPresentation,
	}
}

func (r *startupMarkdownRenderer) Render(source string, width int) string {
	if r == nil {
		return ""
	}
	if width < 1 {
		panic(fmt.Sprintf("render startup Markdown with invalid width %d", width))
	}
	lines := tui.RenderStyledMarkdownLines(
		source,
		r.theme,
		width,
		transcriptrender.StyleRoleNoticeForeground,
		r.linkPresentation,
	)
	return strings.Join(lines, "\n") + "\n"
}
