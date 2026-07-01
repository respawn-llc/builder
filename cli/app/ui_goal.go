package app

import (
	"strings"

	"core/cli/app/commands"
	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	sharedtheme "core/shared/theme"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

const noGoalHint = "No goal to manage yet. First, start a goal with /goal <objective>"

func (c uiInputController) handleGoalCommand(mode commands.GoalMode, objective string) (tea.Model, tea.Cmd) {
	m := c.model
	switch mode {
	case commands.GoalModeShow, "":
		return m, c.startGoalFlowCmd()
	case commands.GoalModeSet:
		return m, m.goalRuntimeCommand(goalRuntimeCheckSet, objective)
	case commands.GoalModePause:
		return m, m.goalRuntimeCommand(goalRuntimePause, "")
	case commands.GoalModeResume:
		return m, m.goalRuntimeCommand(goalRuntimeResume, "")
	case commands.GoalModeClear:
		return m, m.goalRuntimeCommand(goalRuntimeCheckClear, "")
	default:
		errText := "Usage: /goal [show|pause|resume|clear|<objective>]"
		return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", errText, ""), c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
}

func (m *uiModel) workflowSessionActive() bool {
	if m == nil || !m.hasRuntimeClient() {
		return false
	}
	if direct := m.runtimeClient().Status(); direct.WorkflowActive || direct.WorkflowSession != nil {
		return true
	}
	status := m.cachedRuntimeStatus()
	return status.WorkflowActive || status.WorkflowSession != nil
}

func goalIsActive(goal *clientui.RuntimeGoal) bool {
	return goal != nil && goal.Status == clientui.RuntimeGoalStatusActive
}

func goalIsPresent(goal *clientui.RuntimeGoal) bool {
	if goal == nil {
		return false
	}
	switch goal.Status {
	case clientui.RuntimeGoalStatusActive, clientui.RuntimeGoalStatusPaused:
		return true
	default:
		return false
	}
}

func goalRequiresClearConfirmation(goal *clientui.RuntimeGoal) bool {
	return goalIsActive(goal) && !goal.Suspended
}

func (c uiInputController) startGoalFlowCmd() tea.Cmd {
	m := c.model
	m.openGoalOverlay(nil, nil)
	return tea.Batch(m.activateSurface(uiSurfaceGoal), m.goalRuntimeCommand(goalRuntimeShow, ""))
}

func (c uiInputController) stopGoalFlowCmd() tea.Cmd {
	m := c.model
	overlayCmd := m.restoreTranscriptSurface()
	m.closeGoalOverlay()
	return overlayCmd
}

func (c uiInputController) handleGoalOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	if strings.TrimSpace(m.goal.confirmMode) != "" {
		return c.handleGoalConfirmKey(msg)
	}
	switch strings.ToLower(msg.String()) {
	case "ctrl+c":
		if m.isBusy() {
			return m, c.interruptBusyRuntime()
		}
		m.exitAction = UIActionExit
		if overlayCmd := m.restoreTranscriptSurface(); overlayCmd != nil {
			m.closeGoalOverlay()
			return m, tea.Sequence(overlayCmd, tea.Quit)
		}
		return m, tea.Quit
	case "esc", "q":
		return m, c.stopGoalFlowCmd()
	case "up":
		m.moveGoalScroll(-1)
	case "down":
		m.moveGoalScroll(1)
	case "pgup":
		m.moveGoalScrollPage(-1)
	case "pgdown":
		m.moveGoalScrollPage(1)
	case "home":
		m.goal.scroll = 0
	case "end":
		m.goal.scroll = 1 << 30
	}
	return m, nil
}

func (c uiInputController) handleGoalConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	switch strings.ToLower(msg.String()) {
	case "ctrl+c":
		if m.isBusy() {
			return m, c.interruptBusyRuntime()
		}
		m.exitAction = UIActionExit
		if overlayCmd := m.restoreTranscriptSurface(); overlayCmd != nil {
			m.closeGoalOverlay()
			return m, tea.Sequence(overlayCmd, tea.Quit)
		}
		return m, tea.Quit
	case "esc", "q", "n":
		return m, c.stopGoalFlowCmd()
	case "tab", "shift+tab", "left", "right", "h", "l":
		m.toggleGoalConfirmSelection()
		return m, nil
	case "up":
		m.moveGoalScroll(-1)
		return m, nil
	case "down":
		m.moveGoalScroll(1)
		return m, nil
	case "pgup":
		m.moveGoalScrollPage(-1)
		return m, nil
	case "pgdown":
		m.moveGoalScrollPage(1)
		return m, nil
	case "home":
		m.goal.scroll = 0
		return m, nil
	case "end":
		m.goal.scroll = 1 << 30
		return m, nil
	case "enter", "y":
		if strings.ToLower(msg.String()) == "y" {
			m.goal.confirmSelection = goalConfirmSelectionConfirm
		}
		if m.goal.confirmSelection != goalConfirmSelectionConfirm {
			return m, c.stopGoalFlowCmd()
		}
		mode := strings.TrimSpace(m.goal.confirmMode)
		objective := m.goal.pendingObjective
		switch mode {
		case "replace":
			return m, m.goalRuntimeCommand(goalRuntimeSet, objective)
		case "clear":
			return m, m.goalRuntimeCommand(goalRuntimeClear, "")
		}
	}
	return m, nil
}

