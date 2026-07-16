package app

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type uiFeatureUpdateResult struct {
	model   *uiModel
	cmd     tea.Cmd
	handled bool
}

type uiFeatureReducer func(tea.Msg) uiFeatureUpdateResult

func handledUIFeatureUpdate(model *uiModel, cmd tea.Cmd) uiFeatureUpdateResult {
	return uiFeatureUpdateResult{model: model, cmd: cmd, handled: true}
}

func (m *uiModel) reduceFeatureMessage(msg tea.Msg) uiFeatureUpdateResult {
	reducers := []uiFeatureReducer{
		m.reduceKeyMessage,
		m.reduceWindowMessage,
		m.reduceDispatchedEvent,
		m.reduceOngoingMessage,
		m.reduceRuntimeMessage,
		m.reduceStatusMessage,
		m.reduceWorktreeMessage,
		m.reduceAskMessage,
		m.reducePathReferenceMessage,
		m.reduceNoticeMessage,
		m.reduceInputAsyncMessage,
		m.reduceProcessMessage,
		m.reduceClipboardMessage,
	}
	for _, reducer := range reducers {
		if result := reducer(msg); result.handled {
			return result
		}
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) reduceStatusMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case statusRefreshDoneMsg:
		if msg.token != m.status.refreshToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.status.pendingSections = nil
		m.status.sectionWarnings = nil
		m.status.loading = false
		if msg.err != nil {
			m.status.error = msg.err.Error()
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.sendTransientStatusWithNoticeID(msg.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		m.status.error = ""
		m.status.snapshot = msg.snapshot
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, nil)
	case statusBaseRefreshDoneMsg:
		if msg.token != m.status.refreshToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.status.error = ""
		snapshot := msg.snapshot
		if statusHasAuthData(m.status.snapshot) {
			snapshot.Auth = m.status.snapshot.Auth
			snapshot.Subscription = m.status.snapshot.Subscription
		}
		if m.status.snapshot.Git.Visible {
			snapshot.Git = m.status.snapshot.Git
		}
		snapshot.SkillPolicy = m.status.snapshot.SkillPolicy
		if m.status.snapshot.Skills != nil {
			snapshot.Skills = m.status.snapshot.Skills
		}
		if m.status.snapshot.SkillTokenCounts != nil {
			snapshot.SkillTokenCounts = m.status.snapshot.SkillTokenCounts
		}
		if m.status.snapshot.AgentsPaths != nil {
			snapshot.AgentsPaths = m.status.snapshot.AgentsPaths
		}
		if m.status.snapshot.AgentTokenCounts != nil {
			snapshot.AgentTokenCounts = m.status.snapshot.AgentTokenCounts
		}
		m.status.snapshot = snapshot
		m.finishStatusSectionRefresh(uiStatusSectionBase, msg.snapshot.CollectorWarning)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, nil)
	case statusAuthRefreshDoneMsg:
		if msg.token != m.status.refreshToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.status.snapshot.Auth = msg.result.Auth
		m.status.snapshot.Subscription = msg.result.Subscription
		if m.statusRepository != nil {
			m.statusRepository.StoreAuth(msg.cacheKey, msg.result, time.Now())
		}
		m.finishStatusSectionRefresh(uiStatusSectionAuth, msg.result.Warning)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, nil)
	case statusGitRefreshDoneMsg:
		if msg.background {
			m.statusGitBackgroundInFlight = false
		}
		if msg.token != m.status.refreshToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.status.snapshot.Git = msg.result.Git
		if m.statusRepository != nil {
			m.statusRepository.StoreGit(msg.cacheKey, msg.result, time.Now())
		}
		m.finishStatusSectionRefresh(uiStatusSectionGit, "")
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, nil)
	case statusEnvironmentRefreshDoneMsg:
		if msg.token != m.status.refreshToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.status.snapshot.SkillPolicy = msg.result.SkillPolicy
		m.status.snapshot.Skills = msg.result.Skills
		m.status.snapshot.SkillTokenCounts = msg.result.SkillTokenCounts
		m.status.snapshot.AgentsPaths = msg.result.AgentsPaths
		m.status.snapshot.AgentTokenCounts = msg.result.AgentTokenCounts
		if m.statusRepository != nil {
			m.statusRepository.StoreEnvironment(msg.cacheKey, msg.result, time.Now())
		}
		m.finishStatusSectionRefresh(uiStatusSectionEnvironment, msg.result.CollectorWarning)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, nil)
	}
	return uiFeatureUpdateResult{}
}
