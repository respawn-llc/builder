package app

import (
	"context"
	"errors"
	"fmt"
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
	operationRef := newRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	preSubmitCompactionRef := newRuntimeOperationRef(clientui.RuntimeOperationKindPreSubmitCompact)
	dispatch := submitDispatch{
		kind:                            submitDispatchUserTurn,
		input:                           input,
		preSubmitCompactionOperationRef: preSubmitCompactionRef,
	}
	command, isUserShell := parseUserShellCommand(text)
	if isUserShell {
		operationRef = newRuntimeOperationRef(clientui.RuntimeOperationKindUserShell)
		dispatch = submitDispatch{kind: submitDispatchUserShell, shellCommand: command}
	} else {
		m.addPendingRuntimeOperation(preSubmitCompactionRef)
	}
	token := m.beginSubmitAttempt(text, queuedID, operationRef, origin)
	m.holdActiveSubmitInput()
	c.startRuntimeOperationAffordance(false)
	if isUserShell {
		m.logf("step.user_shell.start command_chars=%d", len(command))
	} else {
		m.logf("step.start user_chars=%d", len(text))
	}
	if !m.hasRuntimeClient() && !isUserShell {
		m.conversationFreshness = clientui.ConversationFreshnessEstablished
	}
	m.layout().syncViewport()
	if strings.TrimSpace(m.sessionID) == "" {
		m.releaseActiveSubmitInput()
		return tea.Batch(c.dispatchPreparedSubmit(token, text, dispatch), m.reconcileSpinnerTicking(false))
	}
	return tea.Batch(c.prepareSubmitDraftCmd(token, text, dispatch), m.reconcileSpinnerTicking(false))
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

func (c uiInputController) prepareSubmitDraftCmd(token uint64, text string, dispatch submitDispatch) tea.Cmd {
	m := c.model
	client := m.sessionDrafts
	sessionID := m.sessionID
	inputDraft := m.sessionDraftInput()
	recoveryBuffers := m.sessionDraftRecoveryBuffers()
	draftToken := m.mainInputDraftToken
	return func() tea.Msg {
		err := persistSessionDraft(context.Background(), client, sessionID, inputDraft, recoveryBuffers)
		return submitDoneMsg{
			phase:         submitPhaseDraftPrepared,
			dispatch:      dispatch,
			token:         token,
			draftToken:    draftToken,
			submittedText: text,
			err:           err,
		}
	}
}

func (c uiInputController) submitCmd(
	token uint64,
	text string,
	input runtimeinput.Input,
	operationRef clientui.RuntimeOperationRef,
	preSubmitCompactionOperationRef clientui.RuntimeOperationRef,
) tea.Cmd {
	m := c.model
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", text, errors.New("runtime engine is not configured"))
		}
		submission, err := m.submitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
			OperationRef:                    operationRef,
			PreSubmitCompactionOperationRef: preSubmitCompactionOperationRef,
			Input:                           input,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, serverapi.ErrRuntimeOperationCanceled) {
				return newSubmitDoneMsg(token, "", text, runtimeattach.ErrSubmissionInterrupted)
			}
			return newSubmitDoneMsg(token, "", text, err)
		}
		done := newSubmitDoneMsg(token, submission.Message, text, nil)
		done.queued = submission.Queued
		return done
	}
}

func (c uiInputController) submitUserShellCmd(token uint64, originalText, command string, operationRef clientui.RuntimeOperationRef) tea.Cmd {
	m := c.model
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", originalText, errors.New("runtime engine is not configured"))
		}
		err := m.submitRuntimeShell(context.Background(), clientui.RuntimeShellRequest{
			OperationRef: operationRef,
			Command:      command,
		})
		if err != nil {
			if isRuntimeOperationInterrupted(err) {
				return newSubmitDoneMsg(token, "", originalText, runtimeattach.ErrSubmissionInterrupted)
			}
			return newSubmitDoneMsg(token, "", originalText, err)
		}
		return newSubmitDoneMsg(token, "", originalText, nil)
	}
}

func (m *uiModel) beginSubmitAttempt(text string, queuedID string, operationRef clientui.RuntimeOperationRef, origin activeSubmitOrigin) uint64 {
	if m == nil {
		return 0
	}
	m.submitToken++
	if m.submitToken == 0 {
		m.submitToken++
	}
	m.activeSubmit = activeSubmitState{token: m.submitToken, text: text, queuedID: queuedID, origin: origin, operationRef: operationRef, restoreOnInterrupt: true}
	return m.submitToken
}

func (m *uiModel) holdActiveSubmitInput() {
	if m == nil || m.activeSubmit.token == 0 ||
		m.activeSubmit.origin != activeSubmitOriginDirect ||
		strings.TrimSpace(m.mainEditor.Text()) != "" {
		return
	}
	m.inputController().restoreSubmittedTextIntoInput(m.activeSubmit.text)
	m.activeSubmit.heldInput = &activeSubmitHeldInput{draftToken: m.mainInputDraftToken}
}

func (m *uiModel) activeSubmitHoldsCurrentInput() bool {
	return m != nil &&
		m.activeSubmit.heldInput != nil &&
		m.activeSubmit.heldInput.draftToken == m.mainInputDraftToken
}

func (m *uiModel) releaseActiveSubmitInput() {
	if m == nil || m.activeSubmit.heldInput == nil {
		return
	}
	clearHeldInput := m.activeSubmitHoldsCurrentInput()
	m.activeSubmit.heldInput = nil
	if clearHeldInput {
		m.clearInput()
	}
}

