package app

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"core/cli/tui"
	"core/server/llm"
	sharedtheme "core/shared/theme"

	bubbleprogress "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	statusLineSeparator        = " · "
	statusLineSpinnerSeparator = " "
)

func (l uiViewLayout) renderStatusLine(width int, style uiStyles) string {
	m := l.model
	indicatorLabel := m.statusLineLabel()
	segments := []statusLineSegment{
		{text: style.meta.Render(l.statusBranchLabel()), priority: 9, side: statusLineSideLeft, order: 1},
		{text: style.meta.Render(l.statusModelLabel()), priority: 8, side: statusLineSideLeft, order: 2},
		{text: l.renderReasoningStatus(statusLineUnboundedWidth), priority: 7, side: statusLineSideLeft, order: 5},
		{text: style.meta.Render(processCountLabel(m.processList.entries)), priority: 5, side: statusLineSideLeft, order: 3},
		{text: l.renderStatusContextBar(style), priority: 4, side: statusLineSideRight, order: 2},
		{text: l.renderStatusContextPercent(style), priority: 3, side: statusLineSideRight, order: 1},
		{text: l.renderDetailSelectionAction(style), priority: 10, side: statusLineSideRight, order: 0},
		{text: l.renderHelpHint(style), priority: 10, side: statusLineSideRight, order: 0},
	}
	segments = compactStatusLineSegments(segments)
	notice := l.renderStatusNotice(statusLineUnboundedWidth)
	for line := l.renderStatusLineCandidate(width, style, statusLineSegmentsWithNotice(segments, notice), indicatorLabel); lipgloss.Width(line) > width; line = l.renderStatusLineCandidate(width, style, statusLineSegmentsWithNotice(segments, notice), indicatorLabel) {
		if removeLowestPriorityStatusSegmentAbove(&segments, 6) {
			continue
		}
		break
	}
	if notice != "" {
		if line := l.renderStatusLineCandidate(width, style, statusLineSegmentsWithNotice(segments, notice), indicatorLabel); lipgloss.Width(line) <= width {
			return padANSIRight(line, width)
		}
		if noticeWidth := l.fittingStatusNoticeWidth(width, style, segments, indicatorLabel); noticeWidth > 0 {
			line := l.renderStatusLineCandidate(width, style, statusLineSegmentsWithNotice(segments, l.renderStatusNotice(noticeWidth)), indicatorLabel)
			return padANSIRight(line, width)
		}
		notice = ""
	}
	if line := l.renderStatusLineCandidate(width, style, segments, indicatorLabel); lipgloss.Width(line) <= width {
		return padANSIRight(line, width)
	}
	removeStatusLineSegmentPriority(&segments, 4)
	if line := l.renderStatusLineCandidate(width, style, segments, indicatorLabel); lipgloss.Width(line) <= width {
		return padANSIRight(line, width)
	}
	indicatorLabel = ""
	line := l.renderStatusLineCandidate(width, style, segments, indicatorLabel)
	if lipgloss.Width(line) <= width {
		return padANSIRight(line, width)
	}
	return padANSIRight(truncateANSIRight(line, width), width)
}

func (l uiViewLayout) renderDetailSelectionAction(style uiStyles) string {
	switch l.model.view.DetailSelectionAction() {
	case tui.DetailSelectionActionExpand:
		return style.meta.Render("Enter to expand")
	case tui.DetailSelectionActionCollapse:
		return style.meta.Render("Enter to collapse")
	default:
		return ""
	}
}

const statusLineUnboundedWidth = 1 << 20

type statusLineSide uint8

const (
	statusLineSideLeft statusLineSide = iota
	statusLineSideRight
)

type statusLineSegment struct {
	text     string
	priority int
	side     statusLineSide
	order    int
}

