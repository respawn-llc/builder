package app

import "core/shared/clientui"

func (m *uiModel) localRuntimeSessionView() clientui.RuntimeSessionView {
	return clientui.RuntimeSessionView{
		SessionID:             m.sessionID,
		SessionName:           m.sessionName,
		ConversationFreshness: m.conversationFreshness,
	}
}
