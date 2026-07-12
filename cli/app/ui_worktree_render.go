package app

import (
	"strings"

	"core/cli/app/internal/worktreeui"
	tuiinput "core/cli/tui/input"
	"core/shared/clientui"
	"core/shared/serverapi"
	sharedtheme "core/shared/theme"

	"github.com/charmbracelet/lipgloss"
)

const worktreeOverlayMaxErrorLines = 4

var (
	worktreeOverlayRailGlyph = sharedtheme.SelectionRailGlyph
	worktreeOverlayRailBlank = sharedtheme.SelectionRailBlank
)

func (l uiViewLayout) renderWorktreeOverlay(width, height int, style uiStyles) []string {
	m := l.model
	m.worktrees.inputCursor = uiInputFieldCursor{}
	if m.worktrees.phase == uiWorktreeOverlayPhaseCreate {
		return l.renderWorktreeCreateDialog(width, height, style)
	}
	if m.worktrees.phase == uiWorktreeOverlayPhaseDeleteConfirm {
		return l.renderWorktreeDeleteDialog(width, height, style)
	}
	return l.renderWorktreeList(width, height, style)
}

func (l uiViewLayout) renderWorktreeList(width, height int, style uiStyles) []string {
	m := l.model
	if height < 1 {
		return []string{padRight("", width)}
	}
	header := []string{
		style.brand.Render(truncateQueuedMessageLine("Worktrees", width)),
		style.meta.Render(truncateQueuedMessageLine(worktreeOverlaySummary(m.worktrees.target), width)),
		"",
	}
	remainingHeight := height - len(header) - worktreeOverlayFooterLines
	if remainingHeight < 0 {
		remainingHeight = 0
	}
	content := make([]string, 0, remainingHeight)
	if remainingHeight > 0 {
		switch {
		case m.worktrees.loading:
			content = append(content, style.meta.Render(pendingToolSpinnerFrame(m.spinnerFrame)+" Loading worktrees..."))
		case strings.TrimSpace(m.worktrees.errorText) != "":
			content = append(content, renderWorktreeErrorLines(m.worktrees.errorText, width, lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true), worktreeOverlayMaxErrorLines)...)
		case len(m.worktrees.entries) == 0:
			content = append(content, style.meta.Render("No worktrees."))
		default:
			rows := make([]string, 0, (len(m.worktrees.entries)+1)*worktreeOverlayRowLines)
			rows = append(rows, renderWorktreeCreateRow(m.worktrees.selection == 0, width, m.theme, style)...)
			for idx, item := range m.worktrees.entries {
				rows = append(rows, renderWorktreeEntry(item, idx+1 == m.worktrees.selection, width, m.theme, style)...)
			}
			start := worktreeui.OverlayStartRow(m.worktrees.selection, m.worktreeRowCount(), remainingHeight, worktreeOverlayRowLines)
			end := start + remainingHeight
			if end > len(rows) {
				end = len(rows)
			}
			content = append(content, rows[start:end]...)
		}
		for len(content) < remainingHeight {
			content = append(content, "")
		}
	}
	footer := []string{style.meta.Render(truncateQueuedMessageLine("Esc/q close | Enter switch | c create | d delete | x delete+branch | PgUp/PgDn/Home/End move | r refresh", width))}
	lines := append(append(header, content...), footer...)
	return l.renderChatContentLines(lines, width, style)
}

func worktreeOverlaySummary(target clientui.SessionExecutionTarget) string {
	current := strings.TrimSpace(target.EffectiveWorkdir)
	if current == "" {
		current = strings.TrimSpace(target.WorkspaceRoot)
	}
	if current == "" {
		current = "unknown"
	}
	return "Current workdir: " + current
}

