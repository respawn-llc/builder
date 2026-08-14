package app

import (
	"runtime"
	"strings"
	"time"

	"core/cli/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func (c uiInputController) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	inputState := m.inputModeState()
	if msg.Type != tea.KeyEnter && msg.Type != keyTypeShiftEnterCSI {
		c.clearPendingCSIShiftEnter()
	}
	if msg.Type != tea.KeyEsc {
		m.lastEscAt = time.Time{}
	}
	if inputState.Mode == uiInputModeRollbackSelection {
		return c.handleRollbackSelectionKey(msg)
	}
	if inputState.Mode == uiInputModeStatus {
		next, cmd := c.handleStatusOverlayKey(msg)
		next.(*uiModel).layout().syncViewport()
		return next, cmd
	}
	if inputState.Mode == uiInputModeGoal {
		next, cmd := c.handleGoalOverlayKey(msg)
		next.(*uiModel).layout().syncViewport()
		return next, cmd
	}
	if inputState.Mode == uiInputModeWorktree {
		next, cmd := c.handleWorktreeOverlayKey(msg)
		next.(*uiModel).layout().syncViewport()
		return next, cmd
	}
	if inputState.Mode == uiInputModeProcessList {
		next, cmd := c.handleProcessListKey(msg)
		next.(*uiModel).layout().syncViewport()
		return next, cmd
	}
	if m.view.Mode() == tui.ModeDetail {
		switch msg.Type {
		case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
			return m, m.forwardToView(tea.KeyMsg{Type: msg.Type})
		case tea.KeyEnter:
			return m, m.forwardToView(tea.KeyMsg{Type: msg.Type})
		case tea.KeyEsc:
			if m.blocksRuntimeInput() ||
				strings.TrimSpace(m.mainEditor.Text()) != "" {
				return m, nil
			}
			return c.handleIdleRollbackEsc()
		case tea.KeyTab, tea.KeyShiftTab, tea.KeyCtrlT:
			return m, m.toggleTranscriptMode()
		case tea.KeyCtrlC:
			// Preserve the normal interrupt/quit path below.
		default:
			return m, nil
		}
	}
	if result := applySharedInputEditKeyForGOOS(msg, &m.mainEditor, runtime.GOOS); result.Handled {
		if result.Mutated {
			m.mainInputMutated()
		}
		return m, nil
	}
	switch msg.Type {
	case tea.KeyTab, tea.KeyEnter:
		if m.shouldBlockPathReferenceAcceptanceKey() {
			return m, nil
		}
		if m.acceptPathReferenceSelection() {
			return m, nil
		}
	}
	if isQueueSubmissionKey(msg) {
		submittedText := m.mainEditor.Text()
		trimmedText := strings.TrimSpace(submittedText)
		if trimmedText == "" {
			return m, nil
		}
		if errText, blocked := m.slashCommandInputBlocked(trimmedText); blocked {
			return m, c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		if handled, next, cmd := c.handleQueuedSlashCommandInput(submittedText); handled {
			return next, cmd
		}
		return c.queueOrStartSubmission(submittedText)
	}
	if !msg.Alt {
		switch msg.Type {
		case tea.KeyUp:
			if m.navigateSlashCommandPicker(-1) {
				return m, nil
			}
			if m.navigatePathReferencePicker(-1) {
				return m, nil
			}
			if handled, cmd := c.handlePromptHistoryKey(-1); handled {
				return m, cmd
			}
		case tea.KeyDown:
			if m.navigateSlashCommandPicker(1) {
				return m, nil
			}
			if m.navigatePathReferencePicker(1) {
				return m, nil
			}
			if handled, cmd := c.handlePromptHistoryKey(1); handled {
				return m, cmd
			}
		case tea.KeyLeft:
			if m.navigateSlashCommandPicker(-1) {
				return m, nil
			}
		case tea.KeyRight:
			if m.navigateSlashCommandPicker(1) {
				return m, nil
			}
		}
	}
	if isClipboardPasteKey(msg) {
		return m, m.pasteClipboardCmd(uiClipboardPasteTargetMain)
	}
	if applySharedInputMovementKey(msg, &m.mainEditor) {
		m.refreshAutocompleteStateFromInput()
		return m, nil
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		return c.handleRuntimeCtrlC(nil)
	case tea.KeyShiftTab, tea.KeyCtrlT:
		return m, m.toggleTranscriptMode()
	case tea.KeyEsc:
		if m.view.Mode() != tui.ModeOngoing {
			return m, nil
		}
		if m.blocksRuntimeInput() ||
			strings.TrimSpace(m.mainEditor.Text()) != "" {
			return m, nil
		}
		return c.handleIdleRollbackEsc()
	case tea.KeyEnter:
		c.normalizePendingCSIShiftEnterOnEnter()
		submittedText := m.mainEditor.Text()
		trimmedText := strings.TrimSpace(submittedText)
		if trimmedText == "" {
			if !m.blocksRuntimeInput() && len(m.queued) > 0 {
				return c.flushQueuedInputs(queueDrainOne)
			}
			return m, nil
		}
		if errText, blocked := m.slashCommandInputBlocked(trimmedText); blocked {
			return m, c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		if m.blocksRuntimeInput() {
			if handled, next, cmd := c.handleEnteredSlashCommandInput(submittedText); handled {
				return next, cmd
			}
		}
		if blocked, disconnectCmd := c.blockDisconnectedSubmission(false, ""); blocked {
			return m, disconnectCmd
		}
		_, isUserShell := parseUserShellCommand(trimmedText)
		draft := m.capturePromptHistoryDraftForReuse()
		if m.blocksRuntimeInput() {
			if isUserShell {
				m.queueInput(submittedText)
				m.restoreCapturedPromptHistoryDraft(draft)
				return m, nil
			}
			cmd := m.queueInjectedInput(submittedText)
			m.restoreCapturedPromptHistoryDraft(draft)
			return m, cmd
		}
		if len(m.queued) > 0 {
			m.queueInput(submittedText)
			m.restoreCapturedPromptHistoryDraft(draft)
			return c.flushQueuedInputs(queueDrainOne)
		}
		if handled, next, cmd := c.handleEnteredSlashCommandInput(submittedText); handled {
			return next, cmd
		}
		if commandResult := m.commandRegistry.Execute(trimmedText); commandResult.Handled {
			command, _ := m.commandRegistry.Command(trimmedText)
			recordCmd := m.recordPromptHistory(trimmedText)
			m.clearCommandInput(command, draft)
			next, cmd := c.applyCommandResultWithPreSubmitQueuePositionAndSubmittedText(
				commandResult,
				preSubmitQueueBack,
				activeSubmitOriginDirect,
				submittedText,
			)
			return next, finalizeSlashCommandCmd(commandResult.Action, cmd, recordCmd)
		}
		m.clearInput()
		m.restoreCapturedPromptHistoryDraft(draft)
		return m, c.startSubmissionWithPromptHistoryAndQueuePositionAndID(submittedText, preSubmitQueueBack, "")
	case tea.KeyCtrlJ, keyTypeShiftEnterCSI:
		m.insertInputRunes([]rune{'\n'})
		if msg.Type == keyTypeShiftEnterCSI {
			c.markPendingCSIShiftEnter()
		}
		return m, nil
	case tea.KeySpace:
		m.insertInputRunes([]rune{' '})
		return m, nil
	case tea.KeyHome, tea.KeyCtrlA:
		m.mainEditor.MoveLineStart()
		m.refreshAutocompleteStateFromInput()
		return m, nil
	case tea.KeyEnd, tea.KeyCtrlE, tea.KeyCtrlEnd:
		m.mainEditor.MoveLineEnd()
		m.refreshAutocompleteStateFromInput()
		return m, nil
	case tea.KeyUp:
		m.moveMainCursorVertical(-1)
		return m, nil
	case tea.KeyDown:
		m.moveMainCursorVertical(1)
		return m, nil
	case tea.KeyPgUp, tea.KeyPgDown:
		return m, nil
	default:
		if isShiftEnterKey(msg) {
			m.insertInputRunes([]rune{'\n'})
			return m, nil
		}
		if msg.Type == tea.KeyRunes {
			return m, m.insertInputRunes(msg.Runes)
		}
		return m, nil
	}
}

func (c uiInputController) handleIdleRollbackEsc() (tea.Model, tea.Cmd) {
	m := c.model
	now := time.Now()
	if !m.lastEscAt.IsZero() && now.Sub(m.lastEscAt) <= rollbackDoubleEscWindow {
		m.lastEscAt = time.Time{}
		return m, c.startRollbackSelectionFlowCmd()
	}
	m.lastEscAt = now
	return m, nil
}

func (c uiInputController) handlePromptHistoryKey(delta int) (bool, tea.Cmd) {
	m := c.model
	if !m.shouldAttemptPromptHistoryNavigation(delta) {
		return false, nil
	}
	if m.navigatePromptHistory(delta) {
		return true, nil
	}
	return true, ringBellCmd(m.terminalOutput)
}

func (c uiInputController) handleRollbackSelectionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	switch msg.Type {
	case tea.KeyCtrlC:
		return c.handleRuntimeCtrlC(func() tea.Cmd {
			return c.stopRollbackSelectionFlowCmd()
		})
	case tea.KeyEsc:
		return m, c.stopRollbackSelectionFlowCmd()
	}
	if m.rollback.pendingNavigation != nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyUp:
		return m, m.moveRollbackSelectionWithPaging(-1)
	case tea.KeyDown:
		return m, m.moveRollbackSelectionWithPaging(1)
	case tea.KeyEnter:
		return c.startRollbackFork()
	case tea.KeyPgUp:
		return m, m.pageRollbackSelection(-1)
	case tea.KeyPgDown:
		return m, m.pageRollbackSelection(1)
	case tea.KeyHome:
		m.jumpRollbackSelection(-1)
		return m, nil
	case tea.KeyEnd:
		m.jumpRollbackSelection(1)
		return m, nil
	case tea.KeyRunes:
		if len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'k':
				return m, m.moveRollbackSelectionWithPaging(-1)
			case 'j':
				return m, m.moveRollbackSelectionWithPaging(1)
			}
		}
		return m, nil
	default:
		return m, nil
	}
}
