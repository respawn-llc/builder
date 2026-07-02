package app

import (
	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) transcriptRequestForCurrentMode() clientui.TranscriptPageRequest {
	if m.view.Mode() == tui.ModeDetail {
		return m.detailTranscript.requestedPageForDetailEntry()
	}
	return clientui.TranscriptPageRequest{}
}

func (m *uiModel) maybeRequestDetailTranscriptPage() tea.Cmd {
	if !m.hasRuntimeClient() || m.view.Mode() != tui.ModeDetail || m.runtimeTranscriptBusy {
		return nil
	}
	if !m.view.DetailMetricsResolved() {
		firstVisible, lastVisible, ok := m.view.DetailVisibleEntryRange()
		if !ok {
			return nil
		}
		if firstVisible <= m.view.TranscriptBaseOffset()+1 {
			if req, ok := m.detailTranscript.pageBefore(); ok {
				return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(req, true, runtimeTranscriptSyncCauseManualTranscriptRefresh, clientui.TranscriptRecoveryCauseNone)).cmd
			}
		}
		loadedLast := m.view.TranscriptBaseOffset() + m.view.LoadedTranscriptEntryCount() - 1
		if lastVisible >= loadedLast-1 {
			if req, ok := m.detailTranscript.pageAfter(); ok {
				return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(req, true, runtimeTranscriptSyncCauseManualTranscriptRefresh, clientui.TranscriptRecoveryCauseNone)).cmd
			}
		}
		return nil
	}
	if m.view.DetailScroll() <= uiDetailTranscriptEdgeLineMargin {
		if req, ok := m.detailTranscript.pageBefore(); ok {
			return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(req, true, runtimeTranscriptSyncCauseManualTranscriptRefresh, clientui.TranscriptRecoveryCauseNone)).cmd
		}
	}
	if m.view.DetailMaxScroll()-m.view.DetailScroll() <= uiDetailTranscriptEdgeLineMargin {
		if req, ok := m.detailTranscript.pageAfter(); ok {
			return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(req, true, runtimeTranscriptSyncCauseManualTranscriptRefresh, clientui.TranscriptRecoveryCauseNone)).cmd
		}
	}
	return nil
}

func (m *uiModel) primeDetailTranscriptFromCurrentTail() {
	if m.view.Mode() != tui.ModeDetail {
		return
	}
	page := m.currentDetailTailPage()
	m.detailTranscript.setKnownBounds(m.transcriptBaseOffset, m.transcriptTotalEntries)
	if m.detailTranscript.loaded {
		if m.shouldPreserveLoadedDetailWindowOnPrime(page) {
			m.detailTranscript.totalEntries = max(m.detailTranscript.totalEntries, m.transcriptTotalEntries)
			m.detailTranscript.ongoing = page.Streaming
			m.detailTranscript.ongoingMetadata = cloneClientAssistantStreamMetadata(page.StreamingMetadata)
			m.detailTranscript.ongoingError = page.StreamingError
			return
		}
		m.detailTranscript.syncTail(page)
		return
	}
	m.detailTranscript.replace(page)
}

func (m *uiModel) shouldPreserveLoadedDetailWindowOnPrime(page clientui.TranscriptPage) bool {
	return len(page.Entries) == 0 && len(m.transcriptEntries) == 0
}

func (m *uiModel) currentDetailTailPage() clientui.TranscriptPage {
	page := clientui.TranscriptPage{
		Streaming:         m.view.OngoingStreamingText(),
		StreamingMetadata: cloneClientAssistantStreamMetadata(m.activeAssistantStreamMetadata()),
		StreamingError:    m.view.OngoingErrorText(),
		HasMoreAbove:      m.transcriptBaseOffset > 0,
	}
	for _, entry := range committedTranscriptEntriesForApp(m.transcriptEntries) {
		page.Entries = append(page.Entries, clientui.ChatEntry{
			Visibility:        entry.Visibility,
			Role:              string(entry.Role),
			Text:              entry.Text,
			CondensedText:     entry.CondensedText,
			Phase:             string(entry.Phase),
			MessageType:       string(entry.MessageType),
			SourcePath:        entry.SourcePath,
			CompactLabel:      entry.CompactLabel,
			ToolResultSummary: entry.ToolResultSummary,
			ToolCallID:        entry.ToolCallID,
			ToolCall:          transcriptToolCallMetaClient(entry.ToolCall),
		})
	}
	if len(page.Entries) == 0 && len(m.transcriptEntries) > 0 {
		page.Entries = m.localRuntimeTranscript().Entries
	}
	return page
}
