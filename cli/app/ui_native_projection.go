package app

import "core/cli/tui"

func (m *uiModel) nativeCommittedProjectionForEntries(entries []tui.TranscriptEntry) tui.TranscriptProjection {
	if m == nil {
		return tui.TranscriptProjection{}
	}
	state := m.view.TranscriptProjectionViewState()
	return tui.ProjectCommittedOngoingTranscript(
		entries,
		state.Theme,
		state.ViewportWidth,
		m.transcriptBaseOffset,
		state.CompactDetail,
		state.SelectedEntry,
		state.SelectedEntryIsActive,
	)
}
