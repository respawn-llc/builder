package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type preSubmitQueuePosition uint8

const (
	preSubmitQueueBack preSubmitQueuePosition = iota
	preSubmitQueueFront
)

func (c uiInputController) startSubmissionWithPreSubmitQueuePosition(text string, queuePosition preSubmitQueuePosition, queuedID string) tea.Cmd {
	if cmd, rejected := c.rejectUnavailablePromptCommand(text); rejected {
		return cmd
	}
	return c.startTypedSubmissionWithPreSubmitQueuePosition(text, runtimeinput.Text(text), queuePosition, queuedID, activeSubmitOriginDirect)
}

func (c uiInputController) startSubmissionWithPreSubmitQueuePositionAndOrigin(text string, queuePosition preSubmitQueuePosition, queuedID string, origin activeSubmitOrigin) tea.Cmd {
	return c.startTypedSubmissionWithPreSubmitQueuePosition(text, runtimeinput.Text(text), queuePosition, queuedID, origin)
}

func (c uiInputController) startTypedSubmissionWithPreSubmitQueuePosition(text string, input runtimeinput.Input, queuePosition preSubmitQueuePosition, queuedID string, origin activeSubmitOrigin) tea.Cmd {
	m := c.model
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, text); blocked {
		return disconnectCmd
	}
	if blocked, blockCmd := c.blockInjectedQueueSubmission(); blocked {
		return blockCmd
	}
	c.startRuntimeOperationAffordance(false)
	command, isUserShell := parseUserShellCommand(text)
	if isUserShell {
		m.logf("step.user_shell.start command_chars=%d", len(command))
	} else {
		m.logf("step.start user_chars=%d", len(text))
	}
	if !m.hasRuntimeClient() && !isUserShell {
		m.conversationFreshness = clientui.ConversationFreshnessEstablished
	}
	m.layout().syncViewport()
	var shellCommand *string
	if isUserShell {
		shellCommand = &command
	}
	token := m.beginTypedSubmitAttempt(text, input, shellCommand, queuedID, origin)
	return tea.Batch(m.persistActiveSubmitCmd(token), m.reconcileSpinnerTicking(false))
}

func (c uiInputController) startSubmissionWithPromptHistoryAndQueuePositionAndID(text string, queuePosition preSubmitQueuePosition, queuedID string) tea.Cmd {
	return c.startSubmissionWithPromptHistoryAndQueuePositionAndIDAndOrigin(text, queuePosition, queuedID, activeSubmitOriginDirect)
}

func (c uiInputController) startSubmissionWithPromptHistoryAndQueuePositionAndIDAndOrigin(text string, queuePosition preSubmitQueuePosition, queuedID string, origin activeSubmitOrigin) tea.Cmd {
	m := c.model
	if cmd, rejected := c.rejectUnavailablePromptCommand(text); rejected {
		return cmd
	}
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, text); blocked {
		return disconnectCmd
	}
	if blocked, blockCmd := c.blockInjectedQueueSubmission(); blocked {
		return blockCmd
	}
	m.rememberPromptHistoryLocally(text)
	return c.startSubmissionWithPreSubmitQueuePositionAndOrigin(text, queuePosition, queuedID, origin)
}

func (c uiInputController) submitActiveCmd(token uint64) tea.Cmd {
	m := c.model
	active := m.activeSubmit
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", active.text, errors.New("runtime engine is not configured"))
		}
		if active.shellCommand != nil {
			err := m.submitRuntimeShell(context.Background(), clientui.RuntimeShellRequest{Command: *active.shellCommand})
			if err != nil {
				if isRuntimeOperationInterrupted(err) {
					return newSubmitDoneMsg(token, "", active.text, runtimeattach.ErrSubmissionInterrupted)
				}
				return newSubmitDoneMsg(token, "", active.text, err)
			}
			return newSubmitDoneMsg(token, "", active.text, nil)
		}
		submission, err := m.submitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
			Input: active.input,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return newSubmitDoneMsg(token, "", active.text, runtimeattach.ErrSubmissionInterrupted)
			}
			return newSubmitDoneMsg(token, "", active.text, err)
		}
		message := ""
		if submission.Message != nil {
			message = *submission.Message
		}
		done := newSubmitDoneMsg(token, message, active.text, nil)
		done.queued = submission.Queued
		resultKind := submission.ResultKind
		done.resultKind = &resultKind
		return done
	}
}

func (c uiInputController) handleSubmitDraftPersisted(msg submitDraftPersistedMsg) (tea.Model, tea.Cmd) {
	m := c.model
	if msg.token == 0 || msg.token != m.activeSubmit.token {
		return m, nil
	}
	if msg.err == nil {
		return m, c.submitActiveCmd(msg.token)
	}
	submittedText := m.activeSubmit.text
	m.activeSubmit = activeSubmitState{}
	c.finishRuntimeOperationAffordance(false)
	c.restoreSubmittedTextIntoInput(submittedText)
	m.activity = uiActivityError
	m.layout().syncViewport()
	return m, m.sendTransientStatusWithNoticeID(
		"could not preserve submitted text: "+msg.err.Error(),
		uiStatusNoticeError,
		transientStatusDuration,
		uiStatusNoticeReplace,
		"",
	)
}

