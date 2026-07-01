package app

import (
	"strconv"
	"strings"
	"time"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcriptdiag"

	tea "github.com/charmbracelet/bubbletea"
)

var uiRuntimeHydrationRetryDelay = 750 * time.Millisecond

type runtimeSyncPolicyClass uint8

const (
	runtimeSyncPolicyClassRoutine runtimeSyncPolicyClass = iota + 1
	runtimeSyncPolicyClassAllowed
)

type runtimeTranscriptSyncRequest struct {
	page               clientui.TranscriptPageRequest
	allowDuplicateSkip bool
	syncCause          runtimeTranscriptSyncCause
	recoveryCause      clientui.TranscriptRecoveryCause
	class              runtimeSyncPolicyClass
	priority           int
}

type runtimeTranscriptSyncDecision struct {
	cmd             tea.Cmd
	started         bool
	deferred        bool
	busyPending     bool
	awaitsHydration bool
}

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
	if !m.hasRuntimeClient() {
		return runtimeMainViewRefreshDecision{}
	}
	request = normalizeRuntimeMainViewRefreshRequest(request)
	if m.shouldDeferRuntimeMainViewRefresh(request) {
		m.mergePendingRuntimeMainViewRefresh(request)
		m.logf("ui.runtime.main_view.defer cause=%s streaming=%t process_overlay=%t", request.cause, m.runtimeSyncBlockedByStreaming(), m.runtimeSyncBlockedByProcessOverlay())
		return runtimeMainViewRefreshDecision{deferred: true}
	}
	if m.runtimeMainViewBusy {
		m.mergePendingRuntimeMainViewRefresh(request)
		m.logf("ui.runtime.main_view.busy_pending cause=%s", request.cause)
		return runtimeMainViewRefreshDecision{busyPending: true}
	}
	m.runtimeMainViewToken++
	token := m.runtimeMainViewToken
	client := m.runtimeClient()
	m.runtimeMainViewBusy = true
	m.runtimeMainViewActiveRequest = request
	m.logf("ui.runtime.main_view.start token=%d cause=%s", token, request.cause)
	cmd := func() tea.Msg {
		view, err := client.RefreshMainView()
		return runtimeMainViewRefreshedMsg{token: token, req: request, view: view, err: err}
	}
	return runtimeMainViewRefreshDecision{cmd: cmd, started: true}
}

func (m *uiModel) requestRuntimeBootstrapTranscriptSync() tea.Cmd {
	if !m.hasRuntimeClient() {
		return nil
	}
	return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), false, runtimeTranscriptSyncCauseBootstrap, clientui.TranscriptRecoveryCauseNone)).cmd
}

func (m *uiModel) requestRuntimeCommittedConversationSync() tea.Cmd {
	if !m.hasRuntimeClient() {
		return nil
	}
	return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), false, runtimeTranscriptSyncCauseCommittedConversation, clientui.TranscriptRecoveryCauseNone)).cmd
}

func (m *uiModel) requestRuntimeCommittedGapSync() tea.Cmd {
	if !m.hasRuntimeClient() {
		return nil
	}
	return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), false, runtimeTranscriptSyncCauseCommittedGap, clientui.TranscriptRecoveryCauseNone)).cmd
}

func (m *uiModel) requestRuntimeCommittedGapRecentTailSync() tea.Cmd {
	if !m.hasRuntimeClient() {
		return nil
	}
	return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(clientui.TranscriptPageRequest{}, false, runtimeTranscriptSyncCauseCommittedGap, clientui.TranscriptRecoveryCauseNone)).cmd
}

func (m *uiModel) requestRuntimeQueuedDrainTranscriptSync() tea.Cmd {
	if !m.hasRuntimeClient() {
		return nil
	}
	return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), false, runtimeTranscriptSyncCauseQueuedDrain, clientui.TranscriptRecoveryCauseNone)).cmd
}

func (m *uiModel) requestRuntimeTranscriptSyncForContinuityLoss(cause clientui.TranscriptRecoveryCause) tea.Cmd {
	if !m.hasRuntimeClient() {
		return nil
	}
	return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), false, runtimeTranscriptSyncCauseContinuityRecovery, cause)).cmd
}

