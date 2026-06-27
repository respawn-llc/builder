package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"core/cli/app/internal/worktreeui"
	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/transcriptdiag"

	tea "github.com/charmbracelet/bubbletea"
)

type uiLogger interface {
	Logf(format string, args ...any)
}

func (m *uiModel) clearReviewerState() {
	m.setReviewerRunning(false)
	m.setReviewerBlocking(false)
}

type rollbackCandidate struct {
	TranscriptIndex  int
	RollbackTargetID string
	Text             string
}

func NewProjectedUIModel(runtimeClient clientui.RuntimeClient, runtimeEvents <-chan clientui.Event, askEvents <-chan askEvent, opts ...UIOption) tea.Model {
	m := newUIModelDefaults(runtimeClient, runtimeEvents, askEvents)
	for _, opt := range opts {
		opt(m)
	}
	if m.pathReferenceSearch == nil {
		m.pathReferenceSearch = newUIPathReferenceSearch()
		m.pathReferenceEvents = m.pathReferenceSearch.Events()
	}
	m.updateTranscriptDiagnosticsMode()
	m.refreshAutocompleteFromInput()
	if configurable, ok := m.engine.(interface{ SetConnectionStateObserver(func(error)) }); ok {
		runtimeConnectionEvents := make(chan runtimeConnectionStateChangedMsg, 1)
		m.runtimeConnectionEvents = runtimeConnectionEvents
		configurable.SetConnectionStateObserver(func(err error) {
			enqueueRuntimeConnectionStateChange(runtimeConnectionEvents, err)
		})
	}
	if configurable, ok := m.engine.(interface {
		SetRuntimeReconnectWarningObserver(func(string, clientui.EntryVisibility))
	}); ok {
		runtimeReconnectWarning := make(chan runtimeReconnectWarningMsg, 1)
		m.runtimeReconnectWarning = runtimeReconnectWarning
		configurable.SetRuntimeReconnectWarningObserver(func(text string, visibility clientui.EntryVisibility) {
			enqueueRuntimeReconnectWarning(runtimeReconnectWarning, text, visibility)
		})
	}
	mainView := m.runtimeMainView()
	status := mainView.Status
	m.applyRuntimeMainViewState(mainView)
	if !m.hasRuntimeClient() {
		m.reviewerEnabled = strings.TrimSpace(m.reviewerMode) != "" && strings.TrimSpace(m.reviewerMode) != "off"
	}
	if m.hasRuntimeClient() {
		seedView := mainView.Session
		_ = m.runtimeAdapter().applyProjectedSessionMetadata(seedView)
		_ = m.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, m.startupRuntimeTranscript(), clientui.TranscriptRecoveryCauseNone)
		if startupCmd := m.requestRuntimeBootstrapTranscriptSync(); startupCmd != nil {
			m.startupCmds = append(m.startupCmds, startupCmd)
		}
		m.runtimeTranscriptBusy = false
	} else {
		for _, entry := range m.initialTranscript {
			if strings.TrimSpace(entry.Text) == "" {
				continue
			}
			role := tui.TranscriptRoleFromWire(entry.Role)
			m.transcriptEntries = append(m.transcriptEntries, tui.TranscriptEntry{Role: role, Text: entry.Text, RollbackTargetID: entry.RollbackTargetID})
			m.forwardToView(tui.AppendTranscriptMsg{Role: role, Text: entry.Text})
		}
		m.transcriptBaseOffset = 0
		m.transcriptTotalEntries = len(m.transcriptEntries)
		m.refreshRollbackCandidates()
	}
	if gitStartupCmd := m.statusLineGitRefreshCmd(); gitStartupCmd != nil {
		m.statusGitBackgroundInFlight = true
		m.startupCmds = append(m.startupCmds, gitStartupCmd)
	}
	if m.pathReferenceSearch != nil && strings.TrimSpace(m.statusConfig.WorkspaceRoot) != "" {
		m.startupCmds = append(m.startupCmds, func() tea.Msg {
			m.pathReferenceSearch.StartPrewarm(strings.TrimSpace(m.statusConfig.WorkspaceRoot))
			return nil
		})
	}
	if m.startupUpdateNotice && m.hasRuntimeClient() {
		m.startupCmds = append(m.startupCmds, m.startupUpdateNoticeCmd(status.Update))
	}
	m.layout().syncViewport()
	return m
}

func (m *uiModel) handleRenderDiagnostic(diag tui.RenderDiagnostic) {
	m.startupCmds = append(m.startupCmds, func() tea.Msg {
		return renderDiagnosticMsg{diagnostic: diag}
	})
}

