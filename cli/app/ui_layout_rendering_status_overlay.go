package app

import (
	sharedtheme "core/shared/theme"
	"fmt"
	"strings"
	"time"

	bubbleprogress "github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

func (l uiViewLayout) renderStatusOverlay(width, height int, _ uiStyles) []string {
	if width < 1 || height < 1 {
		return []string{padRight("", width)}
	}
	content := l.statusOverlayContentLines(width)
	contentHeight := max(1, height)
	maxScroll := max(0, len(content)-contentHeight)
	if l.model.status.scroll > maxScroll {
		l.model.status.scroll = maxScroll
	}
	if l.model.status.scroll < 0 {
		l.model.status.scroll = 0
	}
	start := l.model.status.scroll
	end := min(len(content), start+contentHeight)
	visible := append([]string(nil), content[start:end]...)
	for len(visible) < contentHeight {
		visible = append(visible, padRight("", width))
	}
	return visible
}

type statusOverlayLineStyle uint8

const (
	statusOverlayLineStyleNormal statusOverlayLineStyle = iota
	statusOverlayLineStyleBold
	statusOverlayLineStyleSubtle
)

type statusOverlayLine struct {
	Text  string
	Style statusOverlayLineStyle
}

func statusOverlaySessionLines(snapshot uiStatusSnapshot) []statusOverlayLine {
	lines := make([]statusOverlayLine, 0, 3)
	if sessionName := strings.TrimSpace(snapshot.SessionName); sessionName != "" {
		lines = append(lines, statusOverlayLine{Text: sessionName, Style: statusOverlayLineStyleBold})
	}
	lines = append(lines, statusOverlayLine{Text: "Session ID: " + statusValueOrFallback(snapshot.SessionID, "session unknown"), Style: statusOverlayLineStyleNormal})
	if parentSummary := statusParentSessionSummary(snapshot); parentSummary != "" {
		lines = append(lines, statusOverlayLine{Text: parentSummary, Style: statusOverlayLineStyleSubtle})
	}
	return lines
}

func (l uiViewLayout) statusOverlayContentLines(width int) []string {
	m := l.model
	palette := uiPalette(m.theme)
	titleStyle := lipgloss.NewStyle().Foreground(palette.primary).Bold(true)
	boldStyle := lipgloss.NewStyle().Bold(true)
	subtleStyle := lipgloss.NewStyle().Foreground(palette.muted).Faint(true)
	warningStyle := lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Warning.Adaptive()).Bold(true)
	lines := make([]string, 0, 96)

	appendWrapped := func(text string, lineStyle lipgloss.Style) {
		wrapped := wrapLine(strings.TrimRight(text, " \t"), width)
		if len(wrapped) == 0 {
			lines = append(lines, padRight("", width))
			return
		}
		for _, line := range wrapped {
			lines = append(lines, padANSIRight(lineStyle.Render(line), width))
		}
	}
	appendANSI := func(line string) {
		lines = append(lines, padANSIRight(line, width))
	}
	appendGap := func() {
		if len(lines) == 0 {
			return
		}
		lines = append(lines, padRight("", width))
	}
	appendSectionTitle := func(title string) {
		appendGap()
		appendWrapped(title, titleStyle)
	}

	if strings.TrimSpace(m.status.error) != "" && m.status.snapshot.CollectedAt.IsZero() {
		appendSectionTitle("Status")
		appendWrapped(m.status.error, warningStyle)
		return lines
	}

	snapshot := m.status.snapshot

	appendSectionTitle("Session")
	appendWrapped("CWD: "+statusValueOrFallback(snapshot.Workdir, "<unknown>"), boldStyle)
	appendANSI(l.renderStatusModelLine(width, snapshot.Model.Summary))
	if updateLine := l.renderStatusUpdateLine(width, snapshot.Update); updateLine != "" {
		appendANSI(updateLine)
	}
	for _, line := range statusOverlaySessionLines(snapshot) {
		switch line.Style {
		case statusOverlayLineStyleBold:
			appendWrapped(line.Text, boldStyle)
		case statusOverlayLineStyleSubtle:
			appendWrapped(line.Text, subtleStyle)
		default:
			appendWrapped(line.Text, lipgloss.Style{})
		}
	}

	if l.statusSectionLoading(uiStatusSectionGit) || snapshot.Git.Visible || strings.TrimSpace(snapshot.Git.Error) != "" {
		appendSectionTitle("Git")
		if errorText := strings.TrimSpace(snapshot.Git.Error); errorText != "" {
			appendWrapped(errorText, warningStyle)
		} else if snapshot.Git.Visible {
			appendWrapped(snapshot.Git.Branch, boldStyle)
			appendANSI(l.renderStatusGitSummaryLine(width, snapshot.Git))
		} else {
			appendWrapped("Loading git...", subtleStyle)
		}
	}

	appendSectionTitle("Context")
	appendWrapped(fmt.Sprintf("%s (%s) left of %s", statusPercent(snapshot.Context.AvailableTokens, snapshot.Context.WindowTokens), statusTokenShort(snapshot.Context.AvailableTokens), statusTokenShort(snapshot.Context.WindowTokens)), boldStyle)
	appendWrapped(fmt.Sprintf("Compaction at %s (%s).", statusTokenShort(snapshot.Context.ThresholdTokens), statusPercent(snapshot.Context.ThresholdTokens, snapshot.Context.WindowTokens)), lipgloss.Style{})
	autoCompaction := "off"
	if snapshot.Config.AutoCompaction {
		autoCompaction = "on"
	}
	debug := "off"
	if snapshot.Config.Debug {
		debug = "on"
	}
	appendWrapped("auto-compaction "+autoCompaction, lipgloss.Style{})
	appendWrapped("debug "+debug, lipgloss.Style{})
	appendWrapped(fmt.Sprintf("%d compactions", snapshot.CompactionCount), lipgloss.Style{})

	authSummary := statusVisibleAuthSummary(snapshot.Auth, snapshot.Subscription)
	subscriptionSummary := strings.TrimSpace(snapshot.Subscription.Summary)
	hasSubscriptionRows := len(snapshot.Subscription.Windows) > 0
	showAccountSection := authSummary != "" || subscriptionSummary != "" || hasSubscriptionRows || l.statusSectionLoading(uiStatusSectionAuth)
	if showAccountSection {
		appendSectionTitle("Auth")
		if authSummary != "" {
			appendWrapped(authSummary, boldStyle)
		} else if subscriptionSummary == "" && !hasSubscriptionRows && l.statusSectionLoading(uiStatusSectionAuth) {
			appendWrapped("Loading account...", subtleStyle)
		}
		if subscriptionSummary != "" {
			if strings.TrimSpace(snapshot.Subscription.Error) != "" {
				appendWrapped(subscriptionSummary, warningStyle)
			} else {
				appendWrapped(subscriptionSummary, boldStyle)
			}
		}
		if hasSubscriptionRows {
			labelWidth := statusSubscriptionLabelWidth(snapshot.Subscription.Windows)
			for _, window := range snapshot.Subscription.Windows {
				appendANSI(l.renderStatusSubscriptionLine(width, window, labelWidth))
			}
		} else if subscriptionSummary != "" && l.statusSectionLoading(uiStatusSectionAuth) {
			appendWrapped("Loading limits...", subtleStyle)
		}
		for _, detail := range snapshot.Auth.Details {
			appendWrapped(detail, subtleStyle)
		}
	}

	appendSectionTitle("Config")
	appendWrapped(statusDisplayPath(snapshot.Config.SettingsPath, snapshot.Workdir), subtleStyle)
	if len(snapshot.Config.OverrideSources) > 0 {
		appendWrapped("overrides: "+strings.Join(snapshot.Config.OverrideSources, ", "), lipgloss.Style{})
	}
	appendWrapped("supervisor "+snapshot.Config.Supervisor, lipgloss.Style{})
	if !snapshot.Config.Questions {
		appendWrapped("questions off", lipgloss.Style{})
	}

	loadedSkills, failedSkills := statusPartitionSkills(snapshot.Skills)
	subheaderStyle := lipgloss.NewStyle().Foreground(palette.primary).Bold(true)
	directoryStyle := lipgloss.NewStyle().Foreground(palette.foreground)
	treeStyle := lipgloss.NewStyle().Foreground(palette.muted).Faint(true)
	errorStyle := lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true)
	appendGap()
	appendWrapped(fmt.Sprintf("%d skills", len(snapshot.Skills)), subheaderStyle)
	if l.statusSectionLoading(uiStatusSectionEnvironment) && len(snapshot.Skills) == 0 {
		appendWrapped("Loading skills...", subtleStyle)
	} else {
		visibleSkills := append(append([]uiStatusSkillInspection(nil), loadedSkills...), failedSkills...)
		for _, group := range statusGroupSkillsByDirectory(visibleSkills) {
			appendWrapped(statusDisplayPath(group.Directory, snapshot.Workdir), directoryStyle)
			for idx, skill := range group.Skills {
				branch := "├─"
				if idx == len(group.Skills)-1 {
					branch = "└─"
				}
				line := treeStyle.Render(branch + " ")
				if skill.Loaded {
					line += statusSkillLineStyled(skill, snapshot.SkillTokenCounts, subtleStyle)
				} else {
					line += errorStyle.Render("! ") + statusSkillFailureLine(skill)
				}
				appendANSI(padANSIRight(line, width))
			}
		}
	}

	appendGap()
	appendWrapped(fmt.Sprintf("%d agents files", len(snapshot.AgentsPaths)), boldStyle)
	if l.statusSectionLoading(uiStatusSectionEnvironment) && len(snapshot.AgentsPaths) == 0 {
		appendWrapped("Loading AGENTS.md...", subtleStyle)
	} else {
		for _, path := range snapshot.AgentsPaths {
			appendWrapped(fmt.Sprintf("%s (%s)", statusDisplayPath(path, snapshot.Workdir), statusTokenShort(snapshot.AgentTokenCounts[strings.TrimSpace(path)])), lipgloss.Style{})
		}
	}

	if warning := strings.TrimSpace(snapshot.CollectorWarning); warning != "" {
		appendSectionTitle("Warnings")
		appendWrapped(warning, warningStyle)
	}
	return lines
}

