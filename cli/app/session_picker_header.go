package app

import (
	"fmt"
	"strings"

	"core/cli/app/internal/status"
	"core/shared/apicontract"
	"core/shared/serverapi"

	"github.com/mattn/go-runewidth"
)

const sessionPickerHeaderHorizontalFrameWidth = 4

type sessionPickerHeaderInfo struct {
	Version       string
	CWD           string
	Branch        string
	Model         string
	Auth          string
	StatusRequest uiStatusRequest
	AuthManager   status.AuthStateResolver
	OwnsServer    bool
	ServerAddress string
	Notice        *startupPickerNotice
	connection    *interactiveConnectionOwner
	updateStatus  apicontract.ServerStatusService
}

type sessionPickerHeaderLine struct {
	kind   sessionPickerHeaderRowKind
	plain  string
	render string
}

type sessionPickerHeaderRowKind uint8

const (
	sessionPickerHeaderRowVersion sessionPickerHeaderRowKind = iota + 1
	sessionPickerHeaderRowUpdateAvailable
	sessionPickerHeaderRowUpdateFailed
	sessionPickerHeaderRowMetadata
	sessionPickerHeaderRowServer
)

func (m *sessionPickerModel) renderHeader() string {
	maxOuterWidth := m.width
	if maxOuterWidth <= 0 {
		maxOuterWidth = defaultPickerWidth
	}
	maxInnerWidth := maxOuterWidth - sessionPickerHeaderHorizontalFrameWidth
	if maxInnerWidth < 1 {
		maxInnerWidth = 1
	}
	lines := m.projectHeaderRows(maxInnerWidth)
	innerWidth := maxRenderedHeaderLineWidth(lines)
	if innerWidth < 1 {
		innerWidth = 1
	}
	if innerWidth > maxInnerWidth {
		innerWidth = maxInnerWidth
	}
	return m.styles.headerBox.Width(innerWidth + 2).Render(renderHeaderLines(lines, innerWidth))
}

func (m *sessionPickerModel) projectHeaderRows(maxWidth int) []sessionPickerHeaderLine {
	info := m.normalizedHeaderInfo()
	title := "Kent v" + info.Version
	if maxWidth < 1 {
		maxWidth = 1
	}
	lines := []sessionPickerHeaderLine{{
		kind:   sessionPickerHeaderRowVersion,
		plain:  title,
		render: m.styles.headerTitle.Render(truncateQueuedMessageLine(title, maxWidth)),
	}}
	if updateLine := m.projectUpdateHeaderRow(maxWidth); updateLine != nil {
		lines = append(lines, *updateLine)
	}
	lines = append(lines, m.renderHeaderPairLines(gitBranchHeaderSegment(info.Branch), info.CWD, maxWidth)...)
	lines = append(lines, m.renderHeaderPairLines(info.Auth, info.Model, maxWidth)...)
	serverLine := m.serverHeaderLine(info)
	if serverLine != "" {
		if runewidth.StringWidth(serverLine) > maxWidth {
			serverLine = truncateQueuedMessageLine(serverLine, maxWidth)
		}
		style := m.styles.headerSuccess
		if info.OwnsServer {
			style = m.styles.headerWarning
		}
		lines = append(lines, sessionPickerHeaderLine{
			kind:   sessionPickerHeaderRowServer,
			plain:  serverLine,
			render: style.Render(serverLine),
		})
	}
	return lines
}

func (m *sessionPickerModel) projectUpdateHeaderRow(maxWidth int) *sessionPickerHeaderLine {
	if maxWidth < 1 {
		maxWidth = 1
	}
	if m.updateStatus == nil {
		return nil
	}
	switch m.updateStatus.Kind() {
	case serverapi.UpdateStatusCurrent, serverapi.UpdateStatusCheckUnavailable:
		return nil
	case serverapi.UpdateStatusAvailable:
		_, latestVersion, ok := m.updateStatus.Versions()
		if !ok {
			panic("available session-picker update state requires versions")
		}
		return m.renderUpdateHeaderRow(
			sessionPickerHeaderRowUpdateAvailable,
			"Update available: v"+latestVersion,
			maxWidth,
		)
	case serverapi.UpdateStatusCheckFailed:
		cause, ok := m.updateStatus.FailureCause()
		if !ok {
			panic("failed session-picker update state requires a failure cause")
		}
		return m.renderUpdateHeaderRow(
			sessionPickerHeaderRowUpdateFailed,
			"Update check failed: "+cause,
			maxWidth,
		)
	default:
		panic(fmt.Sprintf("unknown validated session-picker update result kind %q", m.updateStatus.Kind()))
	}
}

