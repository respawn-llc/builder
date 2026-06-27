package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/cli/app/internal/runtimeattach"
	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type preSubmitQueuePosition uint8

const (
	preSubmitQueueBack preSubmitQueuePosition = iota
	preSubmitQueueFront
)

func (c uiInputController) startSubmissionWithPreSubmitQueuePosition(text string, queuePosition preSubmitQueuePosition, queuedID string, promptHistoryRecorded bool) tea.Cmd {
	m := c.model
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, text); blocked {
		return disconnectCmd
	}
	if blocked, blockCmd := c.blockInjectedQueueSubmission(); blocked {
		return blockCmd
	}
	c.startBusyActivity(false)
	command, isUserShell := parseUserShellCommand(text)
	if isUserShell {
		m.logf("step.user_shell.start command_chars=%d", len(command))
	} else {
		m.logf("step.start user_chars=%d", len(text))
	}
	if !m.hasRuntimeClient() {
		if isUserShell {
			m.forwardToView(tui.AppendTranscriptMsg{Role: "tool_call", Text: command})
		} else {
			m.conversationFreshness = clientui.ConversationFreshnessEstablished
			m.forwardToView(tui.AppendTranscriptMsg{Role: "user", Text: text})
		}
	}
	m.layout().syncViewport()
	if isUserShell {
		return tea.Batch(c.submitUserShellCmd(text, command), m.reconcileSpinnerTicking(false))
	}
	if m.hasRuntimeClient() {
		return tea.Batch(c.submitCmd(text, queuedID, promptHistoryRecorded), m.reconcileSpinnerTicking(false))
	}
	return tea.Batch(c.submitCmd(text, queuedID, promptHistoryRecorded), m.reconcileSpinnerTicking(false))
}

func (c uiInputController) startSubmissionWithPromptHistoryAndQueuePositionAndID(text string, queuePosition preSubmitQueuePosition, queuedID string) tea.Cmd {
	m := c.model
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, text); blocked {
		return disconnectCmd
	}
	_, isUserShell := parseUserShellCommand(text)
	if m.hasRuntimeClient() && !isUserShell {
		m.rememberPromptHistoryLocally(text)
		return c.startSubmissionWithPreSubmitQueuePosition(text, queuePosition, queuedID, false)
	}
	return sequenceCmds(m.recordPromptHistory(text), c.startSubmissionWithPreSubmitQueuePosition(text, queuePosition, queuedID, false))
}

func (c uiInputController) submitCmd(text string, queuedID string, promptHistoryRecorded bool) tea.Cmd {
	m := c.model
	token := m.beginSubmitAttempt(text, queuedID)
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", text, errors.New("runtime engine is not configured"))
		}
		submission, err := m.submitRuntimeUserMessage(context.Background(), text, promptHistoryRecorded)
		if err != nil {
			if errors.Is(err, context.Canceled) {
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
	token := m.beginSubmitAttempt(originalText, "")
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", originalText, errors.New("runtime engine is not configured"))
		}
		err := client.SubmitUserShellCommand(context.Background(), command)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return newSubmitDoneMsg(token, "", originalText, runtimeattach.ErrSubmissionInterrupted)
			}
			return newSubmitDoneMsg(token, "", originalText, err)
		}
		return newSubmitDoneMsg(token, "", originalText, nil)
	}
}

func (m *uiModel) beginSubmitAttempt(text string, queuedID string) uint64 {
	if m == nil {
		return 0
	}
	m.submitToken++
	if m.submitToken == 0 {
		m.submitToken++
	}
	m.activeSubmit = activeSubmitState{token: m.submitToken, text: text, queuedID: queuedID, restoreOnInterrupt: true}
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
		m.markActiveSubmitUserMessageFlushed(evt)
	}
}

func (m *uiModel) markActiveSubmitUserMessageFlushed(evt clientui.Event) {
	if m == nil || m.activeSubmit.token == 0 {
		return
	}
	active := strings.TrimSpace(m.activeSubmit.text)
	if active == "" {
		return
	}
	if activeStepID := strings.TrimSpace(m.activeSubmit.stepID); activeStepID != "" || strings.TrimSpace(evt.StepID) != "" {
		if activeStepID == "" || strings.TrimSpace(evt.StepID) != activeStepID {
			return
		}
	}
	if strings.TrimSpace(evt.UserMessage) == active {
		m.activeSubmit.flushed = true
		return
	}
	for _, message := range evt.UserMessageBatch {
		if strings.TrimSpace(message) == active {
			m.activeSubmit.flushed = true
			return
		}
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
	c.startBusyActivity(true)
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
		return compactDoneMsg{err: client.CompactContext(context.Background(), args)}
	}
}

func (c uiInputController) startBusyActivity(compacting bool) {
	m := c.model
	m.clearReviewerState()
	m.setBusy(true)
	m.activity = uiActivityRunning
	m.sawAssistantDelta = false
	if compacting {
		m.setCompacting(true)
	}
}