func (m *uiModel) beginSubmitAttempt(text string, queuedID string, origin activeSubmitOrigin) uint64 {
	return m.beginTypedSubmitAttempt(text, runtimeinput.Text(text), nil, queuedID, origin)
}

func (m *uiModel) beginTypedSubmitAttempt(
	text string,
	input runtimeinput.Input,
	shellCommand *string,
	queuedID string,
	origin activeSubmitOrigin,
) uint64 {
	if m == nil {
		return 0
	}
	m.submitToken++
	if m.submitToken == 0 {
		m.submitToken++
	}
	m.activeSubmit = activeSubmitState{
		token:        m.submitToken,
		text:         text,
		queuedID:     queuedID,
		origin:       origin,
		input:        input,
		shellCommand: shellCommand,
	}
	return m.submitToken
}

type uiCompactionOrigin uint8

const (
	uiCompactionOriginNone uiCompactionOrigin = iota
	uiCompactionOriginManual
	uiCompactionOriginQueued
)

func (c uiInputController) startCompactionWithOrigin(args string, origin uiCompactionOrigin) tea.Cmd {
	m := c.model
	if m.isCompacting() {
		return nil
	}
	c.startRuntimeOperationAffordance(true)
	m.compactionOrigin = origin
	m.logf("compaction.start args_chars=%d", len(strings.TrimSpace(args)))
	m.layout().syncViewport()
	return tea.Batch(c.compactCmd(args), m.reconcileSpinnerTicking(false))
}

func (c uiInputController) compactCmd(args string) tea.Cmd {
	m := c.model
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return compactDoneMsg{err: errors.New("runtime engine is not configured")}
		}
		return compactDoneMsg{err: m.compactRuntimeInput(context.Background(), clientui.RuntimeCompactRequest{Args: args})}
	}
}

func (c uiInputController) startRuntimeOperationAffordance(compacting bool) {
	m := c.model
	m.clearReviewerState()
	m.clearActiveAssistantStreamSource()
	if compacting {
		m.setCompacting(true)
	}
}

func (c uiInputController) finishRuntimeOperationAffordance(compacting bool) {
	m := c.model
	m.clearReviewerState()
	m.spinnerFrame = 0
	if !m.shouldAnimateSpinner() {
		m.stopSpinnerTicking()
	}
	if compacting {
		m.setCompacting(false)
	}
}

func (c uiInputController) notifyTurnQueueDrainedIfIdle() {
	m := c.model
	if m.turnQueueHook == nil ||
		m.blocksRuntimeInput() ||
		len(m.queued) > 0 ||
		m.injectedQueueBlocksDrain() ||
		m.hasEnqueuedInjectedRuntimeWork() ||
		m.ask.hasCurrent() {
		return
	}
	m.turnQueueHook.OnTurnQueueDrained()
}

func (c uiInputController) handleSubmitDone(msg submitDoneMsg) (tea.Model, tea.Cmd) {
	m := c.model
	if msg.token == 0 && m.activeSubmit.token != 0 && strings.TrimSpace(msg.submittedText) != "" {
		return m, nil
	}
	if msg.token != 0 && msg.token != m.activeSubmit.token {
		return m, nil
	}
	submitOrigin := m.activeSubmit.origin
	m.observeRuntimeRequestResult(msg.err)
	activeQueuedID := m.activeSubmit.queuedID
	c.finishRuntimeOperationAffordance(false)
	if msg.token == 0 || !m.hasRuntimeClient() {
		_ = m.applyRuntimeActivityProjection(clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle})
	}
	m.discardQueuedInput(activeQueuedID)
	if msg.err != nil {
		// The command has finished once its direct result arrives, regardless
		// of whether the server can prove that it was applied. Keep the text
		// in the editable draft below; do not leave a completed dispatch as
		// local input authority.
		m.activeSubmit = activeSubmitState{}
		if m.turnQueueHook != nil {
			m.turnQueueHook.OnTurnSubmissionAborted()
		}
		restoreInjectedCmd := tea.Cmd(nil)
		if submitOrigin != activeSubmitOriginQueued {
			restoreInjectedCmd = c.restorePendingInjectedIntoInput()
		}
		c.restoreSubmittedTextIntoInput(msg.submittedText)
		c.restoreQueuedMessagesIntoInput()
		c.notifyTurnQueueDrainedIfIdle()
		if isRuntimeOperationInterrupted(msg.err) {
			m.activity = uiActivityInterrupted
			m.logf("step.interrupted")
			m.layout().syncViewport()
			return m, tea.Batch(restoreInjectedCmd, m.interruptedStatusNoticeCmd(), m.persistSessionDraftRecoveryCmd())
		}
		detailErr := runtimeattach.FormatSubmissionError(msg.err)
		if submitOrigin != activeSubmitOriginQueued {
			m.activity = uiActivityError
		}
		m.logf("step.error err=%q", detailErr)
		m.layout().syncViewport()
		statusCmd := m.sendTransientStatusWithNoticeID(detailErr, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		if notFound, ok := promptCommandNotFound(msg.err); ok && notFound.Command != nil {
			if refreshCmd := m.startPromptCatalogRefresh(*notFound.Command); refreshCmd != nil {
				return m, tea.Batch(restoreInjectedCmd, statusCmd, refreshCmd, m.persistSessionDraftRecoveryCmd())
			}
		}
		return m, tea.Batch(restoreInjectedCmd, statusCmd, m.persistSessionDraftRecoveryCmd())
	}

	m.activeSubmit = activeSubmitState{}
	if !m.runtimeActivityBusy() {
		m.activity = uiActivityIdle
	}
	if msg.queued.ID != "" {
		m.registerOwnedSteeredQueuedUserMessage(msg.queued)
	}
	if msg.resultKind != nil &&
		*msg.resultKind == clientui.UserTurnResultKindSilentFinal &&
		m.turnQueueHook != nil {
		m.turnQueueHook.OnTurnQueueAborted()
	}
	m.conversationFreshness = clientui.ConversationFreshnessEstablished
	m.localConversationTurn = true
	m.logf("step.done assistant_chars=%d", len(msg.message))
	m.clearActiveAssistantStreamSource()
	if len(m.queued) > 0 {
		next, drainCmd := c.flushQueuedInputs(queueDrainAuto)
		c.notifyTurnQueueDrainedIfIdle()
		return next, tea.Batch(drainCmd, m.persistSessionDraftRecoveryCmd())
	}
	c.notifyTurnQueueDrainedIfIdle()
	m.layout().syncViewport()
	return m, m.persistSessionDraftRecoveryCmd()
}

