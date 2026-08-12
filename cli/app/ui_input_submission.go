package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/runtimeids"
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
	return c.startSubmissionWithPreSubmitQueuePositionAndOriginAndOrder(text, queuePosition, queuedID, origin, nil)
}

func (c uiInputController) startSubmissionWithPreSubmitQueuePositionAndOriginAndOrder(
	text string,
	queuePosition preSubmitQueuePosition,
	queuedID string,
	origin activeSubmitOrigin,
	submissionOrder *inputSubmissionOrder,
) tea.Cmd {
	return c.startTypedSubmissionWithPreSubmitQueuePositionAndOrder(
		text,
		runtimeinput.Text(text),
		queuePosition,
		queuedID,
		origin,
		submissionOrder,
	)
}

func (c uiInputController) startTypedSubmissionWithPreSubmitQueuePosition(text string, input runtimeinput.Input, queuePosition preSubmitQueuePosition, queuedID string, origin activeSubmitOrigin) tea.Cmd {
	return c.startTypedSubmissionWithPreSubmitQueuePositionAndOrder(text, input, queuePosition, queuedID, origin, nil)
}

func (c uiInputController) startTypedSubmissionWithPreSubmitQueuePositionAndOrder(
	text string,
	input runtimeinput.Input,
	queuePosition preSubmitQueuePosition,
	queuedID string,
	origin activeSubmitOrigin,
	submissionOrder *inputSubmissionOrder,
) tea.Cmd {
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
	if isUserShell {
		return tea.Batch(c.submitUserShellCmd(text, command, origin, submissionOrder), m.reconcileSpinnerTicking(false))
	}
	return tea.Batch(c.submitCmd(text, input, queuedID, origin, submissionOrder), m.reconcileSpinnerTicking(false))
}

func (c uiInputController) startSubmissionWithPromptHistoryAndQueuePositionAndID(text string, queuePosition preSubmitQueuePosition, queuedID string) tea.Cmd {
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
	return c.startSubmissionWithPreSubmitQueuePositionAndOrigin(text, queuePosition, queuedID, activeSubmitOriginDirect)
}

func (c uiInputController) submitCmd(text string, input runtimeinput.Input, queuedID string, origin activeSubmitOrigin, submissionOrder *inputSubmissionOrder) tea.Cmd {
	m := c.model
	clientRequestID := runtimeids.NewRuntimeClientRequestID()
	token := m.beginSubmitAttempt(text, queuedID, origin, clientRequestID, submissionOrder)
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", text, errors.New("runtime engine is not configured"))
		}
		submission, err := m.submitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
			ClientRequestID: clientRequestID,
			Input:           input,
		})
		if err != nil {
			if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) && errors.Is(err, context.Canceled) {
				return newSubmitDoneMsg(token, "", text, runtimeattach.ErrSubmissionInterrupted)
			}
			return newSubmitDoneMsg(token, "", text, err)
		}
		message := ""
		if submission.Message != nil {
			message = *submission.Message
		}
		done := newSubmitDoneMsg(token, message, text, nil)
		done.queued = submission.Queued
		resultKind := submission.ResultKind
		done.resultKind = &resultKind
		return done
	}
}

func (c uiInputController) submitUserShellCmd(originalText, command string, origin activeSubmitOrigin, submissionOrder *inputSubmissionOrder) tea.Cmd {
	m := c.model
	token := m.beginSubmitAttempt(originalText, "", origin, runtimeids.RuntimeClientRequestID{}, submissionOrder)
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", originalText, errors.New("runtime engine is not configured"))
		}
		err := m.submitRuntimeShell(context.Background(), clientui.RuntimeShellRequest{Command: command})
		if err != nil {
			if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) && isRuntimeOperationInterrupted(err) {
				return newSubmitDoneMsg(token, "", originalText, runtimeattach.ErrSubmissionInterrupted)
			}
			return newSubmitDoneMsg(token, "", originalText, err)
		}
		return newSubmitDoneMsg(token, "", originalText, nil)
	}
}

