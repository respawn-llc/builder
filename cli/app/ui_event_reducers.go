package app

import (
	"core/shared/clientui"
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type promptCtrlCContinuationDisposition uint8

const (
	promptCtrlCContinuationDiscard promptCtrlCContinuationDisposition = iota
	promptCtrlCContinuationWait
	promptCtrlCContinuationCancelPrompt
	promptCtrlCContinuationRoute
)

func (m *uiModel) reduceAskMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case askEventMsg:
		cmd := m.askController().acceptEvent(msg.event)
		cmd = tea.Batch(cmd, m.releasePendingPromptCtrlCContinuation())
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case questionRenderResultMsg:
		cmd, repaint := m.applyQuestionRenderResult(msg)
		m.layout().syncViewport()
		if repaint {
			cmd = tea.Batch(cmd, m.renderNativeOngoingSurface())
		}
		return handledUIFeatureUpdate(m, cmd)
	case promptAnswerDeliveryResultMsg:
		cmd := m.askController().applyDeliveryResult(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case promptCtrlCContinuationMsg:
		switch m.promptCtrlCContinuationDisposition(msg.key) {
		case promptCtrlCContinuationDiscard:
			m.clearPendingPromptCtrlCContinuation(msg.key)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		case promptCtrlCContinuationWait:
			key := msg.key
			m.ask.pendingCtrlCContinuation = &key
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		case promptCtrlCContinuationCancelPrompt:
			m.clearPendingPromptCtrlCContinuation(msg.key)
			next, cmd := m.askController().handleCtrlC()
			nextModel := next.(*uiModel)
			nextModel.layout().syncViewport()
			return handledUIFeatureUpdate(nextModel, cmd)
		case promptCtrlCContinuationRoute:
			m.clearPendingPromptCtrlCContinuation(msg.key)
		default:
			panic("unknown prompt Ctrl+C continuation disposition")
		}
		next, cmd := m.inputController().handleRuntimeCtrlC(nil)
		nextModel := next.(*uiModel)
		cmd = tea.Batch(cmd, nextModel.interruptedStatusNoticeCmd())
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) promptCtrlCContinuationDisposition(key transcriptPromptKey) promptCtrlCContinuationDisposition {
	if m == nil ||
		key.sessionID.IsZero() ||
		key.stepID.IsZero() ||
		strings.TrimSpace(m.sessionID) != key.sessionID.String() {
		return promptCtrlCContinuationDiscard
	}
	activity := m.runtimeActivityProjection
	if activity.ActiveStep != nil {
		if activity.ActiveStep.StepID != key.stepID {
			return promptCtrlCContinuationDiscard
		}
		if m.activePromptContinuesCtrlC(key) {
			return promptCtrlCContinuationCancelPrompt
		}
		if activity.State == clientui.RuntimeActivityAwaitingPrompt {
			return promptCtrlCContinuationWait
		}
		return promptCtrlCContinuationRoute
	}
	if activity.ActiveForControl() || m.hasLocalDispatchPending() {
		return promptCtrlCContinuationDiscard
	}
	return promptCtrlCContinuationRoute
}

func (m *uiModel) activePromptContinuesCtrlC(key transcriptPromptKey) bool {
	if m == nil || !m.ask.hasCurrent() {
		return false
	}
	activeKey, err := newTranscriptPromptKey(m.ask.current.prompt)
	if err != nil {
		return false
	}
	return activeKey.sessionID == key.sessionID &&
		activeKey.stepID == key.stepID &&
		activeKey.promptID != key.promptID
}

func (m *uiModel) clearPendingPromptCtrlCContinuation(key transcriptPromptKey) {
	if m == nil ||
		m.ask.pendingCtrlCContinuation == nil ||
		*m.ask.pendingCtrlCContinuation != key {
		return
	}
	m.ask.pendingCtrlCContinuation = nil
}

func (m *uiModel) releasePendingPromptCtrlCContinuation() tea.Cmd {
	if m == nil || m.ask.pendingCtrlCContinuation == nil {
		return nil
	}
	key := *m.ask.pendingCtrlCContinuation
	switch m.promptCtrlCContinuationDisposition(key) {
	case promptCtrlCContinuationDiscard:
		m.ask.pendingCtrlCContinuation = nil
		return nil
	case promptCtrlCContinuationWait:
		return nil
	case promptCtrlCContinuationCancelPrompt, promptCtrlCContinuationRoute:
		m.ask.pendingCtrlCContinuation = nil
		return promptCtrlCContinuationCmd(key)
	default:
		panic("unknown prompt Ctrl+C continuation disposition")
	}
}

func (m *uiModel) reducePathReferenceMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case uiPathReferenceCorpusReadyMsg:
		m.handlePathReferenceCorpusReady(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, waitPathReferenceSearchEvent(m.pathReferenceEvents))
	case uiPathReferenceCorpusFailedMsg:
		m.handlePathReferenceCorpusFailed(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, waitPathReferenceSearchEvent(m.pathReferenceEvents))
	case uiPathReferenceMatchResultMsg:
		m.handlePathReferenceMatchResult(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, waitPathReferenceSearchEvent(m.pathReferenceEvents))
	case uiPathReferenceLoadingDelayMsg:
		m.handlePathReferenceLoadingDelay(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, waitPathReferenceSearchEvent(m.pathReferenceEvents))
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) reduceNoticeMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case clearTransientStatusMsg:
		if msg.token == m.transientStatusToken {
			return handledUIFeatureUpdate(m, m.batchWithNativeOngoingRepaint(m.advanceTransientStatusQueue()))
		}
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, nil)
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) reduceInputAsyncMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case latestFinalAnswerDoneMsg:
		cmd := m.handleLatestFinalAnswerDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case latestFinalAnswerTimeoutMsg:
		cmd := m.handleLatestFinalAnswerTimeout(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case authSlashCommandRefreshedMsg:
		m.applyAuthSlashCommandRefreshed(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, nil)
	case promptHistoryPersistErrMsg:
		m.observeRuntimeRequestResult(msg.err)
		if msg.err == nil {
			return handledUIFeatureUpdate(m, nil)
		}
		m.logf("prompt_history.persist_error err=%q stack=%s", msg.err.Error(), debug.Stack())
		return handledUIFeatureUpdate(m, m.sendTransientStatusWithNoticeID("prompt history persistence failed: "+msg.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	case terminalSequenceWriteErrMsg:
		if msg.err == nil {
			return handledUIFeatureUpdate(m, nil)
		}
		m.logf("terminal.sequence_write_error err=%q stack=%s", msg.err.Error(), debug.Stack())
		return handledUIFeatureUpdate(m, m.handleOngoingSurfaceError(msg.err))
	case committedEntryPersistDoneMsg:
		m.observeRuntimeRequestResult(msg.err)
		if msg.err == nil {
			return handledUIFeatureUpdate(m, nil)
		}
		m.logf("committed_entry.persist_error notice_id=%q err=%q", msg.noticeID, msg.err.Error())
		return handledUIFeatureUpdate(m, m.sendTransientStatusWithNoticeID(msg.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	case promptCatalogRefreshDoneMsg:
		return handledUIFeatureUpdate(m, m.handlePromptCatalogRefreshDone(msg))
	case runtimeControlDoneMsg:
		cmd := m.applyRuntimeControlDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case chatSettingsDoneMsg:
		cmd := m.applyChatSettingsDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case goalRuntimeDoneMsg:
		cmd := m.applyGoalRuntimeDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case pendingWorkRefreshDoneMsg:
		cmd := m.applyPendingWorkRefreshDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, m.batchWithNativeOngoingRepaint(cmd))
	case injectedQueueCreateDoneMsg:
		next, cmd := m.inputController().handleInjectedQueueCreateDone(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case injectedQueueDiscardDoneMsg:
		next, cmd := m.inputController().handleInjectedQueueDiscardDone(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case submitDoneMsg:
		next, cmd := m.inputController().handleSubmitDone(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case compactDoneMsg:
		next, cmd := m.inputController().handleCompactDone(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case spinnerTickMsg:
		next, cmd := m.inputController().handleSpinnerTick(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, tea.Batch(cmd, nextModel.renderNativeOngoingSurface()))
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) reduceProcessMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case processListRefreshTickMsg:
		if !m.processList.open {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		refreshCmd := m.requestProcessListRefresh()
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(refreshCmd, tea.Tick(processListRefreshInterval, func(time.Time) tea.Msg { return processListRefreshTickMsg{} }), m.reconcileSpinnerTicking(false)))
	case processListRefreshDoneMsg:
		if msg.token != m.processList.refreshToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.processList.refreshInFlight = false
		m.processList.loading = false
		if msg.err != nil {
			m.processList.errorText = msg.err.Error()
		} else {
			m.applyProcessEntries(msg.entries)
		}
		var refreshCmd tea.Cmd
		if m.processList.refreshDirty && m.processList.open {
			m.processList.refreshDirty = false
			refreshCmd = m.requestProcessListRefresh()
		}
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(refreshCmd, m.reconcileSpinnerTicking(false)))
	case processActionDoneMsg:
		cmd := m.applyProcessActionDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case openProcessLogsDoneMsg:
		m.layout().syncViewport()
		if msg.err != nil {
			return handledUIFeatureUpdate(m, m.sendTransientStatusWithNoticeID(msg.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		return handledUIFeatureUpdate(m, nil)
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) reduceClipboardMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case clipboardPasteDoneMsg:
		cmd := m.handleClipboardPasteDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, m.batchWithNativeOngoingRepaint(cmd))
	case clipboardImageDiscardDoneMsg:
		cmd := m.handleClipboardImageDiscardDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, m.batchWithNativeOngoingRepaint(cmd))
	case clipboardTextCopyDoneMsg:
		cmd := m.handleClipboardTextCopyDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, m.batchWithNativeOngoingRepaint(cmd))
	}
	return uiFeatureUpdateResult{}
}
