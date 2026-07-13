package app

import (
	"fmt"
	"strings"

	tuiinput "core/cli/tui/input"
	"core/shared/config"
	sharedtheme "core/shared/theme"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

type onboardingStyles struct {
	title          lipgloss.Style
	body           lipgloss.Style
	helper         lipgloss.Style
	footer         lipgloss.Style
	option         lipgloss.Style
	optionSelected lipgloss.Style
	number         lipgloss.Style
	numberSelected lipgloss.Style
	description    lipgloss.Style
	warning        lipgloss.Style
	errorText      lipgloss.Style
	checkbox       lipgloss.Style
	checkboxOn     lipgloss.Style
	spinner        lipgloss.Style
	inputText      lipgloss.Style
	group          lipgloss.Style
	valueNeutral   lipgloss.Style
	valueOn        lipgloss.Style
	valueOff       lipgloss.Style
}

func newOnboardingStyles(theme string) onboardingStyles {
	palette := uiPalette(theme)
	return onboardingStyles{
		title:          lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		body:           lipgloss.NewStyle().Foreground(palette.foreground),
		helper:         lipgloss.NewStyle().Foreground(palette.muted).Faint(true),
		footer:         lipgloss.NewStyle().Foreground(palette.muted).Faint(true),
		option:         lipgloss.NewStyle().Foreground(palette.foreground),
		optionSelected: lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		number:         lipgloss.NewStyle().Foreground(palette.muted),
		numberSelected: lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		description:    lipgloss.NewStyle().Foreground(palette.muted).Faint(true),
		warning:        lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true),
		errorText:      lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true),
		checkbox:       lipgloss.NewStyle().Foreground(palette.muted),
		checkboxOn:     lipgloss.NewStyle().Foreground(palette.secondary).Bold(true),
		spinner:        lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		inputText:      lipgloss.NewStyle().Foreground(palette.foreground),
		group:          lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		valueNeutral:   lipgloss.NewStyle().Foreground(palette.primary).Bold(true),
		valueOn:        lipgloss.NewStyle().Foreground(palette.secondary).Bold(true),
		valueOff:       lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Error.Adaptive()).Bold(true),
	}
}

type onboardingRenderedContent struct {
	lines     []string
	cursorRow int
	cursorCol int
}

func (m *onboardingModel) View() string {
	if m.finalizing || m.currentScreen.Kind == onboardingScreenLoading {
		return m.renderLoadingView()
	}
	headerLines := wrapANSIText(m.styles.title.Render(m.currentScreen.Title), max(1, m.width))
	footerLines := m.renderFooterLines(max(1, m.width))
	content := m.buildContent(max(1, m.width))
	contentHeight := m.contentHeight()
	maxOffset := len(content.lines) - contentHeight
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	end := m.offset + contentHeight
	if end > len(content.lines) {
		end = len(content.lines)
	}
	visible := content.lines
	if len(content.lines) > 0 {
		visible = content.lines[m.offset:end]
	}
	var b strings.Builder
	b.WriteString(strings.Join(headerLines, "\n"))
	b.WriteString("\n\n")
	if len(visible) > 0 {
		b.WriteString(strings.Join(visible, "\n"))
	}
	if filler := m.contentHeight() - len(visible); filler > 0 {
		b.WriteString(strings.Repeat("\n", filler))
	}
	if len(footerLines) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(footerLines, "\n"))
	}
	m.updateTerminalCursor(headerLines, content, visible)
	return b.String()
}

func (m *onboardingModel) updateTerminalCursor(headerLines []string, content onboardingRenderedContent, visible []string) {
	if m.terminalCursor == nil {
		return
	}
	if m.currentScreen.Kind != onboardingScreenInput || content.cursorRow < 0 {
		m.terminalCursor.Clear()
		return
	}
	visibleCursorRow := content.cursorRow - m.offset
	if visibleCursorRow < 0 || visibleCursorRow >= len(visible) {
		m.terminalCursor.Clear()
		return
	}
	m.terminalCursor.Set(uiTerminalCursorPlacement{
		Visible:   true,
		CursorRow: len(headerLines) + 2 + visibleCursorRow,
		CursorCol: content.cursorCol,
		AnchorRow: max(0, m.height-1),
		AltScreen: true,
	})
}