type uiCompactionOrigin uint8

const (
	uiCompactionOriginNone uiCompactionOrigin = iota
	uiCompactionOriginManual
	uiCompactionOriginQueued
)

func (c uiInputController) startCompactionWithOrigin(args string, origin uiCompactionOrigin) tea.Cmd {
	m := c.model
	if m.hasPendingRuntimeOperationKind(clientui.RuntimeOperationKindCompact) {
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
	operationRef := newRuntimeOperationRef(clientui.RuntimeOperationKindCompact)
	m.addPendingRuntimeOperation(operationRef)
	return func() tea.Msg {
		if client == nil {
			return compactDoneMsg{err: errors.New("runtime engine is not configured")}
		}
		return compactDoneMsg{err: m.compactRuntimeInput(context.Background(), clientui.RuntimeCompactRequest{OperationRef: operationRef, Args: args})}
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
	if msg.phase == submitPhaseDraftPrepared {
		return c.handleSubmitDraftPrepared(msg)
	}
	submitOrigin := m.activeSubmit.origin
	m.observeRuntimeRequestResult(msg.err)
	restoreSubmittedText := msg.err != nil && (submitOrigin == activeSubmitOriginQueued || msg.token == 0 || m.shouldRestoreSubmittedTextOnSubmitError(msg.err))
	if msg.token != 0 && msg.err != nil && isRuntimeOperationInterrupted(msg.err) && m.activeSubmit.restoreOnInterrupt {
		restore, _ := m.shouldRestoreActiveSubmitAfterInterrupt()
		restoreSubmittedText = restore
	}
	activeQueuedID := m.activeSubmit.queuedID
	m.activeSubmit = activeSubmitState{}
	m.clearPendingRuntimeOperations(clientui.RuntimeOperationKindPreSubmitCompact)
	c.finishRuntimeOperationAffordance(false)
	if msg.token == 0 || !m.hasRuntimeClient() {
		_ = m.applyRuntimeActivityProjection(clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle})
	}
	m.discardQueuedInput(activeQueuedID)
	if msg.err != nil {
		if m.turnQueueHook != nil {
			m.turnQueueHook.OnTurnQueueAborted()
		}
		restoreInjectedCmd := tea.Cmd(nil)
		if submitOrigin != activeSubmitOriginQueued {
			restoreInjectedCmd = c.restorePendingInjectedIntoInput()
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
	if msg.queued.ID != "" {
		m.registerSteeredQueuedUserMessage(msg.queued)
	}
	if msg.silentFinal && m.turnQueueHook != nil {
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

func (c uiInputController) handleSubmitDraftPrepared(msg submitDoneMsg) (tea.Model, tea.Cmd) {
	m := c.model
	if msg.err != nil {
		submittedText := msg.submittedText
		if m.activeSubmit.heldInput != nil {
			submittedText = ""
		}
		return c.handleSubmitDone(newSubmitDoneMsg(
			msg.token,
			"",
			submittedText,
			serverapi.NewRuntimeCommandNotAcceptedError(fmt.Errorf("persist active submission draft: %w", msg.err)),
		))
	}
	if msg.draftToken != m.mainInputDraftToken {
		return m, c.prepareSubmitDraftCmd(msg.token, msg.submittedText, msg.dispatch)
	}
	m.releaseActiveSubmitInput()
	return m, c.dispatchPreparedSubmit(msg.token, msg.submittedText, msg.dispatch)
}

func (c uiInputController) dispatchPreparedSubmit(token uint64, text string, dispatch submitDispatch) tea.Cmd {
	m := c.model
	switch dispatch.kind {
	case submitDispatchUserTurn:
		return c.submitCmd(
			token,
			text,
			dispatch.input,
			m.activeSubmit.operationRef,
			dispatch.preSubmitCompactionOperationRef,
		)
	case submitDispatchUserShell:
		return c.submitUserShellCmd(token, text, dispatch.shellCommand, m.activeSubmit.operationRef)
	default:
		panic(fmt.Sprintf("invalid submit dispatch kind %d for token %d", dispatch.kind, token))
	}
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
	m.clearPendingRuntimeOperations(clientui.RuntimeOperationKindCompact, clientui.RuntimeOperationKindPreSubmitCompact)
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

func (m *uiModel) shouldRestoreSubmittedTextOnSubmitError(err error) bool {
	if m == nil || err == nil {
		return false
	}
	if isRuntimeOperationInterrupted(err) {
		return true
	}
	if errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		return true
	}
	if !m.hasRuntimeClient() {
		return true
	}
	ref := m.activeSubmit.operationRef
	if ref.Validate() != nil {
		return false
	}
	view := m.cachedRuntimeMainView()
	for _, record := range view.InputReconciliation.Operations {
		if record.Operation != ref {
			continue
		}
		switch record.State {
		case clientui.RuntimeInputReconciliationCanceledNotCommitted, clientui.RuntimeInputReconciliationFailedWithRestore:
			return true
		case clientui.RuntimeInputReconciliationCommitted,
			clientui.RuntimeInputReconciliationSubmitted,
			clientui.RuntimeInputReconciliationUnknown,
			clientui.RuntimeInputReconciliationEvicted:
			return false
		}
	}
	return false
}

func isRuntimeOperationInterrupted(err error) bool {
	return errors.Is(err, runtimeattach.ErrSubmissionInterrupted) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, serverapi.ErrRuntimeOperationCanceled)
}
