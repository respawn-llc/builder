package app

import "core/cli/tui"

func (m *uiModel) nativeCommittedProjectionForEntries(entries []tui.TranscriptEntry) tui.TranscriptProjection {
	if m == nil {
		return tui.TranscriptProjection{}
	}
	committedEntries := ownedCommittedTranscriptEntriesForApp(entries)
	state := m.view.TranscriptProjectionViewState()
	return tui.ProjectNativeCommittedOngoingTranscript(
		committedEntries,
		state.Theme,
		state.ViewportWidth,
		m.transcriptBaseOffset,
		state.CompactDetail,
		state.SelectedEntry,
		state.SelectedEntryIsActive,
	)
}
