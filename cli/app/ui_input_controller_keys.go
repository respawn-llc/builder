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
	if m.view.Mode() == tui.ModeDetail && inputState.Mode != uiInputModeRollbackEdit {
		switch msg.Type {
		case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
			m.forwardToView(tea.KeyMsg{Type: msg.Type})
			return m, m.maybeRequestDetailTranscriptPage()
		case tea.KeyEnter:
			m.forwardToView(tea.KeyMsg{Type: msg.Type})
			return m, nil
		case tea.KeyEsc:
			if m.isBusy() ||
				m.isInputSubmitLocked() ||
				strings.TrimSpace(m.input) != "" {
				return m, nil
			}
			return c.handleIdleRollbackEsc()
		case tea.KeyShiftTab, tea.KeyCtrlT:
			return m, m.toggleTranscriptMode()
		case tea.KeyCtrlC:
			// Preserve the normal interrupt/quit path below.
		default:
			return m, nil
		}
	}
	if m.isInputSubmitLocked() &&
		isSharedInputEditKeyForGOOS(msg, runtime.GOOS) {
		return m, nil
	}
	if handleSharedInputEditKeyForGOOS(msg, uiSharedInputEditActions{
		Backspace:          m.backspaceInput,
		DeleteForward:      m.deleteForwardInput,
		DeleteBackwardWord: m.deleteBackwardWordInput,
		DeleteForwardWord:  m.deleteForwardWordInput,
		KillToLineStart:    m.killInputToLineStart,
		KillToLineEnd:      m.killInputToLineEnd,
		Yank:               m.yankInput,
		DeleteCurrentLine:  m.deleteCurrentInputLine,
	}, runtime.GOOS) {
		return m, nil
	}
	if !m.isInputSubmitLocked() {
		switch msg.Type {
		case tea.KeyTab, tea.KeyEnter:
			if m.shouldBlockPathReferenceAcceptanceKey() {
				return m, nil
			}
			if m.acceptPathReferenceSelection() {
				return m, nil
			}
		}
	}
	if isQueueSubmissionKey(msg) {
		text := strings.TrimSpace(m.input)
		if text == "" {
			return m, nil
		}
		if errText, blocked := m.slashCommandInputBlocked(text); blocked {
			return m, c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		if inputState.Mode == uiInputModeRollbackEdit && !inputState.Busy {
			return c.startRollbackFork(text)
		}
		if handled, next, cmd := c.handleQueuedSlashCommandInput(text); handled {
			return next, cmd
		}
		return c.queueOrStartSubmission(text)
	}
	if !m.isInputSubmitLocked() &&
		!msg.Alt {
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
	if !m.isInputSubmitLocked() &&
		isClipboardImagePasteKey(msg) {
		return m, m.pasteClipboardImageCmd(uiClipboardPasteTargetMain)
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		if m.isBusy() {
			return m, c.interruptBusyRuntime()
		}
		m.exitAction = UIActionExit
		return m, tea.Quit
	case tea.KeyShiftTab, tea.KeyCtrlT:
		return m, m.toggleTranscriptMode()
	case tea.KeyEsc:
		if inputState.Mode == uiInputModeRollbackEdit {
			return m, c.cancelRollbackEditingToSelectionFlowCmd()
		}
		if m.view.Mode() != tui.ModeOngoing {
			return m, nil
		}
		if m.isBusy() ||
			m.isInputSubmitLocked() ||
			strings.TrimSpace(m.input) != "" {
			return m, nil
		}
		return c.handleIdleRollbackEsc()
	case tea.KeyEnter:
		c.normalizePendingCSIShiftEnterOnEnter()
		text := strings.TrimSpace(m.input)
		if text == "" {
			if !m.isBusy() && len(m.queued) > 0 {
				return c.flushQueuedInputs(queueDrainOne)
			}
			return m, nil
		}
		if errText, blocked := m.slashCommandInputBlocked(text); blocked {
			return m, c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		if inputState.Mode == uiInputModeRollbackEdit && !inputState.Busy {
			return c.startRollbackFork(text)
		}
		if m.isBusy() {
			if handled, next, cmd := c.handleEnteredSlashCommandInput(text); handled {
				return next, cmd
			}
		}
		if blocked, disconnectCmd := c.blockDisconnectedSubmission(false, ""); blocked {
			return m, disconnectCmd
		}
		_, isUserShell := parseUserShellCommand(text)
		draftText, draftCursor, restoreDraft := m.capturePromptHistoryDraftForReuse()
		if m.isBusy() {
			if isUserShell {
				m.queueInput(text)
				m.restoreCapturedPromptHistoryDraft(draftText, draftCursor, restoreDraft)
				return m, nil
			}
			cmd := m.queueInjectedInput(text)
			m.restoreCapturedPromptHistoryDraft(draftText, draftCursor, restoreDraft)
			return m, cmd
		}
		if len(m.queued) > 0 {
			m.queueInput(text)
			m.restoreCapturedPromptHistoryDraft(draftText, draftCursor, restoreDraft)
			return c.flushQueuedInputs(queueDrainOne)
		}
		if handled, next, cmd := c.handleEnteredSlashCommandInput(text); handled {
			return next, cmd
		}
		if commandResult := m.commandRegistry.Execute(text); commandResult.Handled {
			command, _ := m.commandRegistry.Command(text)
			recordCmd := m.recordPromptHistory(text)
			m.clearCommandInput(command, draftText, draftCursor, restoreDraft)
			next, cmd := c.applyCommandResultWithPreSubmitQueuePosition(commandResult, preSubmitQueueBack)
			return next, finalizeSlashCommandCmd(commandResult.Action, cmd, recordCmd)
		}
		m.clearInput()
		m.restoreCapturedPromptHistoryDraft(draftText, draftCursor, restoreDraft)
		return m, c.startSubmissionWithPromptHistoryAndQueuePositionAndID(text, preSubmitQueueBack, "")
	case tea.KeyCtrlJ, keyTypeShiftEnterCSI:
		if m.isInputSubmitLocked() {
			return m, nil
		}
		m.insertInputRunes([]rune{'\n'})
		if msg.Type == keyTypeShiftEnterCSI {
			c.markPendingCSIShiftEnter()
		}
		return m, nil
	case tea.KeySpace:
		if m.isInputSubmitLocked() {
			return m, nil
		}
		m.insertInputRunes([]rune{' '})
		return m, nil
	case tea.KeyLeft:
		if m.isInputSubmitLocked() {
			return m, nil
		}
		if msg.Alt {
			m.moveCursorWordLeft()
			return m, nil
		}
		m.moveCursorLeft()
		return m, nil
	case tea.KeyRight:
		if m.isInputSubmitLocked() {
			return m, nil
		}
		if msg.Alt {
			m.moveCursorWordRight()
			return m, nil
		}
		m.moveCursorRight()
		return m, nil
	case tea.KeyHome, tea.KeyCtrlA:
		if m.isInputSubmitLocked() {
			return m, nil
		}
		m.moveCursorStart()
		return m, nil
	case tea.KeyEnd, tea.KeyCtrlE, tea.KeyCtrlEnd:
		if m.isInputSubmitLocked() {
			return m, nil
		}
		m.moveCursorEnd()
		return m, nil
	case tea.KeyCtrlLeft:
		if m.isInputSubmitLocked() {
			return m, nil
		}
		m.moveCursorWordLeft()
		return m, nil
	case tea.KeyCtrlRight:
		if m.isInputSubmitLocked() {
			return m, nil
		}
		m.moveCursorWordRight()
		return m, nil
	case tea.KeyUp:
		if m.isInputSubmitLocked() {
			m.forwardToView(tea.KeyMsg{Type: tea.KeyUp})
			return m, nil
		}
		m.moveCursorUpLine()
		return m, nil
	case tea.KeyDown:
		if m.isInputSubmitLocked() {
			m.forwardToView(tea.KeyMsg{Type: tea.KeyDown})
			return m, nil
		}
		m.moveCursorDownLine()
		return m, nil
	case tea.KeyPgUp, tea.KeyPgDown:
		return m, nil
	default:
		if isShiftEnterKey(msg) {
			if m.isInputSubmitLocked() {
				return m, nil
			}
			m.insertInputRunes([]rune{'\n'})
			return m, nil
		}
		if msg.Type == tea.KeyRunes {
			if m.isInputSubmitLocked() {
				return m, nil
			}
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
	return true, ringBellCmd()
}

func (c uiInputController) handleRollbackSelectionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	switch msg.Type {
	case tea.KeyCtrlC:
		m.exitAction = UIActionExit
		if overlayCmd := m.popRollbackOverlay(); overlayCmd != nil {
			m.stopRollbackSelectionMode()
			return m, tea.Sequence(overlayCmd, tea.Quit)
		}
		return m, tea.Quit
	case tea.KeyEsc:
		return m, c.stopRollbackSelectionFlowCmd()
	case tea.KeyUp:
		if m.rollback.selection <= 0 {
			if cmd := m.requestRollbackSelectionPage(-1); cmd != nil {
				return m, cmd
			}
		}
		m.moveRollbackSelection(-1)
		return m, nil
	case tea.KeyDown:
		if m.rollback.selection >= len(m.rollback.candidates)-1 {
			if cmd := m.requestRollbackSelectionPage(1); cmd != nil {
				return m, cmd
			}
		}
		m.moveRollbackSelection(1)
		return m, nil
	case tea.KeyEnter:
		return m, c.beginRollbackEditingFlowCmd()
	case tea.KeyPgUp, tea.KeyPgDown:
		m.forwardToView(msg)
		return m, nil
	default:
		return m, nil
	}
}
