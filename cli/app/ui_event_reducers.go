package app

import (
	"runtime/debug"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type uiDiagnosticsFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) diagnosticsReducer() uiDiagnosticsFeatureReducer {
	return uiDiagnosticsFeatureReducer{model: m}
}

func (r uiDiagnosticsFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg := msg.(type) {
	case runLoggerDiagnosticMsg:
		cmd := m.applyRunLoggerDiagnostic(msg.diagnostic)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	}
	return uiFeatureUpdateResult{}
}

type uiAskFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) askReducer() uiAskFeatureReducer {
	return uiAskFeatureReducer{model: m}
}

func (r uiAskFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg := msg.(type) {
	case askEventMsg:
		m.askController().acceptEvent(msg.event)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, waitAskEvent(m.askEvents))
	}
	return uiFeatureUpdateResult{}
}

type uiPathReferenceFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) pathReferenceReducer() uiPathReferenceFeatureReducer {
	return uiPathReferenceFeatureReducer{model: m}
}

func (r uiPathReferenceFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
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

type uiNoticeFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) noticeReducer() uiNoticeFeatureReducer {
	return uiNoticeFeatureReducer{model: m}
}

func (r uiNoticeFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg := msg.(type) {
	case clearTransientStatusMsg:
		if msg.token == m.transientStatusToken {
			return handledUIFeatureUpdate(m, m.batchWithNativeOngoingRepaint(m.advanceTransientStatusQueue()))
		}
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, nil)
	case startupUpdateNoticeMsg:
		if m.startupUpdateShown {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.startupUpdateShown = true
		cmd := m.sendTransientStatusWithNoticeID("update available: "+strings.TrimSpace(msg.version), uiStatusNoticeSuccess, updateNoticeDuration, uiStatusNoticeQueue, "")
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	}
	return uiFeatureUpdateResult{}
}

type uiInputAsyncFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) inputAsyncReducer() uiInputAsyncFeatureReducer {
	return uiInputAsyncFeatureReducer{model: m}
}

func (r uiInputAsyncFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
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
	case injectedQueueDiscardDoneMsg:
		next, cmd := m.inputController().handleInjectedQueueDiscardDone(msg)
		nextModel := next.(*uiModel)
		nextModel.layout().syncViewport()
		return handledUIFeatureUpdate(nextModel, cmd)
	case queuedRuntimeWorkCheckDoneMsg:
		next, cmd := m.inputController().handleQueuedRuntimeWorkCheckDone(msg)
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

type uiProcessFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) processReducer() uiProcessFeatureReducer {
	return uiProcessFeatureReducer{model: m}
}

func (r uiProcessFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
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

type uiClipboardFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) clipboardReducer() uiClipboardFeatureReducer {
	return uiClipboardFeatureReducer{model: m}
}

func (r uiClipboardFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
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