func (m *onboardingModel) contentHeight() int {
	headerLines := wrapANSIText(m.styles.title.Render(m.currentScreen.Title), max(1, m.width))
	footerLines := m.renderFooterLines(max(1, m.width))
	height := m.height - len(headerLines) - len(footerLines) - 2
	if height < 1 {
		return 1
	}
	return height
}

func (m *onboardingModel) buildContent(width int) onboardingRenderedContent {
	lines := make([]string, 0, 32)
	cursorRow := -1
	cursorCol := 0
	appendBlank := func() {
		if len(lines) == 0 || lines[len(lines)-1] == "" {
			return
		}
		lines = append(lines, "")
	}
	appendWrapped := func(text string, style lipgloss.Style) {
		for _, line := range wrapStyledParagraphs(text, width, style) {
			lines = append(lines, line)
		}
	}
	body := strings.TrimSpace(m.currentScreen.Body)
	if body != "" {
		appendWrapped(body, m.styles.body)
	}
	if m.currentScreen.ThemePreview {
		appendBlank()
		for _, line := range m.renderThemePreview(width) {
			lines = append(lines, line)
		}
	}
	if m.currentScreen.ID == "review" {
		appendBlank()
		for _, line := range m.renderReviewSummary(width) {
			lines = append(lines, line)
		}
	}
	if text := strings.TrimSpace(m.currentScreen.ErrorText); text != "" {
		appendBlank()
		appendWrapped(text, m.styles.errorText)
	}
	switch m.currentScreen.Kind {
	case onboardingScreenChoice, onboardingScreenMulti:
		appendBlank()
		for index, option := range m.currentScreen.Options {
			if option.Group != "" && (index == 0 || m.currentScreen.Options[index-1].Group != option.Group) {
				if len(lines) > 0 && lines[len(lines)-1] != "" {
					lines = append(lines, "")
				}
				appendWrapped(option.Group, m.styles.group)
			}
			if index == m.cursor {
				cursorRow = len(lines)
			}
			prefix := fmt.Sprintf("%d. ", index+1)
			if m.currentScreen.Kind == onboardingScreenMulti {
				prefix += "[ ] "
				if m.selection[option.ID] {
					prefix = fmt.Sprintf("%d. [x] ", index+1)
				}
			}
			style := m.styles.option
			if index == m.cursor {
				style = m.styles.optionSelected
			}
			optionLine := style.Render(prefix + option.Title)
			if option.ID == m.currentScreen.DefaultOptionID {
				optionLine += m.styles.description.Render("  • recommended")
			}
			if warning := strings.TrimSpace(option.Warning); warning != "" {
				optionLine += m.styles.warning.Render("  " + warning)
			}
			for _, line := range wrapANSIText(optionLine, width) {
				lines = append(lines, line)
			}
			if desc := strings.TrimSpace(option.Description); desc != "" {
				for _, line := range wrapANSIText(m.styles.description.Render("  "+desc), width) {
					lines = append(lines, line)
				}
			}
		}
	case onboardingScreenInput:
		appendBlank()
		renderedInput := renderSingleLineEditor(width, 0, m.input, "> ", true, m.inputMask, m.inputPlaceholder)
		if renderedInput.Cursor.Visible {
			cursorRow = len(lines) + renderedInput.Cursor.Row
			cursorCol = renderedInput.Cursor.Col
		} else {
			cursorRow = len(lines)
			cursorCol = 0
		}
		if m.terminalCursor == nil {
			lines = append(lines, tuiinput.RenderSoftCursorLines(width, renderedInput, m.styles.inputText)...)
		} else {
			for _, line := range renderedInput.Lines {
				lines = append(lines, m.styles.inputText.Render(line))
			}
		}
	}
	if helper := strings.TrimSpace(m.currentScreen.Helper); helper != "" {
		appendBlank()
		appendWrapped(helper, m.styles.helper)
	}
	return onboardingRenderedContent{lines: lines, cursorRow: cursorRow, cursorCol: cursorCol}
}

