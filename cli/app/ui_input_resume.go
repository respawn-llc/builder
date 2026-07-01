package app

import (
	"context"
	"errors"

	"core/cli/app/internal/runtimeattach"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (c uiInputController) startQueuedInjectionSubmission() tea.Cmd {
	m := c.model
	if blocked, disconnectCmd := c.blockDisconnectedSubmission(true, ""); blocked {
		return disconnectCmd
	}
	if m.injectedQueueBlocksDrain() {
		return nil
	}
	return c.queuedRuntimeWorkCheckCmd()
}

func (c uiInputController) queuedRuntimeWorkCheckCmd() tea.Cmd {
	m := c.model
	if m == nil {
		return nil
	}
	client := m.runtimeClient()
	if client == nil {
		return nil
	}
	token := m.nextInjectedQueueToken()
	return func() tea.Msg {
		hasWork, err := client.HasQueuedUserWork()
		return queuedRuntimeWorkCheckDoneMsg{token: token, hasWork: hasWork, err: err}
	}
}

func (c uiInputController) handleQueuedRuntimeWorkCheckDone(msg queuedRuntimeWorkCheckDoneMsg) (tea.Model, tea.Cmd) {
	m := c.model
	if msg.token == 0 || msg.token != m.injectedQueueToken {
		return m, nil
	}
	compactionOrigin := m.queuedRuntimeWorkCheckCompactionOrigin
	m.queuedRuntimeWorkCheckCompactionOrigin = uiCompactionOriginNone
	m.observeRuntimeRequestResult(msg.err)
	if msg.err != nil {
		restoreCmd := c.restorePendingInjectedIntoInput()
		if errors.Is(msg.err, runtimeattach.ErrSubmissionInterrupted) || errors.Is(msg.err, context.Canceled) {
			m.activity = uiActivityInterrupted
			m.logf("step.interrupted")
			m.layout().syncViewport()
			return m, restoreCmd
		}
		detailErr := runtimeattach.FormatSubmissionError(msg.err)
		m.activity = uiActivityError
		appendCmd := m.appendLocalEntryWithNoticeID(operatorErrorFeedbackRole, detailErr, "")
		m.logf("queue_check.error err=%q", detailErr)
		m.layout().syncViewport()
		return m, tea.Batch(restoreCmd, appendCmd)
	}
	blocked := m.injectedQueueBlocksDrain() || m.isBusy() || m.isInputSubmitLocked()
	if !msg.hasWork {
		c.notifyUserCompactionCompleted(compactionOrigin, true)
		if blocked {
			return m, nil
		}
		return m, c.requestIdleRuntimeControlCommittedTranscriptSync(compactionOrigin)
	}
	if blocked {
		c.notifyUserCompactionCompleted(compactionOrigin, false)
		return m, nil
	}
	c.notifyUserCompactionCompleted(compactionOrigin, false)
	c.startBusyActivity(false)
	m.logf("step.resume_queued_injected pending_injected=%d", len(m.pendingInjected))
	m.layout().syncViewport()
	return m, tea.Batch(c.submitQueuedUserMessagesCmd(), c.model.reconcileSpinnerTicking(false))
}

func (c uiInputController) requestIdleRuntimeControlCommittedTranscriptSync(origin uiCompactionOrigin) tea.Cmd {
	m := c.model
	if m == nil || origin == uiCompactionOriginNone || !m.hasRuntimeClient() {
		return nil
	}
	return m.requestRuntimeCommittedConversationSync()
}

func (c uiInputController) submitQueuedUserMessagesCmd() tea.Cmd {
	m := c.model
	token := m.beginSubmitAttempt("", "")
	client := m.runtimeClient()
	return func() tea.Msg {
		if client == nil {
			return newSubmitDoneMsg(token, "", "", errors.New("runtime engine is not configured"))
		}
		msg, err := submitQueuedRuntimeUserMessages(context.Background(), client)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return newSubmitDoneMsg(token, "", "", runtimeattach.ErrSubmissionInterrupted)
			}
			return newSubmitDoneMsg(token, "", "", err)
		}
		return newSubmitDoneMsg(token, msg, "", nil)
	}
}

func submitQueuedRuntimeUserMessages(ctx context.Context, client clientui.RuntimeClient) (string, error) {
	if client == nil {
		return "", errors.New("runtime engine is not configured")
	}
	return client.SubmitQueuedUserMessages(ctx)
}
