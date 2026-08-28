package app

import (
	"strings"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) applySessionSettingFeedback(feedback clientui.TranscriptSessionSettingFeedback) tea.Cmd {
	text, kind := sessionSettingFeedbackPresentation(feedback)
	return m.sendTransientStatusWithNoticeID(
		text,
		kind,
		transientStatusDuration,
		uiStatusNoticeReplace,
		"",
	)
}

func sessionSettingFeedbackPresentation(feedback clientui.TranscriptSessionSettingFeedback) (string, uiStatusNoticeKind) {
	already := ""
	if !feedback.Changed {
		already = "already "
	}
	switch feedback.Kind {
	case clientui.SessionSettingSessionName:
		if *feedback.SessionName == "" {
			return "Session name " + already + "reset", uiStatusNoticeSuccess
		}
		return "Session name " + already + "set to " + *feedback.SessionName, uiStatusNoticeSuccess
	case clientui.SessionSettingThinking:
		return "Thinking level " + already + "set to " + *feedback.Thinking, uiStatusNoticeSuccess
	case clientui.SessionSettingFastMode:
		return toggleSettingFeedback("Fast mode", *feedback.FastMode, feedback.Changed), uiStatusNoticeSuccess
	case clientui.SessionSettingSupervisor:
		mode := strings.TrimSpace(*feedback.Supervisor)
		if mode == "off" {
			return toggleSettingFeedback("Supervisor invocation", false, feedback.Changed), uiStatusNoticeInfo
		}
		return toggleSettingFeedback("Supervisor invocation", true, feedback.Changed) + " (frequency: " + mode + ")", uiStatusNoticeInfo
	case clientui.SessionSettingQuestions:
		return toggleSettingFeedback("Questions", *feedback.Questions, feedback.Changed), uiStatusNoticeInfo
	case clientui.SessionSettingAutoCompaction:
		return toggleSettingFeedback("Auto-compaction", *feedback.AutoCompaction, feedback.Changed), uiStatusNoticeInfo
	default:
		panic("validated Session setting feedback kind is exhaustive")
	}
}

func toggleSettingFeedback(label string, enabled, changed bool) string {
	if enabled {
		if changed {
			return label + " enabled"
		}
		return label + " already enabled"
	}
	if changed {
		return label + " disabled"
	}
	return label + " already disabled"
}
