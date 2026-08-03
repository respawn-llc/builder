package app

import (
	"fmt"
	"strings"

	"core/cli/app/commands"
	tuiinput "core/cli/tui/input"
	"core/shared/runtimeinput"

	tea "github.com/charmbracelet/bubbletea"
)

func (c uiInputController) handleQueuedSlashCommandInput(text string) (bool, tea.Model, tea.Cmd) {
	m := c.model
	selection := m.resolveSlashCommandSelection(text)
	if selection.shouldAutocomplete() {
		m.replaceMainInputAtEnd(selection.autocompleteText())
		return true, m, nil
	}
	if !selection.hasCommand || selection.commandText() == "" {
		if cmd, rejected := c.rejectUnavailablePromptCommand(text); rejected {
			return true, m, cmd
		}
		return false, m, nil
	}
	if errText, blocked := m.blockedDeferredSlashCommand(selection.commandText()); blocked {
		return true, m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", errText, ""), c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	next, cmd := c.queueOrStartSubmission(selection.commandText())
	return true, next, cmd
}

func (c uiInputController) handleEnteredSlashCommandInput(text string) (bool, tea.Model, tea.Cmd) {
	m := c.model
	selection := m.resolveSlashCommandSelection(text)
	if !selection.hasCommand {
		if cmd, rejected := c.rejectUnavailablePromptCommand(text); rejected {
			return true, m, cmd
		}
		return false, m, nil
	}
	commandText := selection.commandText()
	if commandText == "" {
		return false, m, nil
	}
	command := selection.command
	if m.isBusy() {
		switch command.ActiveRunPolicy {
		case commands.ActiveRunPolicyAllowed:
		default:
			m.clearInput()
			return true, m, c.model.sendTransientStatusWithNoticeID(fmt.Sprintf("cannot run /%s while model is working", command.Name), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
	}
	commandResult := m.commandRegistry.Execute(commandText)
	if commandResult.Handled {
		draft := m.capturePromptHistoryDraftForReuse()
		var recordCmd tea.Cmd
		if commandResult.PromptCommand == nil {
			recordCmd = m.recordPromptHistory(commandText)
		}
		m.clearCommandInput(command, draft)
		next, cmd := c.applyCommandResultWithPreSubmitQueuePosition(commandResult, preSubmitQueueBack)
		return true, next, finalizeSlashCommandCmd(commandResult.Action, cmd, recordCmd)
	}
	return false, m, nil
}

func (c uiInputController) rejectUnavailablePromptCommand(text string) (tea.Cmd, bool) {
	if _, reserved := promptCommandToken(text); !reserved {
		return nil, false
	}
	return c.model.sendTransientStatusWithNoticeID("prompt command is unavailable", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""), true
}

func promptCommandToken(text string) (runtimeinput.CommandToken, bool) {
	parsed := parseSlashCommandInput(text)
	if !parsed.active || parsed.token == "" {
		return runtimeinput.CommandToken{}, false
	}
	token, err := runtimeinput.ParseCommandToken(parsed.token)
	if err == nil && token.Namespace == runtimeinput.NamespacePrompt {
		return token, true
	}
	return runtimeinput.CommandToken{}, false
}

func promptCommandInput(text string) (runtimeinput.Input, bool) {
	parsed := parseSlashCommandInput(text)
	if !parsed.active {
		return runtimeinput.Input{}, false
	}
	name, err := runtimeinput.ParsePromptCommandName(parsed.token)
	if err != nil {
		return runtimeinput.Input{}, false
	}
	return runtimeinput.Command(name.String(), parsed.args), true
}

func (m *uiModel) clearCommandInput(command commands.Command, draft *tuiinput.EditorSnapshot) {
	m.clearInput()
	if command.PreservePromptHistoryDraft {
		m.restoreCapturedPromptHistoryDraft(draft)
		return
	}
	m.resetPromptHistoryNavigation()
}

func finalizeSlashCommandCmd(action commands.Action, primary tea.Cmd, record tea.Cmd) tea.Cmd {
	if action == commands.ActionStatus {
		return tea.Batch(primary, record)
	}
	return sequenceCmds(record, primary)
}

func (m *uiModel) blockedDeferredSlashCommand(commandText string) (string, bool) {
	if m.commandRegistry == nil {
		return "", false
	}
	commandResult := m.commandRegistry.Execute(commandText)
	if !commandResult.Handled {
		return "", false
	}
	switch commandResult.Action {
	case commands.ActionBack:
		if !m.hasNavigationTargetSession() {
			return "No parent session available", true
		}
	case commands.ActionSetFast:
		available, _ := m.fastModeState()
		if !available {
			return "Fast mode is only available for OpenAI-based Responses providers", true
		}
	case commands.ActionProcesses:
		args := strings.Fields(strings.TrimSpace(commandResult.Args))
		if len(args) > 0 && m.processClient == nil {
			return "background process client is unavailable", true
		}
	case commands.ActionWorktree:
		if m.isBusy() {
			return "cannot run /worktree while model is working", true
		}
		if m.worktreeClient == nil {
			return "worktree client is unavailable", true
		}
	}
	return "", false
}