func (m *uiModel) nextGoalRuntimeToken() uint64 {
	m.goalRuntimeToken++
	if m.goalRuntimeToken == 0 {
		m.goalRuntimeToken++
	}
	return m.goalRuntimeToken
}

type goalRuntimePendingState struct {
	token             uint64
	sessionID         string
	inFlight          bool
	inFlightOperation goalRuntimeOperation
	inFlightObjective string
	desiredOperation  goalRuntimeOperation
	desiredObjective  string
}

func goalRuntimeOperationMutates(operation goalRuntimeOperation) bool {
	switch operation {
	case goalRuntimeSet, goalRuntimePause, goalRuntimeResume, goalRuntimeClear:
		return true
	default:
		return false
	}
}

func (m *uiModel) beginGoalRuntimeMutation(operation goalRuntimeOperation, sessionID, objective string) (uint64, bool) {
	if m == nil {
		return 0, false
	}
	sessionID = strings.TrimSpace(sessionID)
	objective = strings.TrimSpace(objective)
	if !goalRuntimeOperationMutates(operation) {
		return m.nextGoalRuntimeToken(), true
	}
	m.goalRuntimeMutationSerial = nextNonZeroToken(m.goalRuntimeMutationSerial)
	if m.goalRuntimePending.inFlight && m.goalRuntimePending.sessionID == sessionID {
		m.goalRuntimePending.desiredOperation = operation
		m.goalRuntimePending.desiredObjective = objective
		return 0, false
	}
	token := m.nextGoalRuntimeToken()
	m.goalRuntimePending = goalRuntimePendingState{
		token:             token,
		sessionID:         sessionID,
		inFlight:          true,
		inFlightOperation: operation,
		inFlightObjective: objective,
		desiredOperation:  operation,
		desiredObjective:  objective,
	}
	return token, true
}

func (m *uiModel) goalRuntimeCommand(operation goalRuntimeOperation, objective string) tea.Cmd {
	if m == nil {
		return nil
	}
	client := m.runtimeClient()
	objective = strings.TrimSpace(objective)
	sessionID := strings.TrimSpace(m.sessionID)
	mutationSerial := m.goalRuntimeMutationSerial
	token, shouldStart := m.beginGoalRuntimeMutation(operation, sessionID, objective)
	if !shouldStart {
		return nil
	}
	if client == nil {
		return func() tea.Msg {
			return goalRuntimeDoneMsg{token: token, mutationSerial: mutationSerial, operation: operation, objective: objective}
		}
	}
	return func() tea.Msg {
		msg := goalRuntimeDoneMsg{token: token, sessionID: sessionID, mutationSerial: mutationSerial, operation: operation, objective: objective}
		switch operation {
		case goalRuntimeShow, goalRuntimeCheckSet, goalRuntimeCheckClear:
			msg.goal, msg.err = client.ShowGoal()
		case goalRuntimeSet:
			msg.goal, msg.err = client.SetGoal(objective)
		case goalRuntimePause:
			msg.goal, msg.err = client.PauseGoal()
		case goalRuntimeResume:
			msg.goal, msg.err = client.ResumeGoal()
		case goalRuntimeClear:
			msg.goal, msg.err = client.ClearGoal()
		}
		return msg
	}
}

