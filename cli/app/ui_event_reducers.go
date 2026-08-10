package app

import (
	"runtime/debug"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) reduceAskMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case askEventMsg:
		cmd := m.askController().acceptEvent(msg.event)
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
	}
	return uiFeatureUpdateResult{}
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
	case goalRuntimeDoneMsg:
		cmd := m.applyGoalRuntimeDone(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case injectedQueueCreateDoneMsg:
		next, cmd := m.inputController().handleInjectedQueueCreateDone(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case injectedQueueDraftPersistedMsg:
		next, cmd := m.inputController().handleInjectedQueueDraftPersisted(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case injectedQueueDiscardDoneMsg:
		next, cmd := m.inputController().handleInjectedQueueDiscardDone(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case submitDraftPersistedMsg:
		next, cmd := m.inputController().handleSubmitDraftPersisted(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case draftRecoveryPersistedMsg:
		if msg.err == nil {
			return handledUIFeatureUpdate(m, nil)
		}
		m.logf("draft_recovery.persist_error err=%q", msg.err.Error())
		return handledUIFeatureUpdate(m, m.sendTransientStatusWithNoticeID(
			"Draft Recovery persistence failed: "+msg.err.Error(),
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		))
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