func (l uiViewLayout) renderStatusUpdateLine(width int, update uiStatusUpdateInfo) string {
	if !update.Available || strings.TrimSpace(update.LatestVersion) == "" {
		return ""
	}
	text := "Update: available " + strings.TrimSpace(update.LatestVersion)
	style := lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Success.Adaptive()).Bold(true)
	return padANSIRight(style.Render(truncateQueuedMessageLine(text, width)), width)
}

func (l uiViewLayout) statusSectionLoading(section uiStatusSection) bool {
	return l.model.status.pendingSections != nil && l.model.status.pendingSections[section]
}

func (l uiViewLayout) renderStatusSubscriptionLine(width int, window uiStatusSubscriptionWindow, labelWidth int) string {
	palette := uiPalette(l.model.theme)
	remaining := statusSubscriptionRemaining(window.UsedPercent)
	label := strings.TrimSpace(window.Label)
	if label == "" {
		label = "limit"
	}
	paddedLabel := statusPadRight(label, labelWidth)
	leftStyled := lipgloss.NewStyle().Bold(true).Render(fmt.Sprintf("%.0f%% left", remaining))
	metaParts := make([]string, 0, 2)
	if qualifier := strings.TrimSpace(window.Qualifier); qualifier != "" {
		metaParts = append(metaParts, qualifier)
	}
	resetText := statusSubscriptionResetMeta(window.ResetAt, time.Now())
	if resetText != "" {
		metaParts = append(metaParts, "resets "+resetText)
	}
	metaText := strings.Join(metaParts, " • ")
	metaStyled := ""
	if metaText != "" {
		metaStyled = lipgloss.NewStyle().Foreground(palette.muted).Faint(true).Render("• " + metaText)
	}
	barWidth := statusSubscriptionBarWidthForLine(width, paddedLabel, fmt.Sprintf("%.0f%% left", remaining), metaText)
	bar := l.statusSubscriptionBar(barWidth, remaining)
	line := paddedLabel + " | " + bar + " | " + leftStyled
	if metaStyled != "" {
		line += " " + metaStyled
	}
	if lipgloss.Width(line) <= width {
		return padANSIRight(line, width)
	}
	compact := paddedLabel + " | " + bar + " | " + leftStyled
	return padANSIRight(truncateQueuedMessageLine(compact, width), width)
}