func (m *uiModel) startRuntimeTranscriptSyncRequest(syncRequest runtimeTranscriptSyncRequest) runtimeTranscriptSyncDecision {
	syncRequest = m.normalizeRuntimeTranscriptSyncRequest(syncRequest)
	if !m.hasRuntimeClient() {
		return runtimeTranscriptSyncDecision{}
	}
	if syncRequest.allowDuplicateSkip && m.shouldSkipTranscriptPageRequest(syncRequest.page) {
		request := syncRequest.page
		m.logf("ui.runtime.transcript.skip_duplicate mode=%s cursor=%d sync_cause=%s", m.view.Mode(), request.Cursor, syncRequest.syncCause)
		return runtimeTranscriptSyncDecision{}
	}
	if m.shouldDeferRuntimeTranscriptSync(syncRequest) {
		m.mergePendingRuntimeTranscriptSync(syncRequest)
		m.logRuntimeTranscriptPolicyDecision("defer", syncRequest)
		return runtimeTranscriptSyncDecision{deferred: true}
	}
	if m.runtimeTranscriptBusy {
		m.mergePendingRuntimeTranscriptSync(syncRequest)
		m.logRuntimeTranscriptPolicyDecision("busy_pending", syncRequest)
		return runtimeTranscriptSyncDecision{busyPending: true, awaitsHydration: true}
	}
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptActiveRequest = syncRequest
	m.runtimeTranscriptToken++
	token := m.runtimeTranscriptToken
	client := m.runtimeClient()
	if client == nil {
		m.runtimeTranscriptBusy = false
		m.runtimeTranscriptActiveRequest = runtimeTranscriptSyncRequest{}
		return runtimeTranscriptSyncDecision{}
	}
	syncCause := syncRequest.syncCause
	recoveryCause := syncRequest.recoveryCause
	fetchRequest := syncRequest.page
	m.logRuntimeTranscriptPolicyDecision("start", syncRequest)
	m.logf("ui.runtime.transcript.start token=%d sync_cause=%s", token, syncCause)
	fields := map[string]string{
		"session_id":            m.sessionID,
		"mode":                  string(m.view.Mode()),
		"path":                  "hydrate",
		"token":                 strconv.FormatUint(token, 10),
		"sync_cause":            string(syncCause),
		"recovery_cause":        string(recoveryCause),
		"current_revision":      strconv.FormatInt(m.transcriptRevision, 10),
		"transcript_live_dirty": strconv.FormatBool(m.transcriptLiveDirty),
		"reasoning_live_dirty":  strconv.FormatBool(m.reasoningLiveDirty),
	}
	for key, value := range transcriptdiag.RequestFields(fetchRequest) {
		fields[key] = value
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.hydrate_start", fields))
	cmd := func() tea.Msg {
		var (
			transcript clientui.TranscriptPage
			err        error
		)
		if syncRequest.allowDuplicateSkip {
			transcript, err = client.LoadTranscriptPage(fetchRequest)
		} else {
			transcript, err = client.RefreshTranscriptPage(fetchRequest)
		}
		return runtimeTranscriptRefreshedMsg{token: token, req: fetchRequest, syncRequest: syncRequest, syncCause: syncCause, transcript: transcript, recoveryCause: recoveryCause, err: err}
	}
	return runtimeTranscriptSyncDecision{cmd: cmd, started: true, awaitsHydration: true}
}

func runtimeTranscriptSyncRequestForPage(request clientui.TranscriptPageRequest, allowDuplicateSkip bool, syncCause runtimeTranscriptSyncCause, recoveryCause clientui.TranscriptRecoveryCause) runtimeTranscriptSyncRequest {
	req := runtimeTranscriptSyncRequest{
		page:               request,
		allowDuplicateSkip: allowDuplicateSkip,
		syncCause:          syncCause,
		recoveryCause:      recoveryCause,
		class:              runtimeSyncPolicyClassRoutine,
		priority:           10,
	}
	switch syncCause {
	case runtimeTranscriptSyncCauseContinuityRecovery:
		req.class = runtimeSyncPolicyClassAllowed
		req.priority = 100
	case runtimeTranscriptSyncCauseCommittedGap:
		req.class = runtimeSyncPolicyClassAllowed
		req.priority = 90
	case runtimeTranscriptSyncCauseQueuedDrain:
		req.class = runtimeSyncPolicyClassAllowed
		req.priority = 80
	case runtimeTranscriptSyncCauseManualTranscriptRefresh:
		req.class = runtimeSyncPolicyClassAllowed
		req.priority = 70
	case runtimeTranscriptSyncCauseBootstrap:
		req.class = runtimeSyncPolicyClassAllowed
		req.priority = 60
	case runtimeTranscriptSyncCauseCommittedConversation, runtimeTranscriptSyncCauseDirtyFollowUp:
		req.class = runtimeSyncPolicyClassRoutine
		req.priority = 10
	default:
		if recoveryCause != clientui.TranscriptRecoveryCauseNone {
			req.class = runtimeSyncPolicyClassAllowed
			req.priority = 100
		}
	}
	if recoveryCause != clientui.TranscriptRecoveryCauseNone {
		req.class = runtimeSyncPolicyClassAllowed
		req.priority = max(req.priority, 100)
	}
	return req
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
	return normalizeRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequest{
		cause:    cause,
		class:    class,
		priority: priority,
	})
}

