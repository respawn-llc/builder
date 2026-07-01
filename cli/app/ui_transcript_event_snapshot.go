package app

import "core/cli/tui"

func projectedTranscriptEventSnapshotFromModel(m *uiModel) projectedTranscriptEventSnapshot {
	if m == nil {
		return projectedTranscriptEventSnapshot{}
	}
	liveAssistantText := projectedActiveAssistantStreamText(m)
	return projectedTranscriptEventSnapshot{
		entries:              m.transcriptEntries,
		baseOffset:           m.transcriptBaseOffset,
		revision:             m.transcriptRevision,
		totalEntries:         m.transcriptTotalEntries,
		authoritativeTail:    !m.transcriptLiveDirty,
		hasRuntimeClient:     m.hasRuntimeClient(),
		busy:                 m.isBusy(),
		liveAssistantPending: m.activeAssistantStreamPending(),
		liveAssistantText:    liveAssistantText,
		liveAssistantStepID:  m.activeAssistantStreamStepID,
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
		committedEntries: committedTranscriptEntriesForDeferredTail(m.transcriptEntries),
		baseOffset:       m.transcriptBaseOffset,
		revision:         m.transcriptRevision,
		totalEntries:     m.transcriptTotalEntries,
	}
}

func committedTranscriptEntriesForDeferredTail(entries []tui.TranscriptEntry) []tui.TranscriptEntry {
	if len(entries) == 0 {
		return nil
	}
	committed := make([]tui.TranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Transient && !entry.Committed {
			continue
		}
		committed = append(committed, entry)
	}
	return committed
}