func (l uiViewLayout) renderStatusLineCandidate(width int, style uiStyles, segments []statusLineSegment, indicatorLabel string) string {
	indicator := renderStatusIndicator(l.model.theme, l.model.statusLinePhase(), l.model.statusLineSpinning(), l.model.spinnerFrame, indicatorLabel)
	left := renderStatusLineLeft(indicator, orderedStatusLineTexts(segments, statusLineSideLeft), style.meta.Render(statusLineSeparator))
	right := strings.Join(orderedStatusLineTexts(segments, statusLineSideRight), style.meta.Render(statusLineSeparator))
	if strings.TrimSpace(ansi.Strip(right)) == "" {
		return left
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func renderStatusLineLeft(spin string, segments []string, separator string) string {
	if len(segments) == 0 {
		return spin
	}
	return spin + statusLineSpinnerSeparator + strings.Join(segments, separator)
}

func compactStatusLineSegments(segments []statusLineSegment) []statusLineSegment {
	out := make([]statusLineSegment, 0, len(segments))
	for _, segment := range segments {
		if strings.TrimSpace(ansi.Strip(segment.text)) == "" {
			continue
		}
		out = append(out, segment)
	}
	return out
}

func statusLineSegmentsWithNotice(segments []statusLineSegment, notice string) []statusLineSegment {
	if strings.TrimSpace(ansi.Strip(notice)) == "" {
		return segments
	}
	out := append([]statusLineSegment(nil), segments...)
	out = append(out, statusLineSegment{text: notice, priority: 6, side: statusLineSideLeft, order: 4})
	return out
}

func removeLowestPriorityStatusSegmentAbove(segments *[]statusLineSegment, priority int) bool {
	if len(*segments) == 0 {
		return false
	}
	index := -1
	for i := range *segments {
		if (*segments)[i].priority <= priority {
			continue
		}
		if index == -1 || (*segments)[i].priority > (*segments)[index].priority {
			index = i
		}
	}
	if index == -1 {
		return false
	}
	*segments = append((*segments)[:index], (*segments)[index+1:]...)
	return true
}

func removeStatusLineSegmentPriority(segments *[]statusLineSegment, priority int) bool {
	for i := range *segments {
		if (*segments)[i].priority != priority {
			continue
		}
		*segments = append((*segments)[:i], (*segments)[i+1:]...)
		return true
	}
	return false
}

func orderedStatusLineTexts(segments []statusLineSegment, side statusLineSide) []string {
	selected := make([]statusLineSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.side == side {
			selected = append(selected, segment)
		}
	}
	for i := 1; i < len(selected); i++ {
		for j := i; j > 0 && selected[j-1].order > selected[j].order; j-- {
			selected[j-1], selected[j] = selected[j], selected[j-1]
		}
	}
	texts := make([]string, 0, len(selected))
	for _, segment := range selected {
		texts = append(texts, segment.text)
	}
	return texts
}

func (l uiViewLayout) fittingStatusNoticeWidth(width int, style uiStyles, segments []statusLineSegment, indicatorLabel string) int {
	full := l.renderStatusNotice(statusLineUnboundedWidth)
	high := min(lipgloss.Width(full), width)
	best := 0
	for low := 1; low <= high; {
		mid := low + (high-low)/2
		line := l.renderStatusLineCandidate(width, style, statusLineSegmentsWithNotice(segments, l.renderStatusNotice(mid)), indicatorLabel)
		if lipgloss.Width(line) <= width {
			best = mid
			low = mid + 1
			continue
		}
		high = mid - 1
	}
	return best
}

func (l uiViewLayout) statusBranchLabel() string {
	git := l.model.status.snapshot.Git
	if !git.Visible || strings.TrimSpace(git.Error) != "" {
		return ""
	}
	branch := strings.TrimSpace(git.Branch)
	if branch == "" || branch == "unknown" {
		return ""
	}
	return branch
}

func (l uiViewLayout) renderStatusNotice(available int) string {
	m := l.model
	if available <= 0 {
		return ""
	}
	text := strings.TrimSpace(m.transientStatus)
	kind := m.transientStatusKind
	if text == "" && strings.TrimSpace(m.worktrees.visibleErrorText()) == "" {
		text = strings.TrimSpace(m.runtimeDisconnectStatusText())
		kind = uiStatusNoticeError
	}
	if text == "" {
		return ""
	}
	text = truncateQueuedMessageLine(text, available)
	return statusNoticeStyle(m.theme, kind).Render(text)
}

func rootCauseErrorText(err error) string {
	if err == nil {
		return ""
	}
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return err.Error()
		}
		err = unwrapped
	}
}

func (l uiViewLayout) renderReasoningStatus(available int) string {
	if available <= 0 {
		return ""
	}
	if text := strings.TrimSpace(l.model.reasoningStatusHeader); text != "" {
		text = truncateQueuedMessageLine(text, available)
		return statusNoticeStyle(l.model.theme, uiStatusNoticeInfo).Render(text)
	}
	return ""
}

func (l uiViewLayout) renderHelpHint(style uiStyles) string {
	if !l.shouldRenderHelpHint() {
		return ""
	}
	return style.meta.Render(l.model.statusHelpHint())
}