func (l uiViewLayout) renderStatusGitSummaryLine(width int, git uiStatusGitInfo) string {
	palette := uiPalette(l.model.theme)
	cleanText := "clean"
	cleanliness := lipgloss.NewStyle().Bold(true).Foreground(sharedtheme.DefaultPalette().Status.Success.Adaptive()).Render(cleanText)
	if git.Dirty {
		cleanText = "dirty"
		cleanliness = lipgloss.NewStyle().Bold(true).Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Render(cleanText)
	}
	aheadStyle := lipgloss.NewStyle().Foreground(palette.foreground)
	aheadText := fmt.Sprintf("ahead %d", git.Ahead)
	if git.Ahead > 0 {
		aheadStyle = lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Success.Adaptive()).Bold(true)
	}
	behindStyle := lipgloss.NewStyle().Foreground(palette.foreground)
	behindText := fmt.Sprintf("behind %d", git.Behind)
	if git.Behind > 0 {
		behindStyle = lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true)
	}
	joinParts := func(parts ...string) string {
		return strings.Join(parts, " | ")
	}
	plainFull := joinParts(cleanText, aheadText, behindText)
	if lipgloss.Width(plainFull) <= width {
		return padANSIRight(joinParts(cleanliness, aheadStyle.Render(aheadText), behindStyle.Render(behindText)), width)
	}
	shortAheadText := fmt.Sprintf("+%d", git.Ahead)
	shortBehindText := fmt.Sprintf("-%d", git.Behind)
	plainShort := joinParts(cleanText, shortAheadText, shortBehindText)
	if lipgloss.Width(plainShort) <= width {
		return padANSIRight(joinParts(cleanliness, aheadStyle.Render(shortAheadText), behindStyle.Render(shortBehindText)), width)
	}
	if lipgloss.Width(cleanText) <= width {
		return padANSIRight(cleanliness, width)
	}
	line := strings.Join([]string{
		cleanliness,
		aheadStyle.Render(shortAheadText),
		behindStyle.Render(shortBehindText),
	}, " | ")
	return padANSIRight(line, width)
}

