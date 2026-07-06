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
)

const (
	statusLineSeparator        = " · "
	statusLineSpinnerSeparator = " "
)

func (l uiViewLayout) renderStatusLine(width int, style uiStyles) string {
	m := l.model
	indicator := renderStatusIndicator(m.theme, m.statusLinePhase(), m.statusLineSpinning(), m.spinnerFrame, m.statusLineLabel())
	segments := make([]string, 0, 5)
	if modeLabel := l.statusModeLabel(); modeLabel != "" {
		segments = append(segments, style.meta.Render(modeLabel))
	}
	segments = append(segments, style.meta.Render(l.statusModelLabel()))
	if branchLabel := l.statusBranchLabel(); branchLabel != "" {
		segments = append(segments, style.meta.Render(branchLabel))
	}
	if label := processCountLabel(m.processList.entries); label != "" {
		segments = append(segments, style.meta.Render(label))
	}
	if serverOwnershipSection := l.renderServerOwnershipSection(style); serverOwnershipSection != "" {
		segments = append(segments, serverOwnershipSection)
	}
	separator := style.meta.Render(statusLineSeparator)
	left := renderStatusLineLeft(indicator, segments, separator)
	if lipgloss.Width(left) >= width {
		return padANSIRight(truncateANSIRight(left, width), width)
	}
	right := l.renderStatusLineRight(width, left, style)
	if right == "" {
		return padANSIRight(left, width)
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return padANSIRight(left+strings.Repeat(" ", gap)+right, width)
}

func renderStatusLineLeft(spin string, segments []string, separator string) string {
	if len(segments) == 0 {
		return spin
	}
	return spin + statusLineSpinnerSeparator + strings.Join(segments, separator)
}

func (l uiViewLayout) statusModeLabel() string {
	if l.model.rollback.isActive() {
		return "editing"
	}
	return ""
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

func (l uiViewLayout) renderStatusLineRight(width int, left string, style uiStyles) string {
	separator := style.meta.Render(statusLineSeparator)
	separatorWidth := lipgloss.Width(separator)
	available := width - lipgloss.Width(left) - 1
	if available <= 0 {
		return ""
	}
	segments := make([]string, 0, 3)
	used := 0
	prepend := func(segment string) {
		if segment == "" {
			return
		}
		segmentWidth := lipgloss.Width(segment)
		if segmentWidth == 0 {
			return
		}
		additional := segmentWidth
		if len(segments) > 0 {
			additional += separatorWidth
		}
		if used+additional > available {
			return
		}
		used += additional
		segments = append([]string{segment}, segments...)
	}

	prepend(l.renderContextUsage(style))

	headerAvailable := available - used
	if len(segments) > 0 {
		headerAvailable -= separatorWidth
	}
	prepend(l.renderActivityStatus(headerAvailable, style))

	noticeAvailable := available - used
	if len(segments) > 0 {
		noticeAvailable -= separatorWidth
	}
	prepend(l.renderStatusNotice(noticeAvailable))

	prepend(l.renderDetailSelectionAction(style))

	return strings.Join(segments, separator)
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

func (l uiViewLayout) renderStatusNotice(available int) string {
	m := l.model
	if available <= 0 {
		return ""
	}
	text := strings.TrimSpace(m.runtimeDisconnectStatusText())
	kind := uiStatusNoticeError
	if text == "" {
		if strings.TrimSpace(m.worktrees.visibleErrorText()) != "" {
			return ""
		}
		text = strings.TrimSpace(m.transientStatus)
		kind = m.transientStatusKind
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

func (l uiViewLayout) renderActivityStatus(available int, style uiStyles) string {
	if available <= 0 {
		return ""
	}
	if text := strings.TrimSpace(l.model.reasoningStatusHeader); text != "" {
		text = truncateQueuedMessageLine(text, available)
		return statusNoticeStyle(l.model.theme, uiStatusNoticeNeutral).Render(text)
	}
	if l.model.runtimeDisconnectStatusVisible() {
		return ""
	}
	if strings.TrimSpace(l.model.worktrees.visibleErrorText()) != "" {
		return ""
	}
	if strings.TrimSpace(l.model.transientStatus) != "" {
		return ""
	}
	if l.model.activity == uiActivityInterrupted {
		return statusNoticeStyle(l.model.theme, uiStatusNoticeNeutral).Render(truncateQueuedMessageLine("interrupted", available))
	}
	if !l.shouldRenderHelpHint() {
		return ""
	}
	return style.meta.Render(truncateQueuedMessageLine(l.model.statusHelpHint(), available))
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
	palette := uiPalette(theme)
	color := palette.primary
	switch kind {
	case uiStatusNoticeSuccess:
		color = palette.secondary
	case uiStatusNoticeUpdateAvailable:
		color = sharedtheme.DefaultPalette().Status.Success.Adaptive()
	case uiStatusNoticeError:
		color = sharedtheme.DefaultPalette().Status.Error.Adaptive()
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

func (l uiViewLayout) renderServerOwnershipSection(style uiStyles) string {
	if !l.model.statusConfig.OwnsServer {
		return ""
	}
	return style.meta.Render("server owned")
}

func (l uiViewLayout) renderContextUsage(style uiStyles) string {
	usage := l.model.cachedRuntimeStatus().ContextUsage
	if usage.WindowTokens <= 0 {
		return ""
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
	return label + " " + bar
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
