package app

import (
	"strings"

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
	status := view.Status
	if strings.TrimSpace(status.ThinkingLevel) != "" {
		m.thinkingLevel = status.ThinkingLevel
	}
	m.reviewerMode = status.ReviewerFrequency
	m.reviewerEnabled = status.ReviewerEnabled
	m.autoCompactionEnabled = status.AutoCompactionEnabled
	m.questionsEnabled = status.QuestionsEnabled
	m.fastModeAvailable = status.FastModeAvailable
	m.fastModeEnabled = status.FastModeEnabled
	m.compactionMode = status.CompactionMode
	m.compactionCount = status.CompactionCount
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

func (m *uiModel) initialRuntimeMainView() (clientui.RuntimeMainView, bool) {
	if m == nil {
		return clientui.RuntimeMainView{}, false
	}
	if client := m.runtimeClient(); client != nil {
		if cached, ok := client.(interface {
			CachedMainView() (clientui.RuntimeMainView, bool)
		}); ok {
			return cached.CachedMainView()
		}
		return m.runtimeMainView(), true
	}
	return m.runtimeMainView(), true
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
		usage := m.runtimeContextUsage
		status.ContextUsage.UsedTokens = usage.UsedTokens
		status.ContextUsage.WindowTokens = usage.WindowTokens
		if usage.HasAutomaticThreshold {
			status.ContextUsage.AutomaticThresholdTokens = usage.AutomaticThresholdTokens
			status.ContextUsage.HasAutomaticThreshold = true
		}
		if usage.HasCacheHitPercentage {
			status.ContextUsage.CacheHitPercent = usage.CacheHitPercent
			status.ContextUsage.HasCacheHitPercentage = true
		}
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
	if m.rollback.isActive() {
		return statusLinePhasePrimary
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
	if m.rollback.isActive() {
		return "rollback"
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
		m.runtimeLifecycle.Reviewer.IsRunning()
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
	if strings.TrimSpace(m.runtimeContextUsageSession) == sessionID {
		usage = mergeRuntimeContextUsagePolicy(m.runtimeContextUsage, usage)
	}
	m.runtimeContextUsage = usage
	m.runtimeContextUsageSession = sessionID
}

func mergeRuntimeContextUsagePolicy(
	current clientui.RuntimeContextUsage,
	incoming clientui.RuntimeContextUsage,
) clientui.RuntimeContextUsage {
	if !incoming.HasAutomaticThreshold && current.HasAutomaticThreshold {
		incoming.AutomaticThresholdTokens = current.AutomaticThresholdTokens
		incoming.HasAutomaticThreshold = true
	}
	return incoming
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
		ReviewerFrequency:     m.reviewerMode,
		ReviewerEnabled:       m.reviewerEnabled,
		AutoCompactionEnabled: m.autoCompactionEnabled,
		QuestionsEnabled:      m.questionsEnabled,
		FastModeAvailable:     m.fastModeAvailable,
		FastModeEnabled:       m.fastModeEnabled,
		ConversationFreshness: m.conversationFreshness,
		ThinkingLevel:         m.thinkingLevel,
		CompactionMode:        m.compactionMode,
		CompactionCount:       m.compactionCount,
	}
}