func (m *uiModel) applyRenderDiagnostic(diag tui.RenderDiagnostic) tea.Cmd {
	message := strings.TrimSpace(diag.Message)
	if message == "" {
		return nil
	}
	severity := strings.TrimSpace(string(diag.Severity))
	if severity == "" {
		severity = string(tui.RenderDiagnosticSeverityWarn)
	}
	m.logf("render.diagnostic severity=%s component=%s message=%q", severity, strings.TrimSpace(diag.Component), message)
	if diag.Err != nil {
		m.logf("render.diagnostic.err component=%s err=%q", strings.TrimSpace(diag.Component), diag.Err.Error())
	}
	kind := uiStatusNoticeNeutral
	switch diag.Severity {
	case tui.RenderDiagnosticSeverityError, tui.RenderDiagnosticSeverityFatal:
		kind = uiStatusNoticeError
	default:
		kind = uiStatusNoticeNeutral
	}
	return m.sendTransientStatusWithNoticeID(message, kind, transientStatusDuration, uiStatusNoticeReplace, "")
}

func (m *uiModel) applyRunLoggerDiagnostic(diag runLoggerDiagnostic) tea.Cmd {
	message := strings.TrimSpace(diag.Message)
	if message == "" {
		message = "run logger diagnostic"
	}
	m.logf("run_logger.diagnostic kind=%s message=%q", strings.TrimSpace(diag.Kind), message)
	if diag.Err != nil {
		m.logf("run_logger.diagnostic.err kind=%s err=%q", strings.TrimSpace(diag.Kind), diag.Err.Error())
	}
	return m.sendTransientStatusWithNoticeID(message, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
}

func (m *uiModel) handleRuntimeEventBatch(events []clientui.Event) (*uiModel, tea.Cmd) {
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.runtime_batch", map[string]string{
		"session_id":             strings.TrimSpace(m.sessionID),
		"mode":                   string(m.view.Mode()),
		"event_count":            strconv.Itoa(len(events)),
		"pending_runtime_events": strconv.Itoa(len(m.pendingRuntimeEvents)),
		"wait_after_hydration":   strconv.FormatBool(m.waitRuntimeEventAfterHydration),
	}))
	result := m.runtimeAdapter().applyProjectedRuntimeEventsBatch(events)
	cmd := result.cmd
	cmd = tea.Batch(cmd, m.reconcileSpinnerTicking(true))
	if !result.awaitsHydration {
		cmd = sequenceCmds(cmd, m.flushQueuedInputsAfterHydration())
		cmd = sequenceCmds(cmd, m.inputController().resumeQueuedInputsAfterIdleRuntime())
	}
	m.layout().syncViewport()
	if result.awaitsHydration {
		m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.runtime_batch_pause", map[string]string{
			"session_id":             strings.TrimSpace(m.sessionID),
			"mode":                   string(m.view.Mode()),
			"pending_runtime_events": strconv.Itoa(len(m.pendingRuntimeEvents)),
		}))
		m.waitRuntimeEventAfterHydration = true
	}
	if result.awaitsHydration {
		return m, cmd
	}
	return m, tea.Batch(m.waitRuntimeEventCmd(), cmd)
}

func (m *uiModel) waitRuntimeEventCmd() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.waitRuntimeEventAfterHydration {
		m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.wait_runtime_event_blocked", map[string]string{
			"session_id":             strings.TrimSpace(m.sessionID),
			"mode":                   string(m.view.Mode()),
			"pending_runtime_events": strconv.Itoa(len(m.pendingRuntimeEvents)),
			"wait_after_hydration":   strconv.FormatBool(m.waitRuntimeEventAfterHydration),
		}))
		return nil
	}
	if len(m.pendingRuntimeEvents) == 0 {
		return waitRuntimeEvent(m.runtimeEvents)
	}
	evt := m.pendingRuntimeEvents[0]
	m.pendingRuntimeEvents = append([]clientui.Event(nil), m.pendingRuntimeEvents[1:]...)
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.wait_runtime_event_resume_pending", map[string]string{
		"session_id":             strings.TrimSpace(m.sessionID),
		"mode":                   string(m.view.Mode()),
		"kind":                   string(evt.Kind),
		"pending_runtime_events": strconv.Itoa(len(m.pendingRuntimeEvents)),
	}))
	return func() tea.Msg {
		return runtimeEventBatchMsg{events: []clientui.Event{evt}}
	}
}