func (m *uiModel) beginSubmitAttempt(
	text string,
	queuedID string,
	origin activeSubmitOrigin,
	clientRequestID runtimeids.RuntimeClientRequestID,
	submissionOrder *inputSubmissionOrder,
) uint64 {
	if m == nil {
		return 0
	}
	m.submitToken++
	if m.submitToken == 0 {
		m.submitToken++
	}
	var order inputSubmissionOrder
	if submissionOrder == nil {
		order = m.nextPendingInputSubmissionOrder()
	} else {
		order = *submissionOrder
	}
	m.activeSubmit = activeSubmitState{
		token:           m.submitToken,
		text:            text,
		queuedID:        queuedID,
		origin:          origin,
		clientRequestID: clientRequestID,
		submissionOrder: order,
	}
	return m.submitToken
}

type uiCompactionOrigin uint8

const (
	uiCompactionOriginManual uiCompactionOrigin = iota
	uiCompactionOriginQueued
)

func (c uiInputController) startCompactionWithOrigin(submittedText, args string, origin uiCompactionOrigin) tea.Cmd {
	m := c.model
	switch origin {
	case uiCompactionOriginManual:
	case uiCompactionOriginQueued:
		m.postTurnCompactionsInFlight++
	default:
		panic("compact request has invalid dispatch origin")
	}
	m.logf("compaction.start args_chars=%d", len(strings.TrimSpace(args)))
	m.layout().syncViewport()
	return c.compactCmd(submittedText, args, origin)
}

