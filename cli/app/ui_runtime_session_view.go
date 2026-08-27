package app

import (
	"core/shared/clientui"
	"core/shared/textutil"
)

func (m *uiModel) localRuntimeSessionView() clientui.RuntimeSessionView {
	return clientui.RuntimeSessionView{
		SessionID:             m.sessionID,
		SessionName:           m.sessionName,
		AgentRole:             textutil.Pointer(m.agentRole),
		ConversationFreshness: m.conversationFreshness,
	}
}