func (m *uiModel) applyGoalRuntimeDone(msg goalRuntimeDoneMsg) tea.Cmd {
	if m == nil {
		return nil
	}
	if goalRuntimeOperationMutates(msg.operation) {
		if msg.token != m.goalRuntimePending.token {
			return nil
		}
	} else if msg.token != m.goalRuntimeToken {
		return nil
	}
	if msg.sessionID != "" && strings.TrimSpace(m.sessionID) != "" && msg.sessionID != strings.TrimSpace(m.sessionID) {
		return nil
	}
	m.observeRuntimeRequestResult(msg.err)
	if msg.err != nil {
		if goalRuntimeOperationMutates(msg.operation) {
			m.goalRuntimePending = goalRuntimePendingState{}
		}
		detailErr := runtimeattach.FormatSubmissionError(msg.err)
		if m.goal.open {
			m.goal.error = detailErr
			return nil
		}
		return sequenceCmds(
			m.appendLocalEntryWithNoticeID("error", detailErr, ""),
			m.sendTransientStatusWithNoticeID(detailErr, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""),
		)
	}
	var followUpCmd tea.Cmd
	if goalRuntimeOperationMutates(msg.operation) {
		pending := m.goalRuntimePending
		if pending.inFlight && (pending.desiredOperation != pending.inFlightOperation || pending.desiredObjective != pending.inFlightObjective) {
			m.goalRuntimePending.inFlight = false
			followUpCmd = m.goalRuntimeCommand(pending.desiredOperation, pending.desiredObjective)
		} else {
			m.goalRuntimePending = goalRuntimePendingState{}
		}
	}
	switch msg.operation {
	case goalRuntimeShow:
		m.goal.goal = cloneRuntimeGoal(msg.goal)
		m.goal.error = ""
		return followUpCmd
	case goalRuntimeCheckSet:
		if msg.mutationSerial != m.goalRuntimeMutationSerial {
			return followUpCmd
		}
		if goalIsActive(msg.goal) {
			m.openGoalConfirmOverlay("replace", msg.goal, msg.objective, nil)
			return sequenceCmds(m.activateSurface(uiSurfaceGoal), followUpCmd)
		}
		return sequenceCmds(m.goalRuntimeCommand(goalRuntimeSet, msg.objective), followUpCmd)
	case goalRuntimeCheckClear:
		if msg.mutationSerial != m.goalRuntimeMutationSerial {
			return followUpCmd
		}
		if goalRequiresClearConfirmation(msg.goal) {
			m.openGoalConfirmOverlay("clear", msg.goal, "", nil)
			return sequenceCmds(m.activateSurface(uiSurfaceGoal), followUpCmd)
		}
		return sequenceCmds(m.goalRuntimeCommand(goalRuntimeClear, ""), followUpCmd)
	case goalRuntimeSet:
		m.goal.goal = cloneRuntimeGoal(msg.goal)
		overlayCmd := tea.Cmd(nil)
		if m.goal.open && strings.TrimSpace(m.goal.confirmMode) != "" {
			overlayCmd = m.inputController().stopGoalFlowCmd()
		}
		return sequenceCmds(overlayCmd, followUpCmd)
	case goalRuntimePause:
		m.goal.goal = cloneRuntimeGoal(msg.goal)
		return followUpCmd
	case goalRuntimeResume:
		m.goal.goal = cloneRuntimeGoal(msg.goal)
		return followUpCmd
	case goalRuntimeClear:
		m.goal.goal = nil
		overlayCmd := tea.Cmd(nil)
		if m.goal.open && strings.TrimSpace(m.goal.confirmMode) != "" {
			overlayCmd = m.inputController().stopGoalFlowCmd()
		}
		return sequenceCmds(overlayCmd, followUpCmd)
	default:
		return followUpCmd
	}
}

