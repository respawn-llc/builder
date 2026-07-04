package app

import (
	"strconv"
	"strings"

	"core/cli/app/commands"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func (c uiInputController) applyCommandResultWithPreSubmitQueuePosition(commandResult commands.Result, queuePosition preSubmitQueuePosition) (tea.Model, tea.Cmd) {
	m := c.model
	if commandResult.SubmitUser {
		if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, commandResult.User); blocked {
			return m, disconnectCmd
		}
	}
	if commandResult.SubmitUser && commandResult.FreshConversation && m.currentConversationFreshness() != clientui.ConversationFreshnessFresh {
		m.nextSessionInitialPrompt = commandResult.User
		m.nextSessionInitialPromptHistoryRecorded = true
		m.nextParentSessionID = m.sessionID
		m.exitAction = UIActionNewSession
		return m, tea.Quit
	}
	if commandResult.SubmitUser {
		return m, c.startSubmissionWithPreSubmitQueuePosition(commandResult.User, queuePosition, "")
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
		m.nextParentSessionID = m.sessionID
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
		return m, sequenceCmds(prefixCmd, c.startCompactionWithOrigin(commandResult.Args, uiCompactionOriginManual))
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

const resumeCommandUnavailableMessage = "No other sessions available"

func (c uiInputController) handleResumeCommand() (tea.Model, tea.Cmd) {
	m := c.model
	if !m.resumeCommandAvailable() {
		return m, sequenceCmds(
			c.model.appendLocalEntryWithNoticeID("error", resumeCommandUnavailableMessage, ""),
			c.model.sendTransientStatusWithNoticeID(resumeCommandUnavailableMessage, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""),
		)
	}
	m.exitAction = UIActionResume
	return m, tea.Quit
}

func (c uiInputController) handleBackCommand() (tea.Model, tea.Cmd) {
	m := c.model
	status := m.cachedRuntimeStatus()
	if strings.TrimSpace(status.ParentSessionID) == "" {
		return m, c.model.appendLocalEntryWithNoticeID("system", "No parent session available", "")
	}
	m.nextSessionInitialInput = m.latestAssistantFinalAnswerFromStatus()
	m.nextSessionID = strings.TrimSpace(status.ParentSessionID)
	m.exitAction = UIActionOpenSession
	return m, tea.Quit
}

func (m *uiModel) latestAssistantFinalAnswerFromStatus() string {
	if m.hasRuntimeClient() {
		status := m.cachedRuntimeStatus()
		if answer := strings.TrimSpace(status.LastCommittedAssistantFinalAnswer); answer != "" {
			return status.LastCommittedAssistantFinalAnswer
		}
		return ""
	}
	return ""
}

func (m *uiModel) hasAssistantFinalAnswerToCopy() bool {
	return strings.TrimSpace(m.latestAssistantFinalAnswerFromStatus()) != ""
}

func (c uiInputController) handleCopyCommand() (tea.Model, tea.Cmd) {
	m := c.model
	text := m.latestAssistantFinalAnswerFromStatus()
	if strings.TrimSpace(text) == "" {
		return m, c.model.sendTransientStatusWithNoticeID("No final answer available to copy", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	return m, m.copyClipboardTextCmd(text)
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
		if current == "" {
			current = "unknown"
		}
		return m, c.model.appendLocalEntryWithNoticeID("system", "Thinking level is "+current, "")
	}

	normalized, ok := clientui.NormalizeThinkingLevel(requested)
	if !ok {
		errText := "invalid thinking level " + strconv.Quote(requested) + " (expected low|medium|high|xhigh)"
		return m, c.model.appendLocalEntryWithNoticeID("error", errText, "")
	}
	if m.hasRuntimeClient() {
		return m, m.runtimeControlCommand(runtimeControlSetThinkingLevel, normalized, false, "")
	}
	m.thinkingLevel = normalized
	return m, c.model.appendLocalEntryWithNoticeID("system", "Thinking level set to "+m.thinkingLevel, "")
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
	return m, c.appendSystemFeedbackWithMirroredStatus(status, uiStatusNoticeNeutral)
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
	return m, c.appendSystemFeedbackWithMirroredStatus(status, uiStatusNoticeNeutral)
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
	return m, c.appendSystemFeedbackWithMirroredStatus(status, uiStatusNoticeNeutral)
}