func (m *onboardingModel) renderFooterLines(width int) []string {
	help := "↑/↓ pick or scroll | <-/-> back or forward | enter confirm | space toggle | esc cancel"
	if m.currentScreen.Kind == onboardingScreenInput {
		help = "↑/↓ scroll | <-/-> back or forward | enter confirm | esc cancel"
	} else if m.currentScreen.Kind == onboardingScreenMulti && screenHasToggleAllOption(m.currentScreen) {
		help = "↑/↓ pick or scroll | <-/-> back or forward | enter confirm | space toggle | a toggle all | esc cancel"
	}
	return wrapStyledParagraphs(help, width, m.styles.footer)
}

type onboardingThemePreviewStyles struct {
	status lipgloss.Style
	input  lipgloss.Style
	help   lipgloss.Style
}

func onboardingThemePreviewStyleSet(theme string, width int) onboardingThemePreviewStyles {
	palette := uiPalette(theme)
	innerWidth := max(12, width-2)
	return onboardingThemePreviewStyles{
		status: lipgloss.NewStyle().Foreground(palette.foreground).Background(palette.background).Padding(0, 1).Width(innerWidth),
		input:  lipgloss.NewStyle().Foreground(palette.foreground).Background(palette.inputBg).Padding(0, 1).Width(innerWidth),
		help:   lipgloss.NewStyle().Foreground(palette.muted).Background(palette.background).Padding(0, 1).Width(innerWidth).Faint(true),
	}
}

func (m *onboardingModel) renderThemePreview(width int) []string {
	theme := m.activeTheme()
	palette := uiPalette(theme)
	previewStyles := onboardingThemePreviewStyleSet(theme, width)
	heading := lipgloss.NewStyle().Foreground(palette.primary).Bold(true).Render("Preview")
	modelLabel := strings.TrimSpace(m.state.selections.model.value)
	if modelLabel == "" {
		modelLabel = config.DefaultModel()
	}
	statusLine := lipgloss.NewStyle().Foreground(palette.primary).Bold(true).Render(config.Command) +
		lipgloss.NewStyle().Foreground(palette.muted).Render(" | ") +
		lipgloss.NewStyle().Foreground(palette.foreground).Render("ready") +
		lipgloss.NewStyle().Foreground(palette.muted).Render(" | ") +
		lipgloss.NewStyle().Foreground(palette.foreground).Render(modelLabel) +
		lipgloss.NewStyle().Foreground(palette.muted).Render(" | ") +
		lipgloss.NewStyle().Foreground(palette.secondary).Bold(true).Render(theme)
	inputLine := lipgloss.NewStyle().Foreground(palette.primary).Bold(true).Render("> ") + lipgloss.NewStyle().Foreground(palette.foreground).Render("Explain this failing test")
	helpLine := lipgloss.NewStyle().Foreground(palette.muted).Render("status line and input preview")
	return []string{
		heading,
		previewStyles.status.Render(statusLine),
		previewStyles.input.Render(inputLine),
		previewStyles.help.Render(helpLine),
	}
}