func (m *uiModel) openGoalOverlay(goal *clientui.RuntimeGoal, err error) {
	m.goal.open = true
	m.goal.scroll = 0
	m.goal.goal = cloneRuntimeGoal(goal)
	m.goal.error = ""
	if err != nil {
		m.goal.error = err.Error()
	}
	m.setInputMode(uiInputModeGoal)
}

func (m *uiModel) openGoalConfirmOverlay(mode string, goal *clientui.RuntimeGoal, pendingObjective string, err error) {
	m.openGoalOverlay(goal, err)
	m.goal.confirmMode = strings.TrimSpace(mode)
	m.goal.confirmSelection = goalConfirmSelectionCancel
	m.goal.pendingObjective = strings.TrimSpace(pendingObjective)
}

func (m *uiModel) closeGoalOverlay() {
	m.goal = uiGoalOverlayState{}
	m.restorePrimaryInputMode()
}

func (m *uiModel) moveGoalScroll(delta int) {
	m.goal.scroll += delta
	if m.goal.scroll < 0 {
		m.goal.scroll = 0
	}
}

func (m *uiModel) moveGoalScrollPage(deltaPages int) {
	height := m.termHeight
	if height < 1 {
		height = 1
	}
	m.moveGoalScroll(deltaPages * max(1, height-4))
}

const (
	goalConfirmSelectionCancel = iota
	goalConfirmSelectionConfirm
)

type goalOverlayLineBuilder struct {
	model *uiModel
	width int
	lines []string
}

func newGoalOverlayLineBuilder(model *uiModel, width int) *goalOverlayLineBuilder {
	return &goalOverlayLineBuilder{model: model, width: width, lines: make([]string, 0, 24)}
}

func (b *goalOverlayLineBuilder) appendWrapped(text string, lineStyle lipgloss.Style) {
	wrapped := wrapLine(strings.TrimRight(text, " \t"), b.width)
	if len(wrapped) == 0 {
		b.lines = append(b.lines, padRight("", b.width))
		return
	}
	for _, line := range wrapped {
		b.lines = append(b.lines, padANSIRight(lineStyle.Render(line), b.width))
	}
}

func (b *goalOverlayLineBuilder) appendMarkdown(text string) {
	renderer := b.goalMarkdownRenderer()
	if renderer == nil {
		b.appendWrapped(text, lipgloss.Style{})
		return
	}
	rendered, err := renderer.Render(text)
	if err != nil {
		b.appendWrapped(text, lipgloss.Style{})
		return
	}
	for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		b.lines = append(b.lines, padANSIRight(line, b.width))
	}
}

func (b *goalOverlayLineBuilder) goalMarkdownRenderer() *glamour.TermRenderer {
	if b.model == nil {
		return nil
	}
	theme := strings.TrimSpace(b.model.theme)
	width := b.width
	if width < 1 {
		width = 1
	}
	if b.model.goal.markdownRenderer != nil && b.model.goal.markdownTheme == theme && b.model.goal.markdownWidth == width {
		return b.model.goal.markdownRenderer
	}
	renderer := newStartupMarkdownRendererWithWordWrap(theme, width)
	b.model.goal.markdownTheme = theme
	b.model.goal.markdownWidth = width
	b.model.goal.markdownRenderer = renderer
	return renderer
}

func (b *goalOverlayLineBuilder) appendGap() {
	if len(b.lines) > 0 {
		b.lines = append(b.lines, padRight("", b.width))
	}
}

func (m *uiModel) toggleGoalConfirmSelection() {
	if m.goal.confirmSelection == goalConfirmSelectionConfirm {
		m.goal.confirmSelection = goalConfirmSelectionCancel
		return
	}
	m.goal.confirmSelection = goalConfirmSelectionConfirm
}