func renderWorktreeCreateRow(selected bool, width int, theme string, style uiStyles) []string {
	p := uiPalette(theme)
	line := lipgloss.NewStyle().Foreground(p.foreground)
	if selected {
		line = line.Background(p.modeBg)
	}
	titleStyle := line.Foreground(p.primary).Bold(true)
	railStyle := line.Foreground(p.primary).Bold(true)
	rail := worktreeOverlayRailBlank
	sep := ""
	if selected {
		rail = worktreeOverlayRailGlyph
		sep = worktreeOverlayRailGlyph
	}
	parts1 := []string{railStyle.Render(rail), line.Render(" "), titleStyle.Render("Create worktree")}
	parts2 := []string{railStyle.Render(rail), line.Render(" ")}
	return []string{
		worktreeOverlayPadLine(parts1, width, line),
		worktreeOverlayPadLine(parts2, width, line),
		worktreeOverlayPadLine([]string{railStyle.Render(sep)}, width, line),
	}
}

func renderWorktreeEntry(item worktreeui.Item, selected bool, width int, theme string, style uiStyles) []string {
	p := uiPalette(theme)
	line := lipgloss.NewStyle().Foreground(p.foreground)
	if selected {
		line = line.Background(p.modeBg)
	}
	titleStyle := line.Bold(true)
	railStyle := line.Foreground(p.primary).Bold(true)
	metaStyle := line.Foreground(p.muted).Faint(true)
	rail := worktreeOverlayRailBlank
	sep := ""
	if selected {
		rail = worktreeOverlayRailGlyph
		sep = worktreeOverlayRailGlyph
	}
	title := truncateQueuedMessageLine(worktreeui.DisplayName(item), max(1, width-2))
	badges := renderWorktreeBadges(item, selected, theme)
	line1 := worktreeOverlayComposeTitleLine(railStyle.Render(rail), title, titleStyle, badges, width, line)
	path := metaStyle.Render(truncateQueuedMessageLine(strings.TrimSpace(item.CanonicalRoot), max(1, width-2)))
	line2 := worktreeOverlayPadLine([]string{railStyle.Render(rail), line.Render(" "), path}, width, line)
	return []string{
		line1,
		line2,
		worktreeOverlayPadLine([]string{railStyle.Render(sep)}, width, line),
	}
}

func renderWorktreeBadges(item worktreeui.Item, selected bool, theme string) []string {
	p := uiPalette(theme)
	badges := make([]string, 0, 4)
	base := lipgloss.NewStyle()
	if selected {
		base = base.Background(p.modeBg)
	}
	badge := func(text string, fg lipgloss.TerminalColor) string {
		return base.Foreground(fg).Bold(true).Render("[" + text + "]")
	}
	if item.IsCurrent {
		badges = append(badges, badge("current", p.secondary))
	}
	if item.IsMain {
		badges = append(badges, badge("main", p.primary))
	}
	if item.Detached {
		badges = append(badges, badge("detached", sharedtheme.DefaultPalette().Status.Warning.Adaptive()))
	} else if branch := worktreeui.BranchName(item); branch != "" {
		badges = append(badges, badge("branch:"+branch, p.foreground))
	}
	if !item.Managed && !item.IsMain {
		badges = append(badges, badge("external", p.muted))
	}
	return badges
}

func worktreeOverlayComposeTitleLine(rail string, title string, titleStyle lipgloss.Style, badges []string, width int, fill lipgloss.Style) string {
	prefix := rail + " "
	available := max(1, width-lipgloss.Width(prefix))
	badgeWidth := 0
	hasBadges := len(badges) > 0
	if len(badges) > 0 {
		badgeWidth = 1
		for index, badge := range badges {
			badgeWidth += lipgloss.Width(badge)
			if index > 0 {
				badgeWidth += 1
			}
		}
	}
	maxTitleWidth := available - badgeWidth
	if maxTitleWidth < 1 {
		maxTitleWidth = available
		hasBadges = false
	}
	parts := []string{rail, fill.Render(" "), titleStyle.Render(truncateQueuedMessageLine(title, maxTitleWidth))}
	if hasBadges {
		parts = append(parts, fill.Render(" "))
		for index, badge := range badges {
			if index > 0 {
				parts = append(parts, fill.Render(" "))
			}
			parts = append(parts, badge)
		}
	}
	return worktreeOverlayPadLine(parts, width, fill)
}

func worktreeOverlayPadLine(parts []string, width int, fill lipgloss.Style) string {
	line := strings.Join(parts, "")
	remaining := width - lipgloss.Width(line)
	if remaining <= 0 {
		return line
	}
	return line + fill.Render(strings.Repeat(" ", remaining))
}