func (m *uiModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		m.waitRuntimeEventCmd(),
		waitAskEvent(m.askEvents),
		waitPathReferenceSearchEvent(m.pathReferenceEvents),
		tea.SetWindowTitle(sessionTitle(m.sessionName)),
		tea.WindowSize(),
	}
	if m.runtimeConnectionEvents != nil {
		cmds = append(cmds, waitRuntimeConnectionStateChange(m.runtimeConnectionEvents))
	}
	if m.runtimeReconnectWarning != nil {
		cmds = append(cmds, waitRuntimeReconnectWarning(m.runtimeReconnectWarning))
	}
	cmds = append([]tea.Cmd{tea.ClearScreen}, cmds...)
	if startupSubmitCmd := m.startupSubmitCmd(); startupSubmitCmd != nil {
		cmds = append(cmds, startupSubmitCmd)
	}
	if len(m.startupCmds) > 0 {
		cmds = append(cmds, m.startupCmds...)
		m.startupCmds = nil
	}
	return tea.Batch(cmds...)
}

func (m *uiModel) startupSubmitCmd() tea.Cmd {
	startupText := strings.TrimSpace(m.startupSubmit)
	if startupText == "" {
		return nil
	}
	if m.startupSubmitPromptHistoryRecorded {
		return m.inputController().startSubmissionWithPreSubmitQueuePosition(startupText, preSubmitQueueBack, "", true)
	}
	return m.inputController().startSubmissionWithPromptHistoryAndQueuePositionAndID(startupText, preSubmitQueueBack, "")
}

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer m.enterUIMainThread("Update")()
	if probe, ok := msg.(uiModelProbeMessage); ok {
		probe.probeUIModel(m)
		return m, nil
	}
	switch msg.(type) {
	case tea.FocusMsg:
		m.terminalFocus.MarkFocused()
		return m, nil
	case tea.BlurMsg:
		m.terminalFocus.MarkBlurred()
		return m, nil
	}
	if result := m.reduceFeatureMessage(msg); result.handled {
		return result.model, result.cmd
	}

	if _, ok := msg.(tea.MouseMsg); ok && m.rollback.isActive() {
		m.layout().syncViewport()
		return m, nil
	}
	m.forwardToView(msg)
	m.layout().syncViewport()
	return m, m.maybeRequestDetailTranscriptPage()
}

func (m *uiModel) setDebugKeyTransientStatus(raw tea.Msg, normalized tea.KeyMsg, source string) {
	rawString := ""
	if stringer, ok := raw.(fmt.Stringer); ok {
		rawString = stringer.String()
	}
	m.transientStatusToken++
	m.transientStatus = fmt.Sprintf("key src=%s raw=%q norm=%q type=%d", source, rawString, normalized.String(), normalized.Type)
	m.transientStatusKind = uiStatusNoticeNeutral
}

func statusHasAuthData(snapshot uiStatusSnapshot) bool {
	return snapshot.Auth.Visible || snapshot.Subscription.Applicable || strings.TrimSpace(snapshot.Subscription.Summary) != "" || len(snapshot.Subscription.Windows) > 0
}

func (m *uiModel) forwardToView(msg tea.Msg) {
	prevMode := m.view.Mode()
	next, _ := m.view.Update(msg)
	casted, ok := next.(tui.Model)
	if ok {
		m.view = casted
	}
	if prevMode != m.view.Mode() && m.surface().isTranscript() {
		m.activeSurface = surfaceForTranscriptMode(m.view.Mode())
		m.syncRendererOutputGate()
	}
	if prevMode != m.view.Mode() && m.view.Mode() == tui.ModeDetail && m.hasRuntimeClient() {
		m.primeDetailTranscriptFromCurrentTail()
		page := m.detailTranscript.page()
		nextDetail, _ := m.view.Update(tui.SetConversationMsg{
			BaseOffset:   page.Offset,
			TotalEntries: page.TotalEntries,
			Entries:      transcriptEntriesFromPage(page),
			Ongoing:      page.Streaming,
			OngoingError: page.StreamingError,
		})
		if castedDetail, ok := nextDetail.(tui.Model); ok {
			m.view = castedDetail
		}
	}
}

func (m *uiModel) Close() {
	if m == nil {
		return
	}
	m.closeNativeSurface()
	m.syncRendererOutputGate()
	if m.pathReferenceSearch != nil {
		m.pathReferenceSearch.Stop()
		m.pathReferenceSearch = nil
		m.pathReferenceEvents = nil
	}
}

