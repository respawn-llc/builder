package app

import (
	"strings"

	"core/cli/tui"
)

func projectedTranscriptEventSnapshotFromModel(m *uiModel) projectedTranscriptEventSnapshot {
	if m == nil {
		return projectedTranscriptEventSnapshot{}
	}
	liveAssistantText := projectedActiveAssistantStreamText(m)
	return projectedTranscriptEventSnapshot{
		entries:              m.transcriptEntries,
		baseOffset:           m.transcriptBaseOffset,
		revision:             m.transcriptRevision,
		hasRuntimeClient:     m.hasRuntimeClient(),
		busy:                 m.isBusy(),
		liveAssistantPending: strings.TrimSpace(liveAssistantText) != "" || m.sawAssistantDelta,
		liveAssistantText:    liveAssistantText,
	}
}

func projectedActiveAssistantStreamText(m *uiModel) string {
	if m == nil {
		return ""
	}
	values := []string{
		m.view.OngoingStreamingText(),
	}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