func (l uiViewLayout) renderWorktreeCreateDialog(width, height int, style uiStyles) []string {
	m := l.model
	dialog := m.worktrees.create
	body := make([]string, 0, 32)
	focusedStart := 0
	focusedEnd := -1
	cursorBodyRow := -1
	cursorCol := 0
	addSection := func(field uiWorktreeCreateField, focusable bool, lines []string) {
		if len(lines) == 0 {
			return
		}
		if len(body) > 0 {
			separator := ""
			if focusable && dialog.focus == field {
				separator = lipgloss.NewStyle().Background(uiPalette(m.theme).modeBg).Render(strings.Repeat(" ", width))
			}
			body = append(body, separator)
		}
		start := len(body)
		body = append(body, lines...)
		if focusable && dialog.focus == field {
			focusedStart = start
			focusedEnd = len(body) - 1
			if field == uiWorktreeCreateFieldBranchTarget {
				rendered := renderSingleLineEditor(max(1, width), 1, dialog.branchTarget, "› ", true, 0, "")
				if rendered.Cursor.Visible {
					cursorBodyRow = start + 3 + rendered.Cursor.Row
					cursorCol = rendered.Cursor.Col
				}
			}
			if field == uiWorktreeCreateFieldBaseRef {
				rendered := renderSingleLineEditor(max(1, width), 1, dialog.baseRef, "› ", true, 0, "")
				if rendered.Cursor.Visible {
					helperRows := 0
					if strings.TrimSpace("Used when creating a new branch.") != "" {
						helperRows = 1
					}
					cursorBodyRow = start + 1 + helperRows + 1 + rendered.Cursor.Row
					cursorCol = rendered.Cursor.Col
				}
			}
		}
	}
	addSection(uiWorktreeCreateFieldBranchTarget, false, []string{
		style.brand.Render(truncateQueuedMessageLine("New worktree", width)),
	})
	addSection(uiWorktreeCreateFieldBranchTarget, true, l.renderWorktreeCreateTargetField(width, dialog))
	usesBaseRef := dialog.resolution.Kind == serverapi.WorktreeCreateTargetResolutionKindNewBranch
	addSection(uiWorktreeCreateFieldBaseRef, usesBaseRef, l.renderWorktreeCreateField(width, style, "Base ref", "Used when creating a new branch.", dialog.baseRef, dialog.focus == uiWorktreeCreateFieldBaseRef, usesBaseRef))
	addSection(uiWorktreeCreateFieldActions, true, renderWorktreeCreateActionGroup(width, m.theme, dialog, dialog.focus == uiWorktreeCreateFieldActions))
	footer := make([]string, 0, 3)
	if dialog.submitting {
		footer = append(footer, style.meta.Render(truncateQueuedMessageLine(pendingToolSpinnerFrame(m.spinnerFrame)+" Creating worktree...", width)))
		if dialog.setupEvent != nil && dialog.setupEvent.Phase == serverapi.WorktreeSetupPhaseStarted {
			footer = append(footer,
				style.meta.Render(truncateQueuedMessageLine("Setup script: "+dialog.setupEvent.ScriptPath, width)),
				style.meta.Render(truncateQueuedMessageLine("Setup worktree: "+dialog.setupEvent.WorktreeRoot, width)),
			)
		}
	}
	if trimmed := strings.TrimSpace(dialog.errorText); trimmed != "" {
		footer = append(footer, renderWorktreeErrorLines(trimmed, width, lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true), worktreeOverlayMaxErrorLines)...)
	}
	footer = append(footer, style.meta.Render(truncateQueuedMessageLine("Esc back | Up/Down move | Left/Right change option | Enter activate", width)))
	if len(footer) > height {
		footer = footer[len(footer)-height:]
	}
	bodyHeight := height - len(footer)
	if bodyHeight < 0 {
		bodyHeight = 0
	}
	if focusedEnd < focusedStart {
		focusedStart = 0
		focusedEnd = 0
	}
	visibleStart := worktreeui.DialogVisibleStart(len(body), bodyHeight, focusedStart, focusedEnd)
	visibleEnd := visibleStart + bodyHeight
	if visibleEnd > len(body) {
		visibleEnd = len(body)
	}
	if cursorBodyRow >= visibleStart && cursorBodyRow < visibleEnd {
		m.worktrees.inputCursor = uiInputFieldCursor{Visible: true, Row: cursorBodyRow - visibleStart, Col: cursorCol, Absolute: true}
	}
	lines := append([]string{}, body[visibleStart:visibleEnd]...)
	for len(lines) < bodyHeight {
		lines = append(lines, padRight("", width))
	}
	lines = append(lines, footer...)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, padANSIRight(line, width))
	}
	return out
}

