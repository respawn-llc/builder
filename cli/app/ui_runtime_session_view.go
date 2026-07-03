package app

import "core/shared/clientui"

func (m *uiModel) runtimeSessionView() clientui.RuntimeSessionView {
	m.checkTUIBlockingOperation("runtime session-view read", "SessionView/MainView")
	return m.runtimeMainView().Session
}

func (m *uiModel) localRuntimeSessionView() clientui.RuntimeSessionView {
	return clientui.RuntimeSessionView{
		SessionID:             m.sessionID,
		SessionName:           m.sessionName,
		ConversationFreshness: m.conversationFreshness,
		Chat: clientui.ChatSnapshot{
			Entries:           clientEntriesFromTranscriptEntries(m.transcriptEntries),
			Streaming:         m.activeAssistantStreamSource,
			StreamingMetadata: cloneClientAssistantStreamMetadata(&clientui.AssistantStreamMetadata{StepID: m.activeAssistantStreamIdentity.StepID}),
		},
	}
}