func (c uiInputController) compactCmd(submittedText, args string, origin uiCompactionOrigin) tea.Cmd {
	m := c.model
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return compactDoneMsg{
				submittedText: submittedText,
				origin:        origin,
				err: serverapi.NewRuntimeCommandNotAcceptedError(
					errors.New("runtime engine is not configured"),
				),
			}
		}
		err := client.CompactRuntime(context.Background(), clientui.RuntimeCompactRequest{Args: args})
		return compactDoneMsg{
			submittedText: submittedText,
			origin:        origin,
			invoked:       true,
			err:           err,
		}
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
		m.postTurnCompactionsInFlight > 0 ||
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
	restoreSubmittedText := msg.err != nil && (msg.token == 0 || m.shouldRestoreSubmittedTextOnSubmitError(msg.err))
	activeQueuedID := m.activeSubmit.queuedID
	if msg.err == nil && msg.queued.ID != "" {
		m.registerSteeredQueuedUserMessage(msg.queued)
	}
	m.activeSubmit = activeSubmitState{}
	c.finishRuntimeOperationAffordance(false)
	if msg.token == 0 || !m.hasRuntimeClient() {
		_ = m.applyRuntimeActivityProjection(clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle})
	}
	m.discardQueuedInput(activeQueuedID)
	if msg.err != nil {
		if m.turnQueueHook != nil {
			m.turnQueueHook.OnTurnQueueAborted()
		}
		if isRuntimeOperationInterrupted(msg.err) &&
			!errors.Is(msg.err, serverapi.ErrRuntimeCommandNotAccepted) &&
			m.hasPendingInterrupt() {
			m.activity = uiActivityInterrupted
			m.logf("step.interrupted")
			m.layout().syncViewport()
			return m, m.interruptedStatusNoticeCmd()
		}
		restoreInjectedCmd := tea.Cmd(nil)
		if submitOrigin != activeSubmitOriginQueued {
			restoreInjectedCmd = c.restoreInjectedInputsIntoComposer()
		}
		if restoreSubmittedText {
			c.restoreSubmittedTextIntoInput(msg.submittedText)
		}
		c.restoreQueuedMessagesIntoInput()
		c.notifyTurnQueueDrainedIfIdle()
		if isRuntimeOperationInterrupted(msg.err) {
			m.activity = uiActivityInterrupted
			m.logf("step.interrupted")
			m.layout().syncViewport()
			return m, tea.Batch(restoreInjectedCmd, m.interruptedStatusNoticeCmd())
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
				return m, tea.Batch(restoreInjectedCmd, statusCmd, refreshCmd)
			}
		}
		return m, tea.Batch(restoreInjectedCmd, statusCmd)
	}

	if !m.runtimeActivityBusy() {
		m.activity = uiActivityIdle
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
		return next, drainCmd
	}
	c.notifyTurnQueueDrainedIfIdle()
	m.layout().syncViewport()
	return m, nil
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
	compactionOrigin := msg.origin
	notAccepted := errors.Is(msg.err, serverapi.ErrRuntimeCommandNotAccepted)
	if !msg.invoked && !notAccepted {
		msg.err = serverapi.NewRuntimeCommandNotAcceptedError(
			errors.New("compact request completed without dispatch"),
		)
		notAccepted = true
	}
	if compactionOrigin == uiCompactionOriginQueued {
		if m.postTurnCompactionsInFlight == 0 {
			if m.debugMode {
				panic("compact completion has no matching post-turn request")
			}
			msg.err = serverapi.NewRuntimeCommandNotAcceptedError(
				errors.New("compact completion has no matching post-turn request"),
			)
			notAccepted = true
		} else {
			m.postTurnCompactionsInFlight--
		}
	}
	if msg.invoked {
		m.observeRuntimeRequestResult(msg.err)
	}
	if msg.err != nil {
		if notAccepted {
			c.restoreSubmittedTextIntoInput(msg.submittedText)
			if compactionOrigin == uiCompactionOriginQueued {
				if m.turnQueueHook != nil {
					m.turnQueueHook.OnTurnQueueAborted()
				}
				c.restoreQueuedMessagesIntoInput()
			}
			detailErr := runtimeattach.FormatSubmissionError(msg.err)
			m.logf("compaction.error err=%q", detailErr)
			m.layout().syncViewport()
			return m, m.sendTransientStatusWithNoticeID(
				detailErr,
				uiStatusNoticeError,
				transientStatusDuration,
				uiStatusNoticeReplace,
				"",
			)
		}
		detailErr := runtimeattach.FormatSubmissionError(msg.err)
		m.activity = uiActivityError
		appendCmd := m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, detailErr, "")
		m.logf("compaction.error err=%q", detailErr)
		next, advanceCmd := c.advanceAfterAcceptedCompaction(compactionOrigin)
		return next, tea.Batch(appendCmd, advanceCmd)
	}

	m.logf("compaction.done")
	return c.advanceAfterAcceptedCompaction(compactionOrigin)
}

func (c uiInputController) advanceAfterAcceptedCompaction(origin uiCompactionOrigin) (tea.Model, tea.Cmd) {
	m := c.model
	if len(m.queued) > 0 {
		c.notifyUserCompactionCompleted(origin, false)
		if origin != uiCompactionOriginQueued {
			m.layout().syncViewport()
			return m, nil
		}
		next, cmd := c.flushQueuedInputs(queueDrainAuto)
		c.notifyTurnQueueDrainedIfIdle()
		return next, cmd
	}
	if m.injectedQueueBlocksDrain() || m.hasEnqueuedInjectedRuntimeWork() {
		c.notifyUserCompactionCompleted(origin, false)
		m.layout().syncViewport()
		return m, nil
	}
	c.notifyUserCompactionCompleted(origin, true)
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

func (m *uiModel) shouldRestoreSubmittedTextOnSubmitError(err error) bool {
	if m == nil || err == nil {
		return false
	}
	if errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		return true
	}
	if !m.hasRuntimeClient() {
		return true
	}
	return false
}

func isRuntimeOperationInterrupted(err error) bool {
	return errors.Is(err, runtimeattach.ErrSubmissionInterrupted) ||
		errors.Is(err, context.Canceled)
}