func (c uiInputController) finishBusyActivity(compacting bool) {
	m := c.model
	m.setBusy(false)
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
	if m.turnQueueHook == nil || m.isBusy() || len(m.queued) > 0 || m.ask.hasCurrent() {
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
	restoreSubmittedText := true
	if msg.token != 0 && m.activeSubmit.flushed {
		restoreSubmittedText = false
	}
	activeQueuedID := m.activeSubmit.queuedID
	m.activeSubmit = activeSubmitState{}
	c.finishBusyActivity(false)
	m.discardQueuedInput(activeQueuedID)
	if msg.err != nil {
		if m.turnQueueHook != nil {
			m.turnQueueHook.OnTurnQueueAborted()
		}
		unlockCmd := c.releaseLockedInjectedInput(true)
		restoreInjectedCmd := c.restorePendingInjectedIntoInput()
		if restoreSubmittedText {
			c.restoreSubmittedTextIntoInput(msg.submittedText)
		}
		c.restoreQueuedMessagesIntoInput()
		if errors.Is(msg.err, runtimeattach.ErrSubmissionInterrupted) || errors.Is(msg.err, context.Canceled) {
			m.activity = uiActivityInterrupted
			m.logf("step.interrupted")
			m.layout().syncViewport()
			return m, batchCmds(unlockCmd, restoreInjectedCmd)
		}
		detailErr := runtimeattach.FormatSubmissionError(msg.err)
		m.activity = uiActivityError
		appendCmd := m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, detailErr, "")
		m.logf("step.error err=%q", detailErr)
		m.layout().syncViewport()
		return m, tea.Batch(unlockCmd, restoreInjectedCmd, appendCmd)
	}

	m.activity = uiActivityIdle
	if msg.queued.ID != "" {
		m.registerSteeredQueuedUserMessage(msg.queued)
	}
	if msg.silentFinal && m.turnQueueHook != nil {
		m.turnQueueHook.OnTurnQueueAborted()
	}
	if !m.hasRuntimeClient() && !msg.silentFinal {
		if !m.sawAssistantDelta && msg.message != "" {
			m.forwardToView(tui.StreamAssistantMsg{Delta: msg.message})
		}
		m.forwardToView(tui.CommitAssistantMsg{})
	}
	m.conversationFreshness = clientui.ConversationFreshnessEstablished
	m.localConversationTurn = true
	m.logf("step.done assistant_chars=%d", len(msg.message))
	m.sawAssistantDelta = false
	if len(m.queued) > 0 {
		if m.hasRuntimeClient() && c.queuedDrainRequiresHydration() {
			m.pendingQueuedDrainAfterHydration = true
			m.queuedDrainReadyAfterHydration = false
			m.layout().syncViewport()
			return m, m.requestRuntimeQueuedDrainTranscriptSync()
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
	compactionOrigin := m.compactionOrigin
	m.compactionOrigin = uiCompactionOriginNone
	m.observeRuntimeRequestResult(msg.err)
	c.finishBusyActivity(true)
	releaseCmd := c.releaseLockedInjectedInput(true)
	if msg.err != nil {
		restoreInjectedCmd := c.restorePendingInjectedIntoInput()
		c.restoreQueuedMessagesIntoInput()
		if errors.Is(msg.err, runtimeattach.ErrSubmissionInterrupted) || errors.Is(msg.err, context.Canceled) {
			m.activity = uiActivityInterrupted
			m.logf("step.interrupted")
			m.layout().syncViewport()
			return m, tea.Batch(releaseCmd, restoreInjectedCmd)
		}
		detailErr := runtimeattach.FormatSubmissionError(msg.err)
		m.activity = uiActivityError
		appendCmd := m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, detailErr, "")
		m.logf("compaction.error err=%q", detailErr)
		m.layout().syncViewport()
		return m, tea.Batch(releaseCmd, restoreInjectedCmd, appendCmd)
	}

	m.activity = uiActivityIdle
	m.logf("compaction.done")
	if len(m.queued) > 0 {
		c.notifyUserCompactionCompleted(compactionOrigin, false)
		next, cmd := c.flushQueuedInputs(queueDrainAuto)
		c.notifyTurnQueueDrainedIfIdle()
		return next, tea.Batch(releaseCmd, cmd)
	}
	if m.injectedQueueBlocksDrain() {
		c.notifyUserCompactionCompleted(compactionOrigin, false)
		m.queuedRuntimeWorkCheckCompactionOrigin = compactionOrigin
		m.layout().syncViewport()
		return m, releaseCmd
	}
	if !m.hasRuntimeClient() {
		c.notifyUserCompactionCompleted(compactionOrigin, !m.pendingQueuedDrainAfterHydration)
		m.layout().syncViewport()
		return m, releaseCmd
	}
	m.queuedRuntimeWorkCheckCompactionOrigin = compactionOrigin
	m.layout().syncViewport()
	return m, tea.Batch(releaseCmd, c.startQueuedInjectionSubmission())
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