func (m *sessionPickerModel) renderUpdateHeaderRow(
	kind sessionPickerHeaderRowKind,
	text string,
	maxWidth int,
) *sessionPickerHeaderLine {
	rendered := truncateQueuedMessageLine(text, maxWidth)
	style := m.styles.headerSuccess
	if kind == sessionPickerHeaderRowUpdateFailed {
		style = m.styles.headerError
	}
	return &sessionPickerHeaderLine{
		kind:   kind,
		plain:  rendered,
		render: style.Render(rendered),
	}
}

func (m *sessionPickerModel) normalizedHeaderInfo() sessionPickerHeaderInfo {
	info := m.header
	info.Version = strings.TrimSpace(info.Version)
	if info.Version == "" {
		info.Version = "dev"
	}
	info.CWD = strings.TrimSpace(info.CWD)
	info.Branch = strings.TrimSpace(info.Branch)
	info.Model = strings.TrimSpace(info.Model)
	info.Auth = strings.TrimSpace(info.Auth)
	info.ServerAddress = strings.TrimSpace(info.ServerAddress)
	return info
}

func (m *sessionPickerModel) renderHeaderPairLines(first string, second string, maxWidth int) []sessionPickerHeaderLine {
	first = strings.TrimSpace(first)
	second = strings.TrimSpace(second)
	if first == "" && second == "" {
		return nil
	}
	if first == "" {
		return []sessionPickerHeaderLine{m.renderHeaderTextLine(second, maxWidth)}
	}
	if second == "" {
		return []sessionPickerHeaderLine{m.renderHeaderTextLine(first, maxWidth)}
	}
	row := first + statusLineSeparator + second
	if runewidth.StringWidth(row) <= maxWidth {
		return []sessionPickerHeaderLine{{
			kind:   sessionPickerHeaderRowMetadata,
			plain:  row,
			render: m.styles.headerText.Render(row),
		}}
	}
	return []sessionPickerHeaderLine{
		m.renderHeaderTextLine(first, maxWidth),
		m.renderHeaderTextLine(second, maxWidth),
	}
}

func (m *sessionPickerModel) renderHeaderTextLine(text string, maxWidth int) sessionPickerHeaderLine {
	renderedText := strings.TrimSpace(text)
	if runewidth.StringWidth(renderedText) > maxWidth {
		renderedText = truncateSessionPickerHeaderSegment(renderedText, maxWidth)
	}
	return sessionPickerHeaderLine{
		kind:   sessionPickerHeaderRowMetadata,
		plain:  renderedText,
		render: m.styles.headerText.Render(renderedText),
	}
}

func gitBranchHeaderSegment(branch string) string {
	if trimmed := strings.TrimSpace(branch); trimmed != "" {
		return "git " + trimmed
	}
	return ""
}

func (m *sessionPickerModel) serverHeaderLine(info sessionPickerHeaderInfo) string {
	if info.OwnsServer {
		return "Server owned by this terminal"
	}
	if info.ServerAddress == "" {
		return "Server"
	}
	return "Server at " + info.ServerAddress
}

func maxRenderedHeaderLineWidth(lines []sessionPickerHeaderLine) int {
	maxWidth := 0
	for _, line := range lines {
		if width := runewidth.StringWidth(line.plain); width > maxWidth {
			maxWidth = width
		}
	}
	return maxWidth
}

func renderHeaderLines(lines []sessionPickerHeaderLine, width int) string {
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, " "+padANSIRight(line.render, width)+" ")
	}
	return strings.Join(rendered, "\n")
}

func truncateSessionPickerHeaderSegment(segment string, width int) string {
	if width < 1 {
		return ""
	}
	if runewidth.StringWidth(segment) <= width {
		return segment
	}
	if strings.HasPrefix(segment, "~") || strings.HasPrefix(segment, "/") {
		return truncateMiddleLine(segment, width)
	}
	return truncateQueuedMessageLine(segment, width)
}

func truncateMiddleLine(text string, width int) string {
	if width < 1 {
		return ""
	}
	if runewidth.StringWidth(text) <= width {
		return text
	}
	if width == 1 {
		return "…"
	}
	leftWidth := (width - 1) / 2
	rightWidth := width - 1 - leftWidth
	left := takeRunesByWidth(text, leftWidth)
	right := takeRunesByWidthFromEnd(text, rightWidth)
	if left == "" && right == "" {
		return "…"
	}
	return left + "…" + right
}

func takeRunesByWidth(text string, width int) string {
	if width <= 0 {
		return ""
	}
	var out strings.Builder
	used := 0
	for _, r := range text {
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		if used+rw > width {
			break
		}
		out.WriteRune(r)
		used += rw
	}
	return out.String()
}

func takeRunesByWidthFromEnd(text string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	used := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		rw := runewidth.RuneWidth(runes[i])
		if rw < 1 {
			rw = 1
		}
		if used+rw > width {
			break
		}
		used += rw
		start = i
	}
	return string(runes[start:])
}