func (m *uiModel) normalizeRuntimeTranscriptSyncRequest(req runtimeTranscriptSyncRequest) runtimeTranscriptSyncRequest {
	if req.syncCause == "" {
		req.syncCause = runtimeTranscriptSyncCauseCommittedConversation
	}
	if req.class == 0 || req.priority == 0 {
		req = runtimeTranscriptSyncRequestForPage(req.page, req.allowDuplicateSkip, req.syncCause, req.recoveryCause)
	}
	return req
}

func (m *uiModel) runtimeSyncBlockedByProcessOverlay() bool {
	return m != nil && m.processList.open
}

func (m *uiModel) runtimeSyncBlockedByStreaming() bool {
	if m == nil {
		return false
	}
	return strings.TrimSpace(m.view.OngoingStreamingText()) != "" ||
		m.sawAssistantDelta ||
		m.isBusy() ||
		m.isCompacting() ||
		m.isReviewerRunning()
}

func (m *uiModel) shouldDeferRuntimeTranscriptSync(req runtimeTranscriptSyncRequest) bool {
	if req.class != runtimeSyncPolicyClassRoutine {
		return false
	}
	return m.runtimeSyncBlockedByProcessOverlay() || m.runtimeSyncBlockedByStreaming()
}

func (m *uiModel) shouldDeferRuntimeMainViewRefresh(req runtimeMainViewRefreshRequest) bool {
	if req.class != runtimeSyncPolicyClassRoutine {
		return false
	}
	return m.runtimeSyncBlockedByProcessOverlay() || m.runtimeSyncBlockedByStreaming()
}

func (m *uiModel) mergePendingRuntimeTranscriptSync(req runtimeTranscriptSyncRequest) {
	if m == nil {
		return
	}
	req = m.normalizeRuntimeTranscriptSyncRequest(req)
	if !m.runtimeTranscriptPendingSet || shouldReplacePendingRuntimeTranscriptSync(m.runtimeTranscriptPending, req) {
		m.runtimeTranscriptPending = req
		m.runtimeTranscriptPendingSet = true
	}
}

func shouldReplacePendingRuntimeTranscriptSync(current, next runtimeTranscriptSyncRequest) bool {
	if next.priority != current.priority {
		return next.priority > current.priority
	}
	nextRecentTail := next.page.Cursor <= 0 && next.page.NewerCursor <= 0
	currentRecentTail := current.page.Cursor <= 0 && current.page.NewerCursor <= 0
	if nextRecentTail != currentRecentTail {
		return nextRecentTail
	}
	if next.page.NewerCursor > 0 || current.page.NewerCursor > 0 {
		return next.page.NewerCursor >= current.page.NewerCursor
	}
	return next.page.Cursor >= current.page.Cursor
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

func (m *uiModel) drainPendingRuntimeTranscriptSync() runtimeTranscriptSyncDecision {
	if m == nil || !m.runtimeTranscriptPendingSet || m.runtimeTranscriptBusy {
		return runtimeTranscriptSyncDecision{}
	}
	req := m.runtimeTranscriptPending
	m.runtimeTranscriptPending = runtimeTranscriptSyncRequest{}
	m.runtimeTranscriptPendingSet = false
	decision := m.startRuntimeTranscriptSyncRequest(req)
	if decision.deferred || decision.busyPending {
		return decision
	}
	m.logRuntimeTranscriptPolicyDecision("release", req)
	return decision
}

func (m *uiModel) drainPendingRuntimeMainViewRefresh() runtimeMainViewRefreshDecision {
	if m == nil || !m.runtimeMainViewPendingSet || m.runtimeMainViewBusy {
		return runtimeMainViewRefreshDecision{}
	}
	req := m.runtimeMainViewPending
	m.runtimeMainViewPending = runtimeMainViewRefreshRequest{}
	m.runtimeMainViewPendingSet = false
	decision := m.startRuntimeMainViewRefreshRequest(req)
	if decision.deferred || decision.busyPending {
		return decision
	}
	m.logf("ui.runtime.main_view.release cause=%s", req.cause)
	return decision
}

func (m *uiModel) releaseDeferredRuntimeSyncs() tea.Cmd {
	transcript := m.drainPendingRuntimeTranscriptSync()
	mainView := m.drainPendingRuntimeMainViewRefresh()
	return tea.Batch(transcript.cmd, mainView.cmd)
}

func (m *uiModel) logRuntimeTranscriptPolicyDecision(outcome string, req runtimeTranscriptSyncRequest) {
	if m == nil {
		return
	}
	m.logf(
		"ui.runtime.transcript.policy outcome=%s sync_cause=%s recovery_cause=%s priority=%d streaming=%t process_overlay=%t cursor=%d",
		outcome,
		req.syncCause,
		req.recoveryCause,
		req.priority,
		m.runtimeSyncBlockedByStreaming(),
		m.runtimeSyncBlockedByProcessOverlay(),
		req.page.Cursor,
	)
}

func (m *uiModel) shouldSkipTranscriptPageRequest(req clientui.TranscriptPageRequest) bool {
	if m.runtimeTranscriptPendingSet || m.transcriptLiveDirty || m.reasoningLiveDirty {
		return false
	}
	if m.view.Mode() != tui.ModeDetail {
		return false
	}
	if !m.detailTranscript.loaded {
		return false
	}
	return pageRequestEqual(m.detailTranscript.lastRequest, req)
}

func (m *uiModel) scheduleRuntimeTranscriptRetry(syncCause runtimeTranscriptSyncCause, cause clientui.TranscriptRecoveryCause) tea.Cmd {
	if !m.hasRuntimeClient() {
		return nil
	}
	return m.scheduleRuntimeTranscriptRetryForRequest(runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), false, syncCause, cause))
}