func (m *onboardingModel) renderReviewSummary(width int) []string {
	lines := make([]string, 0, 10)
	appendRow := func(label, value string, style lipgloss.Style) {
		row := m.styles.body.Render("- "+label+": ") + style.Render(value)
		lines = append(lines, wrapANSIText(row, width)...)
	}
	themeValue := m.state.selections.themeValue()
	themeSummary := sharedtheme.Auto + " (" + sharedtheme.Resolve(themeValue) + ")"
	if sharedtheme.IsExplicit(themeValue) {
		themeSummary = sharedtheme.Resolve(themeValue)
	}
	appendRow("Theme", themeSummary, m.styles.valueNeutral)
	appendRow("Model", m.state.selections.model.value, m.styles.valueNeutral)
	modelFact := modelFactFor(&m.state, m.state.selections.model.value)
	if modelFact.ContextWindowTokens != nil && *modelFact.ContextWindowTokens > 0 {
		contextValue := formatTokenWindow(m.state.selections.contextWindowTokens(modelFact))
		if m.state.selections.contextWindow.kind == onboardingContextDefault {
			contextValue = "default (" + formatTokenWindow(*modelFact.ContextWindowTokens) + ")"
		}
		appendRow("Context window", contextValue, m.styles.valueNeutral)
	}
	thinking := strings.TrimSpace(m.state.selections.thinkingValue())
	if thinking == "" {
		appendRow("Thinking", "off", m.styles.valueOff)
	} else {
		appendRow("Thinking", thinking, m.styles.valueNeutral)
	}
	verbosity := m.state.selections.verbosity.value
	if strings.TrimSpace(verbosity) == "" {
		verbosity = "off"
	}
	verbosityStyle := m.styles.valueNeutral
	if verbosity == "off" {
		verbosityStyle = m.styles.valueOff
	}
	appendRow("Verbosity", verbosity, verbosityStyle)
	if m.state.selections.askQuestion {
		appendRow("Questions", "on", m.styles.valueOn)
	} else {
		appendRow("Questions", "off", m.styles.valueOff)
	}
	reviewer := string(m.state.selections.supervisor.frequency)
	reviewerStyle := m.styles.valueOn
	if reviewer == "off" {
		reviewerStyle = m.styles.valueOff
	}
	appendRow("Supervisor", reviewer, reviewerStyle)
	if reviewerEnabled(&m.state) {
		appendRow("Supervisor model", m.state.selections.reviewerModelValue(), m.styles.valueNeutral)
		reviewerThinking := strings.TrimSpace(m.state.selections.reviewerThinkingValue())
		reviewerThinkingStyle := m.styles.valueNeutral
		if reviewerThinking == "" {
			reviewerThinking = "off"
			reviewerThinkingStyle = m.styles.valueOff
		}
		appendRow("Supervisor thinking", reviewerThinking, reviewerThinkingStyle)
	}
	compactionStyle := m.styles.valueNeutral
	if m.state.selections.compaction == onboardingCompactionManualOnly {
		compactionStyle = m.styles.valueOff
	}
	appendRow("Compaction", string(m.state.selections.compactionValue()), compactionStyle)
	if summary := skillImportSummary(&m.state); summary != "" {
		appendRow("Skills import", summary, m.styles.valueNeutral)
	}
	if enabled, disabled := selectedSkillCounts(&m.state); enabled > 0 || disabled > 0 {
		appendRow("Enabled skills", fmt.Sprintf("%d enabled, %d disabled", enabled, disabled), m.styles.valueNeutral)
	}
	return lines
}

func wrapStyledParagraphs(text string, width int, style lipgloss.Style) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	paragraphs := strings.Split(trimmed, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if strings.TrimSpace(paragraph) == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, wrapANSIText(style.Render(paragraph), width)...)
	}
	return lines
}

func wrapANSIText(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	wrapped := ansi.Wordwrap(strings.TrimRight(text, "\n"), width, " ,.;-+|")
	if strings.TrimSpace(ansi.Strip(wrapped)) == "" {
		return []string{text}
	}
	return strings.Split(strings.TrimRight(wrapped, "\n"), "\n")
}

func (m *onboardingModel) renderLoadingView() string {
	title := m.currentScreen.Title
	if m.finalizing {
		title = "First-time setup"
	}
	loadingText := m.currentScreen.LoadingText
	if m.finalizingLabel != "" {
		loadingText = m.finalizingLabel
	}
	content := strings.Join([]string{m.styles.title.Render(title), "", m.styles.spinner.Render(pendingToolSpinnerFrame(m.spinnerFrame) + " " + loadingText)}, "\n")
	if m.currentScreen.ErrorText != "" {
		content += "\n\n" + m.styles.errorText.Render(m.currentScreen.ErrorText)
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

func renderedLineCount(text string) int {
	trimmed := ansi.Strip(strings.TrimRight(text, "\n"))
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}