func (l uiViewLayout) renderWorktreeCreateTargetField(width int, dialog uiWorktreeCreateDialogState) []string {
	p := uiPalette(l.model.theme)
	rowStyle := lipgloss.NewStyle()
	if dialog.focus == uiWorktreeCreateFieldBranchTarget {
		rowStyle = rowStyle.Background(p.modeBg)
	}
	labelStyle := rowStyle.Foreground(p.primary).Bold(true)
	if dialog.focus != uiWorktreeCreateFieldBranchTarget {
		labelStyle = rowStyle.Foreground(p.foreground)
	}
	badgeStyle := rowStyle.Foreground(p.muted).Faint(true)
	badgeText := ""
	switch {
	case strings.TrimSpace(dialog.branchTarget.Text()) == "":
		badgeText = ""
	case dialog.resolving:
		badgeText = ""
	case dialog.resolution.Kind == serverapi.WorktreeCreateTargetResolutionKindNewBranch:
		badgeStyle = rowStyle.Foreground(p.secondary).Bold(true)
		badgeText = "✔︎ new branch"
	case dialog.resolution.Kind == serverapi.WorktreeCreateTargetResolutionKindExistingBranch:
		badgeStyle = rowStyle.Foreground(sharedtheme.DefaultPalette().Status.Warning.Adaptive()).Bold(true)
		badgeText = "∴ existing branch"
	case dialog.resolution.Kind == serverapi.WorktreeCreateTargetResolutionKindDetachedRef:
		badgeStyle = rowStyle.Foreground(sharedtheme.DefaultPalette().Status.Warning.Adaptive()).Bold(true)
		badgeText = "∴ detached ref"
	}
	lineStyle := rowStyle.Foreground(p.foreground)
	borderStyle := rowStyle.Foreground(p.muted).Faint(true)
	lines := []string{labelStyle.Render(padANSIRight("Branch or ref", width))}
	lines = append(lines, badgeStyle.Render(padANSIRight(truncateQueuedMessageLine(badgeText, width), width)))
	lines = append(lines, renderSingleLineEditorFramedSoftCursorLines(max(1, width), 1, dialog.branchTarget, "› ", dialog.focus == uiWorktreeCreateFieldBranchTarget && l.model.terminalCursor == nil, lineStyle, borderStyle, 0, "")...)
	return lines
}

func (l uiViewLayout) renderWorktreeCreateField(width int, style uiStyles, label string, helper string, input tuiinput.Editor, focused bool, enabled bool) []string {
	p := uiPalette(l.model.theme)
	rowStyle := lipgloss.NewStyle()
	if focused {
		rowStyle = rowStyle.Background(p.modeBg)
	}
	labelStyle := rowStyle.Foreground(p.primary).Bold(true)
	if !focused {
		labelStyle = rowStyle.Foreground(p.foreground)
	}
	if !enabled {
		labelStyle = rowStyle.Foreground(p.muted).Faint(true)
	}
	lineStyle := rowStyle.Foreground(p.foreground)
	borderStyle := rowStyle.Foreground(p.muted).Faint(true)
	if !enabled {
		lineStyle = rowStyle.Foreground(p.muted).Faint(true)
	}
	contentWidth := max(1, width)
	lines := []string{labelStyle.Render(padANSIRight(truncateQueuedMessageLine(label, contentWidth), contentWidth))}
	if strings.TrimSpace(helper) != "" {
		helperStyle := rowStyle.Foreground(p.muted).Faint(true)
		lines = append(lines, helperStyle.Render(padANSIRight(truncateQueuedMessageLine(helper, contentWidth), contentWidth)))
	}
	lines = append(lines, renderSingleLineEditorFramedSoftCursorLines(contentWidth, 1, input, "› ", focused && enabled && l.model.terminalCursor == nil, lineStyle, borderStyle, 0, "")...)
	return lines
}

