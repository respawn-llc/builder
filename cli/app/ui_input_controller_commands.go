package app

import (
	"strconv"
	"strings"

	"core/cli/app/commands"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func (c uiInputController) applyCommandResultWithPreSubmitQueuePosition(commandResult commands.Result, queuePosition preSubmitQueuePosition) (tea.Model, tea.Cmd) {
	return c.applyCommandResultWithPreSubmitQueuePositionAndOrigin(commandResult, queuePosition, activeSubmitOriginDirect)
}

func (c uiInputController) applyCommandResultWithPreSubmitQueuePositionAndOrigin(commandResult commands.Result, queuePosition preSubmitQueuePosition, origin activeSubmitOrigin) (tea.Model, tea.Cmd) {
	return c.applyCommandResultWithPreSubmitQueuePositionAndOriginAndOrder(commandResult, queuePosition, origin, nil)
}

func (c uiInputController) applyCommandResultWithPreSubmitQueuePositionAndOriginAndOrder(
	commandResult commands.Result,
	queuePosition preSubmitQueuePosition,
	origin activeSubmitOrigin,
	submissionOrder *inputSubmissionOrder,
) (tea.Model, tea.Cmd) {
	m := c.model
	if commandResult.PromptCommand != nil {
		invocation := commandResult.PromptCommand
		canonical, err := runtimeinput.CanonicalCommandText(invocation.Name, invocation.Arguments)
		if err != nil {
			return m, m.sendTransientStatusWithNoticeID(err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		history, err := invocation.CanonicalHistoryText()
		if err != nil {
			return m, m.sendTransientStatusWithNoticeID(err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		if commandResult.FreshConversation && (m.isBusy() || m.currentConversationFreshness() != clientui.ConversationFreshnessFresh) {
			previousSessionID, err := runtimeids.ParseSessionID(m.sessionID)
			if err != nil {
				return m, c.model.appendLocalEntryWithNoticeID("error", "Current session identity is invalid: "+err.Error(), "")
			}
			if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, canonical); blocked {
				return m, disconnectCmd
			}
			m.nextSessionInitialPrompt = history
			m.nextSessionInitialPromptHistoryRecorded = true
			m.nextPreviousSessionID = &previousSessionID
			m.exitAction = UIActionNewSession
			return m, tea.Quit
		}
		m.rememberPromptCommandHistoryLocally(history)
		return m, c.startTypedSubmissionWithPreSubmitQueuePositionAndOrder(
			canonical,
			runtimeinput.Input{Kind: runtimeinput.KindPromptCommand, PromptCommand: invocation},
			queuePosition,
			"",
			origin,
			submissionOrder,
		)
	}
	if commandResult.SubmitUser {
		if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, commandResult.User); blocked {
			return m, disconnectCmd
		}
	}
	if commandResult.SubmitUser && commandResult.FreshConversation && (m.isBusy() || m.currentConversationFreshness() != clientui.ConversationFreshnessFresh) {
		previousSessionID, err := runtimeids.ParseSessionID(m.sessionID)
		if err != nil {
			return m, c.model.appendLocalEntryWithNoticeID("error", "Current session identity is invalid: "+err.Error(), "")
		}
		m.nextSessionInitialPrompt = commandResult.User
		m.nextSessionInitialPromptHistoryRecorded = true
		m.nextPreviousSessionID = &previousSessionID
		m.exitAction = UIActionNewSession
		return m, tea.Quit
	}
	if commandResult.SubmitUser {
		return m, c.startSubmissionWithPreSubmitQueuePositionAndOriginAndOrder(
			commandResult.User,
			queuePosition,
			"",
			origin,
			submissionOrder,
		)
	}
	prefixCmd := tea.Cmd(nil)
	if commandResult.Text != "" {
		prefixCmd = c.model.appendLocalEntryWithNoticeID("system", commandResult.Text, "")
	}

	switch commandResult.Action {
	case commands.ActionExit:
		m.exitAction = UIActionExit
		return m, sequenceCmds(prefixCmd, tea.Quit)
	case commands.ActionNew:
		previousSessionID, err := runtimeids.ParseSessionID(m.sessionID)
		if err != nil {
			return m, sequenceCmds(prefixCmd, c.model.appendLocalEntryWithNoticeID("error", "Current session identity is invalid: "+err.Error(), ""))
		}
		m.nextPreviousSessionID = &previousSessionID
		m.exitAction = UIActionNewSession
		return m, sequenceCmds(prefixCmd, tea.Quit)
	case commands.ActionResume:
		next, cmd := c.handleResumeCommand()
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionBack:
		next, cmd := c.handleBackCommand()
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionLogout:
		m.exitAction = UIActionLogout
		return m, sequenceCmds(prefixCmd, tea.Quit)
	case commands.ActionSetName:
		next, cmd := c.handleSessionNameCommand(commandResult.SessionName)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionSetThinking:
		next, cmd := c.handleThinkingLevelCommand(commandResult.ThinkingLevel)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionSetFast:
		next, cmd := c.handleFastModeCommand(commandResult.FastMode)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionSetSupervisor:
		next, cmd := c.handleSupervisorModeCommand(commandResult.SupervisorMode)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionSetAutoCompaction:
		next, cmd := c.handleAutoCompactionCommand(commandResult.AutoCompactionMode)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionSetQuestions:
		next, cmd := c.handleQuestionsCommand(commandResult.QuestionsMode)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionCompact:
		return m, sequenceCmds(prefixCmd, c.startCompaction(commandResult.Args))
	case commands.ActionStatus:
		return m, sequenceCmds(prefixCmd, c.startStatusFlowCmd())
	case commands.ActionGoal:
		next, cmd := c.handleGoalCommand(commandResult.GoalMode, commandResult.GoalObjective)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionProcesses:
		args := strings.Fields(strings.TrimSpace(commandResult.Args))
		if len(args) == 0 {
			return m, sequenceCmds(prefixCmd, c.startProcessListFlowCmd())
		}
		action := strings.ToLower(strings.TrimSpace(args[0]))
		id := ""
		if len(args) > 1 {
			id = strings.TrimSpace(args[1])
		}
		next, cmd := c.runProcessAction(action, id)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionWorktree:
		next, cmd := c.handleWorktreeCommand(commandResult.Args)
		return next, sequenceCmds(prefixCmd, cmd)
	case commands.ActionCopy:
		next, cmd := c.handleCopyCommand()
		return next, sequenceCmds(prefixCmd, cmd)
	}
	return m, prefixCmd
}

func (c uiInputController) handleResumeCommand() (tea.Model, tea.Cmd) {
	m := c.model
	m.exitAction = UIActionResume
	return m, tea.Quit
}

func (c uiInputController) handleBackCommand() (tea.Model, tea.Cmd) {
	m := c.model
	status := m.cachedRuntimeStatus()
	if status.NavigationTargetSessionID == nil {
		return m, c.model.appendLocalEntryWithNoticeID("system", "No parent session available", "")
	}
	if m.finalAnswerOperation != nil {
		return m, nil
	}
	return m, m.startFinalAnswerOperation(uiFinalAnswerOperationBack, status.NavigationTargetSessionID.String())
}

func (c uiInputController) handleCopyCommand() (tea.Model, tea.Cmd) {
	m := c.model
	if m.finalAnswerOperation != nil {
		return m, nil
	}
	return m, m.startFinalAnswerOperation(uiFinalAnswerOperationCopy, "")
}

func (c uiInputController) handleSessionNameCommand(sessionName string) (tea.Model, tea.Cmd) {
	m := c.model
	sessionName = strings.TrimSpace(sessionName)
	if m.hasRuntimeClient() {
		return m, m.runtimeControlCommand(runtimeControlSetSessionName, sessionName, false, "")
	}
	m.sessionName = sessionName
	return m, tea.SetWindowTitle(sessionTitle(m.sessionName))
}

func (c uiInputController) handleThinkingLevelCommand(requested string) (tea.Model, tea.Cmd) {
	m := c.model
	requested = strings.TrimSpace(requested)
	if requested == "" {
		current := strings.TrimSpace(m.thinkingLevel)
		if m.hasRuntimeClient() {
			current = m.cachedRuntimeStatus().ThinkingLevel
		}
		return m, c.model.sendThinkingLevelQueryStatus(current)
	}

	normalized, ok := clientui.NormalizeThinkingLevel(requested)
	if !ok {
		errText := "invalid thinking level " + strconv.Quote(requested) + " (expected low|medium|high|xhigh|max|ultra)"
		return m, c.model.appendLocalEntryWithNoticeID("error", errText, "")
	}
	if m.hasRuntimeClient() {
		return m, m.runtimeControlCommand(runtimeControlSetThinkingLevel, normalized, false, "")
	}
	m.thinkingLevel = normalized
	return m, c.model.sendThinkingLevelSetStatus(m.thinkingLevel)
}

func (m *uiModel) sendThinkingLevelQueryStatus(level string) tea.Cmd {
	current := strings.TrimSpace(level)
	if current == "" {
		current = "unknown"
	}
	return m.sendTransientStatusWithNoticeID("Thinking level is "+current, uiStatusNoticeInfo, transientStatusDuration, uiStatusNoticeReplace, "")
}

func (m *uiModel) sendThinkingLevelSetStatus(level string) tea.Cmd {
	return m.sendTransientStatusWithNoticeID("Thinking level set to "+strings.TrimSpace(level), uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
}

func (c uiInputController) handleFastModeCommand(requested string) (tea.Model, tea.Cmd) {
	m := c.model
	available, currentEnabled := m.fastModeState()
	currentEnabled = m.runtimeControlPendingEnabled(runtimeControlSetFastMode, m.sessionID, currentEnabled)
	if !available {
		errText := "Fast mode is only available for OpenAI-based Responses providers"
		return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", errText, ""), c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}

	requested = strings.ToLower(strings.TrimSpace(requested))
	switch requested {
	case "status":
		status := "off"
		if currentEnabled {
			status = "on"
		}
		return m, c.model.appendLocalEntryWithNoticeID("system", "Fast mode is "+status, "")
	case "", "on", "off":
		// supported
	default:
		errText := "Usage: /fast [on|off|status]"
		return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", errText, ""), c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}

	targetEnabled := currentEnabled
	switch requested {
	case "":
		targetEnabled = !currentEnabled
	case "on":
		targetEnabled = true
	case "off":
		targetEnabled = false
	}

	changed := currentEnabled != targetEnabled
	if m.hasRuntimeClient() {
		return m, m.runtimeControlCommand(runtimeControlSetFastMode, "", targetEnabled, "")
	} else {
		m.fastModeEnabled = targetEnabled
	}

	status := serverapi.FastModeToggleStatusMessage(m.fastModeEnabled, changed)
	return m, c.appendSystemFeedbackWithMirroredStatus(status, uiStatusNoticeSuccess)
}

func (c uiInputController) handleSupervisorModeCommand(requested string) (tea.Model, tea.Cmd) {
	m := c.model
	requested = strings.ToLower(strings.TrimSpace(requested))
	currentEnabled, currentMode := m.reviewerInvocationState()
	currentEnabled = m.runtimeControlPendingEnabled(runtimeControlSetReviewer, m.sessionID, currentEnabled)
	targetEnabled := currentEnabled
	switch requested {
	case "":
		targetEnabled = !currentEnabled
	case "on":
		targetEnabled = true
	case "off":
		targetEnabled = false
	default:
		errText := "invalid supervisor mode " + strconv.Quote(requested) + " (expected on|off)"
		return m, c.model.appendLocalEntryWithNoticeID("error", errText, "")
	}
	changed := false
	nextMode := currentMode
	if m.hasRuntimeClient() {
		return m, m.runtimeControlCommand(runtimeControlSetReviewer, "", targetEnabled, "")
	} else {
		nextMode = "off"
		if targetEnabled {
			nextMode = "edits"
		}
		changed = currentEnabled != targetEnabled
	}
	m.reviewerMode = nextMode
	m.reviewerEnabled = nextMode != "off"
	status := serverapi.ReviewerToggleStatusMessage(m.reviewerEnabled, nextMode, changed)
	return m, c.appendSystemFeedbackWithMirroredStatus(status, uiStatusNoticeInfo)
}

func (c uiInputController) handleQuestionsCommand(requested string) (tea.Model, tea.Cmd) {
	m := c.model
	requested = strings.ToLower(strings.TrimSpace(requested))
	currentEnabled := m.cachedRuntimeStatus().QuestionsEnabled
	currentEnabled = m.runtimeControlPendingEnabled(runtimeControlSetQuestions, m.sessionID, currentEnabled)
	targetEnabled := currentEnabled
	switch requested {
	case "":
		targetEnabled = !currentEnabled
	case "on":
		targetEnabled = true
	case "off":
		targetEnabled = false
	default:
		errText := "invalid questions mode " + strconv.Quote(requested) + " (expected on|off)"
		return m, c.model.appendLocalEntryWithNoticeID("error", errText, "")
	}
	changed := false
	nextEnabled := currentEnabled
	if m.hasRuntimeClient() {
		return m, m.runtimeControlCommand(runtimeControlSetQuestions, "", targetEnabled, "")
	} else {
		nextEnabled = targetEnabled
		changed = currentEnabled != targetEnabled
	}
	m.questionsEnabled = nextEnabled
	status := serverapi.QuestionsToggleStatusMessage(nextEnabled, changed)
	return m, c.appendSystemFeedbackWithMirroredStatus(status, uiStatusNoticeInfo)
}

func (c uiInputController) handleAutoCompactionCommand(requested string) (tea.Model, tea.Cmd) {
	m := c.model
	requested = strings.ToLower(strings.TrimSpace(requested))
	currentEnabled := m.cachedRuntimeStatus().AutoCompactionEnabled
	currentEnabled = m.runtimeControlPendingEnabled(runtimeControlSetAutoCompaction, m.sessionID, currentEnabled)
	currentCompactionMode := "native"
	if m.hasRuntimeClient() {
		currentCompactionMode = m.cachedRuntimeStatus().CompactionMode
	}
	targetEnabled := currentEnabled
	switch requested {
	case "":
		targetEnabled = !currentEnabled
	case "on":
		targetEnabled = true
	case "off":
		targetEnabled = false
	default:
		errText := "invalid autocompaction mode " + strconv.Quote(requested) + " (expected on|off)"
		return m, c.model.appendLocalEntryWithNoticeID("error", errText, "")
	}
	if m.workflowSessionActive() && !targetEnabled {
		errText := "Auto-compaction cannot be disabled for workflow task sessions"
		return m, c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}

	changed := false
	nextEnabled := currentEnabled
	if m.hasRuntimeClient() {
		return m, m.runtimeControlCommand(runtimeControlSetAutoCompaction, "", targetEnabled, currentCompactionMode)
	} else {
		nextEnabled = targetEnabled
		changed = currentEnabled != targetEnabled
	}
	m.autoCompactionEnabled = nextEnabled
	status := serverapi.AutoCompactionToggleStatusMessage(nextEnabled, changed, currentCompactionMode)
	return m, c.appendSystemFeedbackWithMirroredStatus(status, uiStatusNoticeInfo)
}
