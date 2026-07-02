package app

func projectedTranscriptEventSnapshotFromModel(m *uiModel) projectedTranscriptEventSnapshot {
	if m == nil {
		return projectedTranscriptEventSnapshot{}
	}
	liveAssistantText := projectedActiveAssistantStreamText(m)
	return projectedTranscriptEventSnapshot{
		entries:               m.transcriptEntries,
		baseOffset:            m.transcriptBaseOffset,
		revision:              m.transcriptRevision,
		totalEntries:          m.transcriptTotalEntries,
		authoritativeTail:     !m.transcriptLiveDirty,
		hasRuntimeClient:      m.hasRuntimeClient(),
		busy:                  m.isBusy(),
		liveAssistantPending:  m.activeAssistantStreamPending(),
		liveAssistantText:     liveAssistantText,
		liveAssistantStepID:   m.activeAssistantStreamStepID(),
		liveAssistantIdentity: m.activeAssistantStreamIdentity,
	}
}

func projectedActiveAssistantStreamText(m *uiModel) string {
	if m == nil {
		return ""
	}
	return m.activeAssistantStreamText()
}

func deferredCommittedTailSnapshotFromModel(m *uiModel) deferredCommittedTailSnapshot {
	if m == nil {
		return deferredCommittedTailSnapshot{}
	}
	return deferredCommittedTailSnapshot{
		tails:            m.deferredCommittedTail,
		committedEntries: ownedCommittedTranscriptEntriesForApp(m.transcriptEntries),
		baseOffset:       m.transcriptBaseOffset,
		revision:         m.transcriptRevision,
		totalEntries:     m.transcriptTotalEntries,
	}
}
