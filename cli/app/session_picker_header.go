package app

import (
	"fmt"
	"strings"

	"core/cli/app/internal/status"
	"core/shared/apicontract"
	"core/shared/serverapi"

	"github.com/charmbracelet/lipgloss"
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
	updateStatus  apicontract.ServerStatusService
}

type sessionPickerHeaderLine struct {
	role  sessionPickerHeaderRowRole
	plain string
}

type sessionPickerHeaderRowRole uint8

const (
	sessionPickerHeaderRowTitle sessionPickerHeaderRowRole = iota + 1
	sessionPickerHeaderRowUpdateAvailable
	sessionPickerHeaderRowUpdateFailed
	sessionPickerHeaderRowMetadata
	sessionPickerHeaderRowOwnedServer
	sessionPickerHeaderRowRemoteServer
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
	return m.styles.headerBox.Width(innerWidth + 2).Render(m.renderHeaderLines(lines, innerWidth))
}

func (m *sessionPickerModel) projectHeaderRows(maxWidth int) []sessionPickerHeaderLine {
	info := m.normalizedHeaderInfo()
	title := "Kent v" + info.Version
	if maxWidth < 1 {
		maxWidth = 1
	}
	lines := []sessionPickerHeaderLine{{
		role:  sessionPickerHeaderRowTitle,
		plain: truncateQueuedMessageLine(title, maxWidth),
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
		role := sessionPickerHeaderRowRemoteServer
		if info.OwnsServer {
			role = sessionPickerHeaderRowOwnedServer
		}
		lines = append(lines, sessionPickerHeaderLine{
			role:  role,
			plain: serverLine,
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
		versions := m.updateStatus.Versions()
		if versions == nil {
			return m.projectInvalidUpdateHeaderRow("available result is missing versions", maxWidth)
		}
		return m.renderUpdateHeaderRow(
			sessionPickerHeaderRowUpdateAvailable,
			"Update available: v"+versions.Latest,
			maxWidth,
		)
	case serverapi.UpdateStatusCheckFailed:
		failure := m.updateStatus.Failure()
		if failure == nil {
			return m.projectInvalidUpdateHeaderRow("failed result is missing a cause", maxWidth)
		}
		return m.renderUpdateHeaderRow(
			sessionPickerHeaderRowUpdateFailed,
			"Update check failed: "+failure.Cause,
			maxWidth,
		)
	default:
		return m.projectInvalidUpdateHeaderRow(
			fmt.Sprintf("unknown validated result kind %q", m.updateStatus.Kind()),
			maxWidth,
		)
	}
}

func (m *sessionPickerModel) projectInvalidUpdateHeaderRow(cause string, maxWidth int) *sessionPickerHeaderLine {
	if m.header.StatusRequest.Settings.Debug {
		panic(fmt.Sprintf(
			"session picker update header invariant violated: kind=%q cause=%q",
			m.updateStatus.Kind(),
			cause,
		))
	}
	return m.renderUpdateHeaderRow(
		sessionPickerHeaderRowUpdateFailed,
		"Update check failed: invalid update status: "+cause,
		maxWidth,
	)
}

func (m *sessionPickerModel) renderUpdateHeaderRow(
	role sessionPickerHeaderRowRole,
	text string,
	maxWidth int,
) *sessionPickerHeaderLine {
	return &sessionPickerHeaderLine{
		role:  role,
		plain: truncateQueuedMessageLine(text, maxWidth),
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
			role:  sessionPickerHeaderRowMetadata,
			plain: row,
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
		role:  sessionPickerHeaderRowMetadata,
		plain: renderedText,
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

func (m *sessionPickerModel) renderHeaderLines(lines []sessionPickerHeaderLine, width int) string {
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		style, valid := m.sessionPickerHeaderRowStyle(line.role)
		if !valid {
			cause := fmt.Sprintf("unknown header row role %d", line.role)
			if m.header.StatusRequest.Settings.Debug {
				panic("session picker header invariant violated: " + cause)
			}
			line.plain = truncateQueuedMessageLine(
				"Update check failed: invalid session picker header: "+cause,
				width,
			)
		}
		styled := style.Render(line.plain)
		rendered = append(rendered, " "+padANSIRight(styled, width)+" ")
	}
	return strings.Join(rendered, "\n")
}

func (m *sessionPickerModel) sessionPickerHeaderRowStyle(role sessionPickerHeaderRowRole) (lipgloss.Style, bool) {
	switch role {
	case sessionPickerHeaderRowTitle:
		return m.styles.headerTitle, true
	case sessionPickerHeaderRowUpdateAvailable, sessionPickerHeaderRowRemoteServer:
		return m.styles.headerSuccess, true
	case sessionPickerHeaderRowUpdateFailed:
		return m.styles.headerError, true
	case sessionPickerHeaderRowMetadata:
		return m.styles.headerText, true
	case sessionPickerHeaderRowOwnedServer:
		return m.styles.headerWarning, true
	default:
		return m.styles.headerError, false
	}
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
