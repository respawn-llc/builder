package app

import "core/cli/tui"

func (m *uiModel) nativeCommittedProjectionForEntries(entries []tui.TranscriptEntry) tui.TranscriptProjection {
	if m == nil {
		return tui.TranscriptProjection{}
	}
	committedEntries := committedTranscriptEntriesForApp(entries)
	state := m.view.TranscriptProjectionViewState()
	return tui.ProjectCommittedOngoingTranscript(
		committedEntries,
		state.Theme,
		state.ViewportWidth,
		m.transcriptBaseOffset,
		state.CompactDetail,
		state.SelectedEntry,
		state.SelectedEntryIsActive,
	)
}
