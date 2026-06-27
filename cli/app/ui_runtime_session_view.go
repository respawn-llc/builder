package app

import "core/shared/clientui"

func (m *uiModel) runtimeSessionView() clientui.RuntimeSessionView {
	m.checkTUIBlockingOperation("runtime session-view read", "SessionView/MainView")
	return m.runtimeMainView().Session
}

func (m *uiModel) localRuntimeSessionView() clientui.RuntimeSessionView {
	transcript := m.localRuntimeTranscript()
	return clientui.RuntimeSessionView{
		SessionID:             m.sessionID,
		SessionName:           m.sessionName,
		ConversationFreshness: m.conversationFreshness,
		Transcript: clientui.TranscriptMetadata{
			Revision:            transcript.Revision,
			CommittedEntryCount: transcript.CommittedEntryCount,
		},
		Chat: clientui.ChatSnapshot{
			Entries:   transcript.Entries,
			Streaming: transcript.Streaming,
		},
	}
}
