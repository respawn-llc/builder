package app

import "core/shared/clientui"

func (m *uiModel) runtimeSessionView() clientui.RuntimeSessionView {
	m.checkTUIBlockingOperation("runtime session-view read", "SessionView/MainView")
	return m.runtimeMainView().Session
}

func (m *uiModel) localRuntimeSessionView() clientui.RuntimeSessionView {
	transcript := m.localRuntimeTranscript()
	committedEntryCount := max(m.transcriptTotalEntries, m.transcriptBaseOffset+len(transcript.Entries))
	return clientui.RuntimeSessionView{
		SessionID:             m.sessionID,
		SessionName:           m.sessionName,
		ConversationFreshness: m.conversationFreshness,
		Transcript: clientui.TranscriptMetadata{
			Revision:            transcript.Revision,
			CommittedEntryCount: committedEntryCount,
		},
		Chat: clientui.ChatSnapshot{
			Entries:           transcript.Entries,
			Streaming:         transcript.Streaming,
			StreamingMetadata: cloneClientAssistantStreamMetadata(transcript.StreamingMetadata),
		},
	}
}
