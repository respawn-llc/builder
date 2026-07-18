package app

import (
	"strings"

	"core/cli/tui"
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

type runtimeMainViewCandidateClient interface {
	fetchMainViewWithPendingRefs([]clientui.RuntimeOperationRef) (clientui.RuntimeMainView, error)
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
	var metadataBaselineRevision *uint64
	if sessionClient, ok := client.(*sessionRuntimeClient); ok {
		revision := sessionClient.mainViewMetadataRevision()
		metadataBaselineRevision = &revision
	}
	cmd := func() tea.Msg {
		var (
			view clientui.RuntimeMainView
			err  error
		)
		if candidateClient, ok := client.(runtimeMainViewCandidateClient); ok {
			view, err = candidateClient.fetchMainViewWithPendingRefs(refs)
		} else if requestClient, ok := client.(runtimeMainViewReconciliationClient); ok {
			view, err = requestClient.RefreshMainViewWithPendingRefs(refs)
		} else {
			view, err = client.RefreshMainView()
		}
		return runtimeMainViewRefreshedMsg{
			token:                    token,
			req:                      request,
			metadataBaselineRevision: metadataBaselineRevision,
			view:                     view,
			err:                      err,
		}
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
	canonical := msg.view
	if client, ok := m.runtimeClient().(*sessionRuntimeClient); ok {
		canonical = client.mergeMainViewCandidate(
			msg.view,
			runtimeTupleIngressAuthoritativeSnapshot,
			msg.metadataBaselineRevision,
		).view
	}
	applyCmd := m.applyRuntimeMainViewState(canonical)
	return sequenceCmds(applyCmd, m.applyRuntimeSessionMetadata(canonical.Session), m.drainPendingRuntimeMainViewRefresh().cmd)
}

func (m *uiModel) applyRuntimeSessionMetadata(session clientui.RuntimeSessionView) tea.Cmd {
	if m == nil {
		return nil
	}
	previousSessionID := strings.TrimSpace(m.sessionID)
	nextSessionID := strings.TrimSpace(session.SessionID)
	if nextSessionID != "" {
		m.sessionID = nextSessionID
	}
	if strings.TrimSpace(session.SessionName) != "" {
		m.sessionName = strings.TrimSpace(session.SessionName)
	}
	m.conversationFreshness = session.ConversationFreshness
	if previousSessionID == "" || nextSessionID == "" || previousSessionID == nextSessionID {
		return nil
	}
	rollbackCmd := m.discardRollbackStateForSessionReplacement()
	cancelCmd := m.cancelPendingDetailTranscriptRequest()
	m.detailTranscript.reset()
	resetCmd := m.forwardToView(tui.ResetDetailTranscriptMsg{})
	loadCmd := m.loadDetailTranscriptPageCmd(m.detailTranscript.requestedPageForDetailEntry())
	return sequenceCmds(rollbackCmd, cancelCmd, resetCmd, loadCmd)
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