func (c uiInputController) handleSpinnerTick(msg spinnerTickMsg) (tea.Model, tea.Cmd) {
	m := c.model
	if msg.token == 0 || msg.token != m.spinnerTickToken {
		return m, nil
	}
	if !m.shouldAnimateSpinner() {
		m.stopSpinnerTicking()
		return m, nil
	}
	frameCount := len(pendingToolSpinner.Frames)
	if frameCount <= 0 {
		frameCount = 1
	}
	tickAt := msg.at
	if tickAt.IsZero() {
		tickAt = m.spinnerClock.anchor
		if tickAt.IsZero() {
			tickAt = uiAnimationNow()
		}
		tickAt = tickAt.Add(time.Duration(m.spinnerFrame+1) * spinnerTickInterval)
	}
	m.spinnerFrame = m.spinnerClock.Frame(tickAt, frameCount, spinnerTickInterval)
	m.layout().syncViewport()
	return m, m.scheduleSpinnerTick(msg.token, tickAt)
}

func (c uiInputController) handleCompactDone(msg compactDoneMsg) (tea.Model, tea.Cmd) {
	m := c.model
	serverActiveBeforeCompletion := m.runtimeActivityBusy()
	compactionOrigin := m.compactionOrigin
	m.compactionOrigin = uiCompactionOriginNone
	m.observeRuntimeRequestResult(msg.err)
	c.finishRuntimeOperationAffordance(true)
	if msg.err != nil {
		restoreInjectedCmd := c.restorePendingInjectedIntoInput()
		c.restoreQueuedMessagesIntoInput()
		if isRuntimeOperationInterrupted(msg.err) {
			m.activity = uiActivityInterrupted
			m.logf("step.interrupted")
			m.layout().syncViewport()
			return m, tea.Batch(restoreInjectedCmd, m.interruptedStatusNoticeCmd())
		}
		detailErr := runtimeattach.FormatSubmissionError(msg.err)
		m.activity = uiActivityError
		appendCmd := m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, detailErr, "")
		m.logf("compaction.error err=%q", detailErr)
		m.layout().syncViewport()
		return m, tea.Batch(restoreInjectedCmd, appendCmd)
	}

	if !serverActiveBeforeCompletion {
		m.activity = uiActivityIdle
	}
	m.logf("compaction.done")
	if len(m.queued) > 0 {
		c.notifyUserCompactionCompleted(compactionOrigin, false)
		next, cmd := c.flushQueuedInputs(queueDrainAuto)
		c.notifyTurnQueueDrainedIfIdle()
		return next, cmd
	}
	if m.injectedQueueBlocksDrain() || m.hasEnqueuedInjectedRuntimeWork() {
		c.notifyUserCompactionCompleted(compactionOrigin, false)
		m.layout().syncViewport()
		return m, nil
	}
	c.notifyUserCompactionCompleted(compactionOrigin, true)
	m.layout().syncViewport()
	return m, nil
}

func (c uiInputController) notifyUserCompactionCompleted(origin uiCompactionOrigin, queueDrained bool) {
	m := c.model
	if m == nil || m.turnQueueHook == nil {
		return
	}
	switch origin {
	case uiCompactionOriginManual, uiCompactionOriginQueued:
		m.turnQueueHook.OnUserCompactionCompleted(queueDrained)
	}
}

func isRuntimeOperationInterrupted(err error) bool {
	return errors.Is(err, runtimeattach.ErrSubmissionInterrupted) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, serverapi.ErrRuntimeOperationCanceled)
}