func (l uiViewLayout) renderStatusModelLine(width int, summary string) string {
	text := "Model: " + statusValueOrFallback(summary, "<unset>")
	if !strings.HasSuffix(text, " fast") || lipgloss.Width(text) > width {
		return padANSIRight(lipgloss.NewStyle().Bold(true).Render(text), width)
	}
	base := strings.TrimSuffix(text, " fast")
	boldStyle := lipgloss.NewStyle().Bold(true)
	fastStyle := lipgloss.NewStyle().Bold(true).Foreground(sharedtheme.DefaultPalette().Status.Warning.Adaptive())
	return padANSIRight(boldStyle.Render(base)+" "+fastStyle.Render("fast"), width)
}

func (l uiViewLayout) statusSubscriptionBar(width int, remaining float64) string {
	bar := bubbleprogress.New(
		bubbleprogress.WithWidth(max(4, width)),
		bubbleprogress.WithoutPercentage(),
		bubbleprogress.WithSolidFill(statusContextZone(l.model.theme, int(100-remaining)).TrueColor),
		bubbleprogress.WithFillCharacters('▮', '▯'),
	)
	bar.EmptyColor = sharedtheme.ResolvePalette(l.model.theme).Status.ContextEmpty.TrueColor
	return bar.ViewAs(remaining / 100.0)
}
