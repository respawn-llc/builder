package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type preSubmitQueuePosition uint8

const (
	preSubmitQueueBack preSubmitQueuePosition = iota
	preSubmitQueueFront
)

func (c uiInputController) startSubmissionWithPreSubmitQueuePosition(text string, queuePosition preSubmitQueuePosition, queuedID string) tea.Cmd {
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
		return tea.Batch(c.submitUserShellCmd(text, command), m.reconcileSpinnerTicking(false))
	}
	return tea.Batch(c.submitCmd(text, queuedID), m.reconcileSpinnerTicking(false))
}

func (c uiInputController) startSubmissionWithPromptHistoryAndQueuePositionAndID(text string, queuePosition preSubmitQueuePosition, queuedID string) tea.Cmd {
	m := c.model
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, text); blocked {
		return disconnectCmd
	}
	if blocked, blockCmd := c.blockInjectedQueueSubmission(); blocked {
		return blockCmd
	}
	m.rememberPromptHistoryLocally(text)
	return c.startSubmissionWithPreSubmitQueuePosition(text, queuePosition, queuedID)
}

func (c uiInputController) submitCmd(text string, queuedID string) tea.Cmd {
	m := c.model
	operationRef := newRuntimeOperationRef(clientui.RuntimeOperationKindSubmit)
	preSubmitCompactionRef := newRuntimeOperationRef(clientui.RuntimeOperationKindPreSubmitCompact)
	m.addPendingRuntimeOperation(preSubmitCompactionRef)
	token := m.beginSubmitAttempt(text, queuedID, operationRef)
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", text, errors.New("runtime engine is not configured"))
		}
		submission, err := m.submitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
			OperationRef:                    operationRef,
			PreSubmitCompactionOperationRef: preSubmitCompactionRef,
			Text:                            text,
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

func (c uiInputController) submitUserShellCmd(originalText, command string) tea.Cmd {
	m := c.model
	operationRef := newRuntimeOperationRef(clientui.RuntimeOperationKindUserShell)
	token := m.beginSubmitAttempt(originalText, "", operationRef)
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", originalText, errors.New("runtime engine is not configured"))
		}
		err := m.submitRuntimeShell(context.Background(), clientui.RuntimeShellRequest{OperationRef: operationRef, Command: command})
		if err != nil {
			if isRuntimeOperationInterrupted(err) {
				return newSubmitDoneMsg(token, "", originalText, runtimeattach.ErrSubmissionInterrupted)
			}
			return newSubmitDoneMsg(token, "", originalText, err)
		}
		return newSubmitDoneMsg(token, "", originalText, nil)
	}
}

func (m *uiModel) beginSubmitAttempt(text string, queuedID string, operationRef clientui.RuntimeOperationRef) uint64 {
	if m == nil {
		return 0
	}
	m.submitToken++
	if m.submitToken == 0 {
		m.submitToken++
	}
	m.activeSubmit = activeSubmitState{token: m.submitToken, text: text, queuedID: queuedID, operationRef: operationRef, restoreOnInterrupt: true}
	return m.submitToken
}