func (l uiViewLayout) shouldRenderHelpHint() bool {
	m := l.model
	if !m.canShowHelp() || m.helpVisible {
		return false
	}
	if m.isBusy() || m.isCompacting() || m.isReviewerRunning() {
		return false
	}
	return m.activity == uiActivityIdle
}

func statusNoticeStyle(theme string, kind uiStatusNoticeKind) lipgloss.Style {
	palette := sharedtheme.ResolvePalette(theme)
	color := palette.App.Primary.Lipgloss()
	switch kind {
	case uiStatusNoticeSuccess:
		color = palette.Status.Success.Lipgloss()
	case uiStatusNoticeWarning:
		color = palette.Status.Warning.Lipgloss()
	case uiStatusNoticeError:
		color = palette.Status.Error.Lipgloss()
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true)
}

func (l uiViewLayout) statusModelLabel() string {
	m := l.model
	return statusModelLabelText(
		m.modelName,
		m.thinkingLevel,
		m.fastModeAvailable,
		m.fastModeEnabled,
		m.modelContractLocked,
		m.configuredModelName,
	)
}

func statusModelLabelText(modelName string, thinkingLevel string, fastModeAvailable bool, fastModeEnabled bool, modelContractLocked bool, configuredModelName string) string {
	label := llm.ModelDisplayLabel(modelName, thinkingLevel)
	if fastModeAvailable && fastModeEnabled {
		label += " fast"
	}
	if !modelContractLocked {
		return label
	}
	if strings.TrimSpace(modelName) == strings.TrimSpace(configuredModelName) {
		return label
	}
	return label + " (model locked)"
}

func (l uiViewLayout) renderStatusContextPercent(style uiStyles) string {
	percent, _ := l.renderStatusContextUsageParts(style)
	return percent
}

func (l uiViewLayout) renderStatusContextBar(style uiStyles) string {
	_, bar := l.renderStatusContextUsageParts(style)
	return bar
}

func (l uiViewLayout) renderStatusContextUsageParts(style uiStyles) (string, string) {
	usage := l.model.cachedRuntimeStatus().ContextUsage
	if usage.WindowTokens <= 0 {
		return "", ""
	}
	used := usage.UsedTokens
	if used < 0 {
		used = 0
	}
	rawPercent := int(math.Round((float64(used) * 100) / float64(usage.WindowTokens)))
	barPercent := rawPercent
	if barPercent < 0 {
		barPercent = 0
	}
	if barPercent > 100 {
		barPercent = 100
	}
	barProgress := bubbleprogress.New(
		bubbleprogress.WithWidth(statusContextBarWidth),
		bubbleprogress.WithoutPercentage(),
		bubbleprogress.WithSolidFill(statusContextZone(l.model.theme, rawPercent).TrueColor),
		bubbleprogress.WithFillCharacters('▮', '▯'),
	)
	barProgress.EmptyColor = sharedtheme.ResolvePalette(l.model.theme).Status.ContextEmpty.TrueColor
	bar := barProgress.ViewAs(float64(barPercent) / 100.0)
	label := style.meta.Render(fmt.Sprintf("%d%%", rawPercent))
	return label, bar
}

func statusContextZone(themeName string, percent int) sharedtheme.Color {
	palette := sharedtheme.ResolvePalette(themeName).Status
	if percent < 50 {
		return palette.Success
	}
	if percent < 80 {
		return palette.Warning
	}
	return palette.Error
}

const statusStateCircleGlyph = "●"

func renderStatusIndicator(theme string, phase statusLinePhase, spinning bool, frame int, label string) string {
	glyph := statusStateCircleGlyph
	if spinning {
		glyph = pendingToolSpinnerFrame(frame)
	}
	label = strings.TrimSpace(label)
	if label != "" {
		glyph += " " + label
	}
	return lipgloss.NewStyle().Foreground(statusLinePhaseColor(theme, phase)).Render(glyph)
}

func statusLinePhaseColor(theme string, phase statusLinePhase) lipgloss.TerminalColor {
	palette := sharedtheme.ResolvePalette(theme)
	switch phase {
	case statusLinePhaseSecondary:
		return palette.App.Secondary.Lipgloss()
	case statusLinePhaseSuccess:
		return palette.Status.Success.Lipgloss()
	case statusLinePhaseError:
		return palette.Status.Error.Lipgloss()
	default:
		return palette.App.Primary.Lipgloss()
	}
}
