package app

import (
	"strings"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type runtimeSyncPolicyClass uint8

const (
	runtimeSyncPolicyClassAllowed runtimeSyncPolicyClass = iota + 1
	runtimeSyncPolicyClassRoutine
)

type runtimeMainViewRefreshRequest struct {
	cause    runtimeMainViewRefreshCause
	class    runtimeSyncPolicyClass
	priority int
}

type runtimeMainViewRefreshDecision struct {
	cmd         tea.Cmd
	started     bool
	deferred    bool
	busyPending bool
}

func (m *uiModel) startRuntimeMainViewRefreshRequest(request runtimeMainViewRefreshRequest) runtimeMainViewRefreshDecision {
	if m == nil || !m.hasRuntimeClient() {
		return runtimeMainViewRefreshDecision{}
	}
	request = normalizeRuntimeMainViewRefreshRequest(request)
	if m.runtimeMainViewBusy {
		m.mergePendingRuntimeMainViewRefresh(request)
		return runtimeMainViewRefreshDecision{busyPending: true}
	}
	m.runtimeMainViewToken++
	token := m.runtimeMainViewToken
	client := m.runtimeClient()
	m.runtimeMainViewBusy = true
	m.runtimeMainViewActiveRequest = request
	refs := m.pendingRuntimeOperationRefs()
	cmd := func() tea.Msg {
		var (
			view clientui.RuntimeMainView
			err  error
		)
		if requestClient, ok := client.(runtimeMainViewReconciliationClient); ok {
			view, err = requestClient.RefreshMainViewWithPendingRefs(refs)
		} else {
			view, err = client.RefreshMainView()
		}
		return runtimeMainViewRefreshedMsg{token: token, req: request, view: view, err: err}
	}
	return runtimeMainViewRefreshDecision{cmd: cmd, started: true}
}

func normalizeRuntimeMainViewRefreshRequest(req runtimeMainViewRefreshRequest) runtimeMainViewRefreshRequest {
	if req.cause == "" {
		req.cause = runtimeMainViewRefreshCauseManual
	}
	if req.class == 0 {
		req.class = runtimeSyncPolicyClassRoutine
	}
	if req.priority == 0 {
		req.priority = 10
	}
	return req
}

func runtimeMainViewRefreshRequestForCause(cause runtimeMainViewRefreshCause) runtimeMainViewRefreshRequest {
	priority := 10
	class := runtimeSyncPolicyClassRoutine
	switch cause {
	case runtimeMainViewRefreshCauseStartupUpdate:
		priority = 20
	case runtimeMainViewRefreshCauseWorktreeMutation:
		class = runtimeSyncPolicyClassAllowed
		priority = 50
	}
	return normalizeRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequest{cause: cause, class: class, priority: priority})
}

func (m *uiModel) mergePendingRuntimeMainViewRefresh(req runtimeMainViewRefreshRequest) {
	if m == nil {
		return
	}
	req = normalizeRuntimeMainViewRefreshRequest(req)
	if !m.runtimeMainViewPendingSet || req.priority >= m.runtimeMainViewPending.priority {
		m.runtimeMainViewPending = req
		m.runtimeMainViewPendingSet = true
	}
}

func (m *uiModel) drainPendingRuntimeMainViewRefresh() runtimeMainViewRefreshDecision {
	if m == nil || !m.runtimeMainViewPendingSet || m.runtimeMainViewBusy {
		return runtimeMainViewRefreshDecision{}
	}
	req := m.runtimeMainViewPending
	m.runtimeMainViewPending = runtimeMainViewRefreshRequest{}
	m.runtimeMainViewPendingSet = false
	return m.startRuntimeMainViewRefreshRequest(req)
}

func (m *uiModel) releaseDeferredRuntimeSyncs() tea.Cmd {
	return m.drainPendingRuntimeMainViewRefresh().cmd
}

func (m *uiModel) handleRuntimeMainViewRefreshed(msg runtimeMainViewRefreshedMsg) tea.Cmd {
	if m == nil || msg.token != m.runtimeMainViewToken {
		return nil
	}
	m.runtimeMainViewBusy = false
	req := msg.req
	if req == (runtimeMainViewRefreshRequest{}) {
		req = m.runtimeMainViewActiveRequest
	}
	req = normalizeRuntimeMainViewRefreshRequest(req)
	m.runtimeMainViewActiveRequest = runtimeMainViewRefreshRequest{}
	if msg.err != nil {
		m.observeRuntimeRequestResult(msg.err)
		return m.drainPendingRuntimeMainViewRefresh().cmd
	}
	m.observeRuntimeRequestResult(nil)
	applyCmd := m.applyRuntimeMainViewState(msg.view)
	noticeCmd := runtimeMainViewStartupUpdateNoticeCmd(req, msg.view)
	return sequenceCmds(applyCmd, m.runtimeAdapter().applyProjectedSessionMetadata(msg.view.Session), noticeCmd, m.drainPendingRuntimeMainViewRefresh().cmd)
}

func runtimeMainViewStartupUpdateNoticeCmd(req runtimeMainViewRefreshRequest, view clientui.RuntimeMainView) tea.Cmd {
	if req.cause != runtimeMainViewRefreshCauseStartupUpdate {
		return nil
	}
	status := view.Status.Update
	if !status.Available || strings.TrimSpace(status.LatestVersion) == "" {
		return nil
	}
	return func() tea.Msg {
		return startupUpdateNoticeMsg{version: status.LatestVersion}
	}
}

func (m *uiModel) flushQueuedInputsAfterHydration() tea.Cmd {
	if m == nil || !m.pendingQueuedDrainAfterHydration {
		return nil
	}
	m.pendingQueuedDrainAfterHydration = false
	m.queuedDrainReadyAfterHydration = false
	if len(m.queued) == 0 {
		m.inputController().notifyTurnQueueDrainedIfIdle()
		return nil
	}
	_, cmd := m.inputController().flushQueuedInputs(queueDrainAuto)
	m.inputController().notifyTurnQueueDrainedIfIdle()
	return cmd
}
