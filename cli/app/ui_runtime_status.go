package app

import (
	"strings"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type statusLinePhase uint8

const (
	statusLinePhasePrimary statusLinePhase = iota
	statusLinePhaseSecondary
	statusLinePhaseSuccess
	statusLinePhaseError
)

func (m *uiModel) applyRuntimeMainViewState(view clientui.RuntimeMainView) tea.Cmd {
	if m == nil {
		return nil
	}
	if view.Version.Validate() == nil {
		if m.acceptRuntimeReadModelVersion(view.Version, true) == runtimeReadModelVersionIgnore {
			if view.Activity.State != "" && runtimeActivityConflictsWithProjection(view.Activity, m) {
				_ = m.sendTransientStatusWithNoticeID("conflicting runtime activity read-model update ignored", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
			}
			return nil
		}
	}
	status := view.Status
	m.reviewerMode = status.ReviewerFrequency
	m.reviewerEnabled = status.ReviewerEnabled
	m.autoCompactionEnabled = status.AutoCompactionEnabled
	m.questionsEnabled = status.QuestionsEnabled
	m.fastModeAvailable = status.FastModeAvailable
	m.fastModeEnabled = status.FastModeEnabled
	m.conversationFreshness = status.ConversationFreshness
	m.setRuntimeContextUsage(view.Session.SessionID, status.ContextUsage)
	if view.Activity.State != "" {
		if err := m.applyRuntimeActivityProjection(view.Activity); err != nil {
			m.activity = uiActivityError
			_ = m.sendTransientStatusWithNoticeID("invalid runtime activity: "+err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
			return nil
		}
	}
	if view.Activity.State != "" && !view.Activity.ActiveForControl() && m.hasPendingInterrupt() {
		if m.pendingInterruptMissingInputReconciliation(view) {
			return nil
		}
		return m.acknowledgePendingInterrupt()
	}
	return nil
}

func (m *uiModel) runtimeMainView() clientui.RuntimeMainView {
	m.checkTUIBlockingOperation("runtime main-view read", "MainView")
	if client := m.runtimeClient(); client != nil {
		return client.MainView()
	}
	return clientui.RuntimeMainView{
		Status:  m.localRuntimeStatus(),
		Session: m.localRuntimeSessionView(),
	}
}

func (m *uiModel) refreshRuntimeMainView() clientui.RuntimeMainView {
	m.checkTUIBlockingOperation("runtime main-view refresh", "RefreshMainView")
	if client := m.runtimeClient(); client != nil {
		var (
			view clientui.RuntimeMainView
			err  error
		)
		if requestClient, ok := client.(runtimeMainViewReconciliationClient); ok {
			view, err = requestClient.RefreshMainViewWithPendingRefs(m.pendingRuntimeOperationRefs())
		} else {
			view, err = client.RefreshMainView()
		}
		if err == nil {
			m.observeRuntimeRequestResult(nil)
			return view
		}
		m.observeRuntimeRequestResult(err)
		return client.MainView()
	}
	return clientui.RuntimeMainView{
		Status:  m.localRuntimeStatus(),
		Session: m.localRuntimeSessionView(),
	}
}

func (m *uiModel) runtimeStatus() clientui.RuntimeStatus {
	m.checkTUIBlockingOperation("runtime status read", "Status/MainView")
	view := m.runtimeMainView()
	status := view.Status
	if m.runtimeContextUsageAppliesTo(view.Session.SessionID) {
		status.ContextUsage = m.runtimeContextUsage
	}
	return status
}

func (m *uiModel) cachedRuntimeMainView() clientui.RuntimeMainView {
	client := m.runtimeClient()
	if cached, ok := client.(interface {
		CachedMainView() (clientui.RuntimeMainView, bool)
	}); ok {
		if cachedView, hasCached := cached.CachedMainView(); hasCached {
			return cachedView
		}
	}
	return clientui.RuntimeMainView{
		Status:  m.localRuntimeStatus(),
		Session: m.localRuntimeSessionView(),
	}
}

func (m *uiModel) cachedRuntimeStatus() clientui.RuntimeStatus {
	view := m.cachedRuntimeMainView()
	status := view.Status
	if m.runtimeContextUsageAppliesTo(view.Session.SessionID) {
		status.ContextUsage = m.runtimeContextUsage
	}
	return status
}

func (m *uiModel) statusLinePhase() statusLinePhase {
	if m == nil {
		return statusLinePhasePrimary
	}
	if m.isCompacting() {
		return statusLinePhaseSecondary
	}
	if m.isReviewerRunning() {
		return statusLinePhaseSuccess
	}
	if goalIsActive(m.cachedRuntimeStatus().Goal) {
		return statusLinePhasePrimary
	}
	if m.activity == uiActivityError {
		return statusLinePhaseError
	}
	return statusLinePhasePrimary
}

func (m *uiModel) statusLineLabel() string {
	if m == nil {
		return ""
	}
	if m.isCompacting() {
		return "compacting"
	}
	if m.isReviewerRunning() {
		return "review"
	}
	if goalIsPresent(m.cachedRuntimeStatus().Goal) {
		return "goal"
	}
	if m.activity == uiActivityError {
		return "error"
	}
	return ""
}

func (m *uiModel) statusLineSpinning() bool {
	if m == nil {
		return false
	}
	return (m.runtimeActivityBusy() && m.activity != uiActivityQuestion) ||
		m.runtimeLifecycle.Compaction.IsRunning() ||
		m.runtimeLifecycle.Reviewer.IsRunning()
}

func (m *uiModel) refreshRuntimeStatus() clientui.RuntimeStatus {
	m.checkTUIBlockingOperation("runtime status refresh", "RefreshMainView")
	view := m.refreshRuntimeMainView()
	status := view.Status
	if m.runtimeContextUsageAppliesTo(view.Session.SessionID) {
		status.ContextUsage = m.runtimeContextUsage
	}
	return status
}

func (m *uiModel) applyRuntimeEventStatus(evt clientui.Event) {
	if m == nil || (evt.ContextUsage == nil && evt.GoalStatus == nil && evt.Kind != clientui.EventRuntimeActivityChanged) {
		return
	}
	if evt.ContextUsage != nil {
		m.setRuntimeContextUsage(m.currentRuntimeSessionID(), *evt.ContextUsage)
	}
	if observer, ok := m.runtimeClient().(interface{ observeRuntimeEventStatus(clientui.Event) }); ok {
		observer.observeRuntimeEventStatus(evt)
	}
}

func (m *uiModel) setRuntimeContextUsage(sessionID string, usage clientui.RuntimeContextUsage) {
	if m == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		m.runtimeContextUsage = clientui.RuntimeContextUsage{}
		m.runtimeContextUsageSession = ""
		return
	}
	m.runtimeContextUsage = usage
	m.runtimeContextUsageSession = sessionID
}

func (m *uiModel) runtimeContextUsageAppliesTo(sessionID string) bool {
	if m == nil || m.runtimeContextUsage.WindowTokens <= 0 {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(m.sessionID)
	}
	return sessionID != "" && strings.TrimSpace(m.runtimeContextUsageSession) == sessionID
}

func (m *uiModel) currentRuntimeSessionID() string {
	if m == nil {
		return ""
	}
	if sessionID := strings.TrimSpace(m.sessionID); sessionID != "" {
		return sessionID
	}
	if client := m.runtimeClient(); client != nil {
		if cached, ok := client.(interface {
			CachedMainView() (clientui.RuntimeMainView, bool)
		}); ok {
			view, hasCached := cached.CachedMainView()
			if hasCached {
				return strings.TrimSpace(view.Session.SessionID)
			}
		}
	}
	return ""
}

func (m *uiModel) localRuntimeStatus() clientui.RuntimeStatus {
	return clientui.RuntimeStatus{
		ReviewerFrequency:                 m.reviewerMode,
		ReviewerEnabled:                   m.reviewerEnabled,
		AutoCompactionEnabled:             m.autoCompactionEnabled,
		QuestionsEnabled:                  m.questionsEnabled,
		FastModeAvailable:                 m.fastModeAvailable,
		FastModeEnabled:                   m.fastModeEnabled,
		ConversationFreshness:             m.conversationFreshness,
		LastCommittedAssistantFinalAnswer: localLastCommittedAssistantFinalAnswer(m.transcriptEntries),
		ThinkingLevel:                     m.thinkingLevel,
	}
}

func localLastCommittedAssistantFinalAnswer(entries []tui.TranscriptEntry) string {
	answer := ""
	for _, entry := range entries {
		if !transcriptEntryAffectsCommittedAssistantFinalAnswer(entry) {
			continue
		}
		if entry.Role == tui.TranscriptRoleAssistant && string(entry.Phase) == clientui.ChatEntryPhaseFinalAnswer && strings.TrimSpace(entry.Text) != "" {
			answer = entry.Text
			continue
		}
		answer = ""
	}
	return answer
}

func transcriptEntryAffectsCommittedAssistantFinalAnswer(entry tui.TranscriptEntry) bool {
	switch entry.Role {
	case "", tui.TranscriptRoleSystem, tui.TranscriptRoleError, tui.TranscriptRoleWarning, tui.TranscriptRoleCacheWarning, tui.TranscriptRoleReviewerStatus, tui.TranscriptRoleReviewerSuggestions, tui.TranscriptRoleDeveloperFeedback:
		return false
	case tui.TranscriptRoleDeveloperErrorFeedback:
		return false
	default:
		return true
	}
}