func (m *uiModel) markActiveSubmitFlushed(evt clientui.Event) {
	if m == nil || m.activeSubmit.token == 0 {
		return
	}
	switch evt.Kind {
	case clientui.EventRunStateChanged:
		if evt.RunState == nil || !evt.RunState.Lifecycle.IsRunning() || strings.TrimSpace(m.activeSubmit.stepID) != "" {
			return
		}
		m.activeSubmit.stepID = strings.TrimSpace(evt.StepID)
	case clientui.EventUserMessageFlushed:
		if strings.TrimSpace(m.activeSubmit.stepID) != "" && strings.TrimSpace(evt.StepID) != strings.TrimSpace(m.activeSubmit.stepID) {
			return
		}
		if strings.TrimSpace(evt.UserMessage) != "" && strings.TrimSpace(evt.UserMessage) != strings.TrimSpace(m.activeSubmit.text) {
			return
		}
		m.activeSubmit.flushed = true
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
	if m.turnQueueHook == nil || m.blocksRuntimeInput() || len(m.queued) > 0 || m.ask.hasCurrent() {
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
	m.observeRuntimeRequestResult(msg.err)
	restoreSubmittedText := msg.err != nil && (msg.token == 0 || m.shouldRestoreSubmittedTextOnSubmitError(msg.err))
	if msg.token != 0 && msg.err != nil && isRuntimeOperationInterrupted(msg.err) && m.activeSubmit.restoreOnInterrupt {
		restore, _ := m.shouldRestoreActiveSubmitAfterInterrupt()
		restoreSubmittedText = restore
	}
	activeQueuedID := m.activeSubmit.queuedID
	m.activeSubmit = activeSubmitState{}
	m.clearPendingRuntimeOperations(clientui.RuntimeOperationKindPreSubmitCompact)
	c.finishRuntimeOperationAffordance(false)
	if msg.token == 0 || !m.hasRuntimeClient() {
		_ = m.applyRuntimeActivityProjection(clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{}))
	}
	m.discardQueuedInput(activeQueuedID)
	if msg.err != nil {
		if m.turnQueueHook != nil {
			m.turnQueueHook.OnTurnQueueAborted()
		}
		restoreInjectedCmd := c.restorePendingInjectedIntoInput()
		if restoreSubmittedText {
			c.restoreSubmittedTextIntoInput(msg.submittedText)
		}
		c.restoreQueuedMessagesIntoInput()
		if isRuntimeOperationInterrupted(msg.err) {
			m.activity = uiActivityInterrupted
			m.logf("step.interrupted")
			m.layout().syncViewport()
			return m, restoreInjectedCmd
		}
		detailErr := runtimeattach.FormatSubmissionError(msg.err)
		m.activity = uiActivityError
		m.logf("step.error err=%q", detailErr)
		m.layout().syncViewport()
		statusCmd := m.sendTransientStatusWithNoticeID(detailErr, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
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
		if m.hasRuntimeClient() && c.queuedDrainRequiresHydration() {
			m.pendingQueuedDrainAfterHydration = true
			m.queuedDrainReadyAfterHydration = false
			m.layout().syncViewport()
			return m, m.requestRuntimeQueuedDrainAfterHydration()
		}
		next, drainCmd := c.flushQueuedInputs(queueDrainAuto)
		c.notifyTurnQueueDrainedIfIdle()
		return next, drainCmd
	}
	c.notifyTurnQueueDrainedIfIdle()
	m.layout().syncViewport()
	return m, nil
}

func (c uiInputController) queuedDrainRequiresHydration() bool {
	m := c.model
	if m == nil || !m.hasRuntimeClient() {
		return false
	}
	if len(m.queued) == 0 {
		return false
	}
	if m.commandRegistry == nil {
		return true
	}
	for _, item := range m.queued {
		trimmed := strings.TrimSpace(item.Text)
		if trimmed == "" {
			continue
		}
		commandResult := m.commandRegistry.Execute(trimmed)
		if commandResult.Handled && !commandResult.SubmitUser {
			continue
		}
		return true
	}
	return false
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
			return m, restoreInjectedCmd
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
	if m.injectedQueueBlocksDrain() {
		c.notifyUserCompactionCompleted(compactionOrigin, false)
		m.queuedRuntimeWorkCheckCompactionOrigin = compactionOrigin
		m.layout().syncViewport()
		return m, nil
	}
	if !m.hasRuntimeClient() {
		c.notifyUserCompactionCompleted(compactionOrigin, !m.pendingQueuedDrainAfterHydration)
		m.layout().syncViewport()
		return m, nil
	}
	m.queuedRuntimeWorkCheckCompactionOrigin = compactionOrigin
	m.layout().syncViewport()
	return m, c.startQueuedInjectionSubmission()
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
	if !m.hasRuntimeClient() {
		return true
	}
	ref := m.activeSubmit.operationRef
	if ref.Validate() != nil {
		return false
	}
	view := m.cachedRuntimeMainView()
	for _, record := range view.InputReconciliation.Operations {
		if record.OperationRef != ref {
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