func (m *uiModel) Transition() UITransition {
	if m.exitAction == UIActionExit {
		return UITransition{
			Action: serverapi.SessionTransitionActionNone,
			Exit:   true,
		}
	}
	return UITransition{
		Action:                       m.exitAction,
		InitialPrompt:                m.nextSessionInitialPrompt,
		InitialPromptHistoryRecorded: m.nextSessionInitialPromptHistoryRecorded,
		InitialInput:                 m.nextSessionInitialInput,
		TargetSessionID:              strings.TrimSpace(m.nextSessionID),
		ForkRollbackTargetID:         m.nextForkRollbackTargetID,
		ParentSessionID:              strings.TrimSpace(m.nextParentSessionID),
	}
}

func (m *uiModel) logf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Logf(format, args...)
	}
}

func (m *uiModel) logTranscriptDiag(line string) {
	if m == nil || !m.transcriptDiagnosticsEnabled() {
		return
	}
	m.logf("%s", strings.TrimSpace(line))
}

func (m *uiModel) transcriptDiagnosticsEnabled() bool {
	if m == nil {
		return false
	}
	return m.transcriptDiagnostics || transcriptdiag.Enabled(m.debugMode, os.Getenv)
}

func (m *uiModel) updateTranscriptDiagnosticsMode() {
	if m == nil {
		return
	}
	if configurable, ok := m.engine.(interface{ SetTranscriptDiagnosticsEnabled(bool) }); ok {
		configurable.SetTranscriptDiagnosticsEnabled(m.transcriptDiagnosticsEnabled())
	}
}

func (m *uiModel) inputController() uiInputController {
	return uiInputController{model: m}
}

func worktreeDeleteSuccessStatus(resp serverapi.WorktreeDeleteResponse) string {
	status := "Deleted worktree " + worktreeui.DisplayName(resp.Worktree)
	if cleanup := strings.TrimSpace(resp.BranchCleanupMessage); cleanup != "" {
		status += ". " + cleanup
	}
	return status
}

func (m *uiModel) askController() uiAskController {
	return uiAskController{model: m}
}

func (m *uiModel) runtimeAdapter() uiRuntimeAdapter {
	return uiRuntimeAdapter{model: m}
}

func (m *uiModel) sendTransientStatusWithNoticeID(message string, kind uiStatusNoticeKind, duration time.Duration, delivery uiStatusNoticeDelivery, noticeID string) tea.Cmd {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	notice := uiStatusNotice{Text: strings.TrimSpace(message), Kind: kind, Duration: duration, NoticeID: strings.TrimSpace(noticeID)}
	if delivery == uiStatusNoticeQueue && strings.TrimSpace(m.transientStatus) != "" {
		if m.transientStatus == notice.Text && m.transientStatusKind == notice.Kind && m.transientStatusNoticeID == notice.NoticeID {
			return nil
		}
		if len(m.transientStatusQueue) > 0 {
			last := m.transientStatusQueue[len(m.transientStatusQueue)-1]
			if last == notice {
				return nil
			}
		}
		m.transientStatusQueue = append(m.transientStatusQueue, notice)
		return nil
	}
	return m.showTransientStatusNotice(notice)
}

func (m *uiModel) showTransientStatusNotice(notice uiStatusNotice) tea.Cmd {
	m.transientStatusToken++
	token := m.transientStatusToken
	m.transientStatus = strings.TrimSpace(notice.Text)
	m.transientStatusKind = notice.Kind
	m.transientStatusNoticeID = strings.TrimSpace(notice.NoticeID)
	if notice.Kind == uiStatusNoticeUpdateAvailable {
		m.startupUpdateShown = true
	}
	return scheduleTransientStatusClear(notice.Duration, token)
}

func (m *uiModel) advanceTransientStatusQueue() tea.Cmd {
	m.transientStatus = ""
	m.transientStatusKind = uiStatusNoticeNeutral
	m.transientStatusNoticeID = ""
	if len(m.transientStatusQueue) == 0 {
		m.layout().syncViewport()
		return nil
	}
	next := m.transientStatusQueue[0]
	m.transientStatusQueue = append([]uiStatusNotice(nil), m.transientStatusQueue[1:]...)
	cmd := m.showTransientStatusNotice(next)
	m.layout().syncViewport()
	return cmd
}

func (m *uiModel) startupUpdateNoticeCmd(status clientui.UpdateStatus) tea.Cmd {
	if status.Available && strings.TrimSpace(status.LatestVersion) != "" {
		return func() tea.Msg {
			return startupUpdateNoticeMsg{version: status.LatestVersion}
		}
	}
	if status.Checked {
		return nil
	}
	return m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseStartupUpdate)).cmd
}

func batchCmds(cmds ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil {
			filtered = append(filtered, cmd)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return tea.Batch(filtered...)
}

func (m *uiModel) layout() uiViewLayout {
	return uiViewLayout{model: m}
}
