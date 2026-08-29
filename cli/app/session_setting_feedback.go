package app

import (
	"core/shared/clientui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) applySessionSettingFeedback(feedback clientui.TranscriptSessionSettingFeedback) tea.Cmd {
	if !feedback.Changed {
		return nil
	}
	return m.sendTransientStatusWithNoticeID(
		sessionSettingFeedbackNotice(feedback),
		uiStatusNoticeSuccess,
		transientStatusDuration,
		uiStatusNoticeReplace,
		sessionSettingNoticeID(feedback.Kind),
	)
}

func sessionSettingFeedbackNotice(feedback clientui.TranscriptSessionSettingFeedback) string {
	switch feedback.Kind {
	case clientui.SessionSettingSessionName:
		if *feedback.SessionName == "" {
			return "Session name reset"
		}
		return "Session name: " + *feedback.SessionName
	case clientui.SessionSettingThinking:
		return "Thinking: " + *feedback.Thinking
	case clientui.SessionSettingFastMode:
		return "Fast: " + chatSettingsOnOffValues[*feedback.FastMode]
	case clientui.SessionSettingSupervisor:
		return "Supervisor: " + chatSettingsSupervisorNotices[serverapi.ChatSettingsSupervisorValue(*feedback.Supervisor)]
	case clientui.SessionSettingQuestions:
		return "Questions: " + chatSettingsOnOffValues[*feedback.Questions]
	case clientui.SessionSettingAutoCompaction:
		return "Auto-compaction: " + chatSettingsOnOffValues[*feedback.AutoCompaction]
	default:
		panic("validated Session setting feedback kind is exhaustive")
	}
}

func sessionSettingNoticeID(kind clientui.SessionSettingKind) string {
	return "session-setting:" + string(kind)
}