func (l uiViewLayout) renderGoalOverlay(width, height int, _ uiStyles) []string {
	if width < 1 || height < 1 {
		return []string{padRight("", width)}
	}
	content := l.goalOverlayContentLines(width)
	maxScroll := max(0, len(content)-height)
	if l.model.goal.scroll > maxScroll {
		l.model.goal.scroll = maxScroll
	}
	if l.model.goal.scroll < 0 {
		l.model.goal.scroll = 0
	}
	start := l.model.goal.scroll
	end := min(len(content), start+height)
	visible := append([]string(nil), content[start:end]...)
	for len(visible) < height {
		visible = append(visible, padRight("", width))
	}
	return visible
}

func (l uiViewLayout) goalOverlayContentLines(width int) []string {
	m := l.model
	palette := uiPalette(m.theme)
	titleStyle := lipgloss.NewStyle().Foreground(palette.primary).Bold(true)
	boldStyle := lipgloss.NewStyle().Bold(true)
	subtleStyle := lipgloss.NewStyle().Foreground(palette.muted).Faint(true)
	warningStyle := lipgloss.NewStyle().Foreground(sharedtheme.DefaultPalette().Status.Warning.Adaptive()).Bold(true)
	builder := newGoalOverlayLineBuilder(m, width)

	builder.appendWrapped("Goal", titleStyle)
	if strings.TrimSpace(m.goal.confirmMode) != "" {
		return l.goalConfirmContentLines(width, titleStyle, boldStyle, subtleStyle, warningStyle)
	}
	if strings.TrimSpace(m.goal.error) != "" {
		builder.appendGap()
		builder.appendWrapped("Could not load goal: "+m.goal.error, warningStyle)
		return builder.lines
	}
	if m.goal.goal == nil {
		builder.appendGap()
		builder.appendWrapped(noGoalHint, subtleStyle)
		return builder.lines
	}
	goal := m.goal.goal
	builder.appendGap()
	builder.appendWrapped("Status: "+strings.TrimSpace(string(goal.Status)), boldStyle)
	if strings.TrimSpace(goal.ID) != "" {
		builder.appendWrapped("ID: "+strings.TrimSpace(goal.ID), subtleStyle)
	}
	builder.appendGap()
	builder.appendWrapped("Objective", titleStyle)
	builder.appendMarkdown(goal.Objective)
	builder.appendGap()
	builder.appendWrapped("Esc/q closes. /goal pause, /goal resume, /goal clear manage lifecycle.", subtleStyle)
	return builder.lines
}

func (l uiViewLayout) goalConfirmContentLines(width int, titleStyle, boldStyle, subtleStyle, warningStyle lipgloss.Style) []string {
	m := l.model
	builder := newGoalOverlayLineBuilder(m, width)

	builder.appendWrapped("Goal", titleStyle)
	builder.appendGap()
	if strings.TrimSpace(m.goal.error) != "" {
		builder.appendWrapped("Could not update goal: "+m.goal.error, warningStyle)
		builder.appendGap()
	}
	switch strings.TrimSpace(m.goal.confirmMode) {
	case "replace":
		builder.appendWrapped("Replace active goal?", boldStyle)
		if m.goal.goal != nil {
			builder.appendWrapped("Current: "+m.goal.goal.Objective, lipgloss.Style{})
		}
		builder.appendWrapped("New: "+m.goal.pendingObjective, lipgloss.Style{})
	case "clear":
		builder.appendWrapped("Clear active goal?", boldStyle)
		if m.goal.goal != nil {
			builder.appendWrapped(m.goal.goal.Objective, lipgloss.Style{})
		}
	default:
		builder.appendWrapped("Confirm goal action?", boldStyle)
	}
	builder.appendGap()
	buttons := []uiChoiceOption{{Label: "Cancel"}, {Label: "Confirm"}}
	builder.lines = append(builder.lines, padANSIRight(renderUIChoiceGroupLine(width, m.theme, uiChoiceGroupKindButton, buttons, m.goal.confirmSelection), builder.width))
	builder.appendWrapped("Tab/←/→ select. Enter confirms. ↑/↓ scroll. Esc cancels.", subtleStyle)
	return builder.lines
}