func (m *uiModel) scheduleRuntimeTranscriptRetryForRequest(req runtimeTranscriptSyncRequest) tea.Cmd {
	if !m.hasRuntimeClient() {
		return nil
	}
	req = m.normalizeRuntimeTranscriptSyncRequest(req)
	m.runtimeTranscriptRetry++
	token := m.runtimeTranscriptRetry
	m.logf("ui.runtime.transcript.retry_scheduled token=%d sync_cause=%s recovery_cause=%s delay=%s", token, req.syncCause, req.recoveryCause, uiRuntimeHydrationRetryDelay)
	return tea.Tick(uiRuntimeHydrationRetryDelay, func(time.Time) tea.Msg {
		return runtimeTranscriptRetryMsg{token: token, syncCause: req.syncCause, recoveryCause: req.recoveryCause, req: req}
	})
}

func (m *uiModel) resumeRuntimeEventsAfterHydrationIfUnowned() tea.Cmd {
	if m == nil || !m.waitRuntimeEventAfterHydration {
		return nil
	}
	m.waitRuntimeEventAfterHydration = false
	return m.waitRuntimeEventCmd()
}

func (m *uiModel) handleRuntimeMainViewRefreshed(msg runtimeMainViewRefreshedMsg) tea.Cmd {
	if msg.token != m.runtimeMainViewToken {
		return nil
	}
	m.runtimeMainViewBusy = false
	req := msg.req
	if req == (runtimeMainViewRefreshRequest{}) {
		req = m.runtimeMainViewActiveRequest
	}
	req = normalizeRuntimeMainViewRefreshRequest(req)
	m.runtimeMainViewActiveRequest = runtimeMainViewRefreshRequest{}
	if m.shouldDeferRuntimeMainViewRefresh(req) {
		m.mergePendingRuntimeMainViewRefresh(req)
		m.logf("ui.runtime.main_view.drop_response_blocked cause=%s streaming=%t process_overlay=%t", req.cause, m.runtimeSyncBlockedByStreaming(), m.runtimeSyncBlockedByProcessOverlay())
		return m.drainPendingRuntimeMainViewRefresh().cmd
	}
	if msg.err != nil {
		m.observeRuntimeRequestResult(msg.err)
		m.logf("ui.runtime.main_view err=%q", msg.err.Error())
		return m.drainPendingRuntimeMainViewRefresh().cmd
	}
	m.observeRuntimeRequestResult(nil)
	m.applyRuntimeMainViewState(msg.view)
	noticeCmd := runtimeMainViewStartupUpdateNoticeCmd(req, msg.view)
	return sequenceCmds(m.runtimeAdapter().applyProjectedSessionMetadata(msg.view.Session), noticeCmd, m.drainPendingRuntimeMainViewRefresh().cmd)
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

func (m *uiModel) handleRuntimeTranscriptRefreshed(msg runtimeTranscriptRefreshedMsg) tea.Cmd {
	if msg.token != m.runtimeTranscriptToken {
		return nil
	}
	m.runtimeTranscriptBusy = false
	activeReq := m.runtimeTranscriptActiveRequest
	if msg.syncRequest != (runtimeTranscriptSyncRequest{}) {
		activeReq = msg.syncRequest
	}
	if activeReq == (runtimeTranscriptSyncRequest{}) {
		activeReq = runtimeTranscriptSyncRequestForPage(msg.req, false, msg.syncCause, msg.recoveryCause)
	}
	activeReq = m.normalizeRuntimeTranscriptSyncRequest(activeReq)
	m.runtimeTranscriptActiveRequest = runtimeTranscriptSyncRequest{}
	if m.shouldDeferRuntimeTranscriptSync(activeReq) {
		m.mergePendingRuntimeTranscriptSync(activeReq)
		m.logRuntimeTranscriptPolicyDecision("drop_response_blocked", activeReq)
		resumeCmd := m.resumeRuntimeEventsAfterHydrationIfUnowned()
		drain := m.drainPendingRuntimeTranscriptSync()
		if drain.started {
			if resumeCmd != nil {
				m.waitRuntimeEventAfterHydration = true
			}
			return drain.cmd
		}
		return sequenceCmds(drain.cmd, resumeCmd)
	}
	if msg.err != nil {
		m.observeRuntimeRequestResult(msg.err)
		m.logf("ui.runtime.transcript err=%q sync_cause=%s recovery_cause=%s", msg.err.Error(), msg.syncCause, msg.recoveryCause)
		m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.hydrate_response", map[string]string{
			"session_id":     m.sessionID,
			"mode":           string(m.view.Mode()),
			"path":           "hydrate",
			"token":          strconv.FormatUint(msg.token, 10),
			"sync_cause":     string(msg.syncCause),
			"recovery_cause": string(msg.recoveryCause),
			"err":            msg.err.Error(),
		}))
		retryCmd := m.scheduleRuntimeTranscriptRetryForRequest(activeReq)
		resumeCmd := m.resumeRuntimeEventsAfterHydrationIfUnowned()
		drain := m.drainPendingRuntimeTranscriptSync()
		if drain.started {
			if resumeCmd != nil {
				m.waitRuntimeEventAfterHydration = true
			}
			return tea.Batch(retryCmd, drain.cmd)
		}
		return tea.Batch(retryCmd, drain.cmd, resumeCmd)
	}
	m.observeRuntimeRequestResult(nil)
	m.logTranscriptPageDiag("transcript.diag.client.hydrate_response", msg.req, msg.transcript, map[string]string{
		"path":           "hydrate",
		"token":          strconv.FormatUint(msg.token, 10),
		"sync_cause":     string(msg.syncCause),
		"recovery_cause": string(msg.recoveryCause),
	})
	recovered := m.runtimeTranscriptRetry != 0
	if m.runtimeTranscriptRetry != 0 {
		m.runtimeTranscriptRetry++
	}
	if recovered {
		m.logf("ui.runtime.transcript.recovered token=%d", msg.token)
	}
	applyCmd := m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(msg.req, msg.transcript, msg.recoveryCause)
	resumeCmd := m.resumeRuntimeEventsAfterHydrationIfUnowned()
	drain := m.drainPendingRuntimeTranscriptSync()
	if drain.started {
		if resumeCmd != nil {
			m.waitRuntimeEventAfterHydration = true
		}
		return sequenceCmds(applyCmd, drain.cmd)
	}
	if m.pendingQueuedDrainAfterHydration {
		m.queuedDrainReadyAfterHydration = true
	}
	return sequenceCmds(applyCmd, drain.cmd, m.flushQueuedInputsAfterHydration(), resumeCmd)
}

func (m *uiModel) flushQueuedInputsAfterHydration() tea.Cmd {
	if m == nil || !m.pendingQueuedDrainAfterHydration {
		return nil
	}
	if !m.queuedDrainReadyAfterHydration {
		return nil
	}
	if m.isBusy() ||
		m.isInputSubmitLocked() {
		if len(m.queued) == 0 || strings.TrimSpace(m.activeSubmit.text) != "" {
			m.pendingQueuedDrainAfterHydration = false
			m.queuedDrainReadyAfterHydration = false
		}
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