func (l uiViewLayout) renderWorktreeDeleteDialog(width, height int, style uiStyles) []string {
	m := l.model
	dialog := m.worktrees.deleteConfirm
	lines := []string{
		style.brand.Render(truncateQueuedMessageLine("Delete "+worktreeui.DisplayName(dialog.target)+"?", width)),
		"",
	}
	body := worktreeui.PreviewLines(dialog.target, dialog.selectedAction)
	for _, line := range body {
		lineStyle := style.chat
		switch line.Kind {
		case worktreeDeletePreviewLineKindHeader:
			lineStyle = lineStyle.Bold(true)
		case worktreeDeletePreviewLineKindWarning:
			lineStyle = lineStyle.Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true)
		}
		lines = append(lines, lineStyle.Render(truncateQueuedMessageLine(line.Text, width)))
	}
	lines = append(lines, "")
	lines = append(lines, renderWorktreeDeleteButtons(width, l.model.theme, dialog)...)
	if dialog.submitting {
		lines = append(lines, "", style.meta.Render(pendingToolSpinnerFrame(m.spinnerFrame)+" Deleting worktree..."))
	}
	if trimmed := strings.TrimSpace(dialog.errorText); trimmed != "" {
		lines = append(lines, "")
		lines = append(lines, renderWorktreeErrorLines(trimmed, width, lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true), worktreeOverlayMaxErrorLines)...)
	}
	return l.renderWorktreeDialogLines(lines, width, height, style)
}

func renderWorktreeErrorLines(text string, width int, lineStyle lipgloss.Style, maxLines int) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || width < 1 || maxLines < 1 {
		return nil
	}
	wrapped := make([]string, 0, maxLines)
	for _, line := range splitPlainLines(strings.TrimRight(trimmed, "\n")) {
		parts := wrapANSIText(line, width)
		if len(parts) == 0 {
			parts = []string{""}
		}
		wrapped = append(wrapped, parts...)
	}
	if len(wrapped) > maxLines {
		wrapped = append([]string(nil), wrapped[:maxLines]...)
		wrapped[len(wrapped)-1] = appendOverflowEllipsis(wrapped[len(wrapped)-1], width)
	}
	out := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		out = append(out, lineStyle.Render(padANSIRight(line, width)))
	}
	return out
}

func appendOverflowEllipsis(line string, width int) string {
	if width < 1 {
		return ""
	}
	if width == 1 {
		return "…"
	}
	trimmed := truncateQueuedMessageLine(line, width-1)
	if strings.HasSuffix(trimmed, "…") {
		trimmed = strings.TrimSuffix(trimmed, "…")
	}
	return trimmed + "…"
}

func renderWorktreeCreateActionGroup(width int, theme string, dialog uiWorktreeCreateDialogState, focused bool) []string {
	p := uiPalette(theme)
	rowStyle := lipgloss.NewStyle()
	if focused {
		rowStyle = rowStyle.Background(p.modeBg)
	}
	selectedStyle := rowStyle.Foreground(p.primary).Bold(true)
	defaultStyle := rowStyle.Foreground(p.muted).Faint(true)
	return []string{renderUIChoiceGroupLineStyled(width, uiChoiceGroupKindButton, []uiChoiceOption{{Label: "Create"}, {Label: "Cancel"}}, int(dialog.action), selectedStyle, defaultStyle)}
}

func (l uiViewLayout) renderWorktreeDialogLines(lines []string, width int, height int, style uiStyles) []string {
	if height < 1 {
		return []string{padRight("", width)}
	}
	if len(lines) < height {
		for len(lines) < height {
			lines = append(lines, "")
		}
	} else if len(lines) > height {
		lines = lines[:height]
	}
	return l.renderChatContentLines(lines, width, style)
}
