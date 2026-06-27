package app

import (
	"strconv"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (a uiRuntimeAdapter) applyProjectedTranscriptEntries(evt clientui.Event) (tea.Cmd, bool, bool) {
	m := a.model
	if len(evt.TranscriptEntries) == 0 {
		return nil, false, false
	}
	incomingCount := len(evt.TranscriptEntries)
	reduction := reduceProjectedTranscriptEvent(newProjectedTranscriptEventState(projectedTranscriptEventSnapshotFromModel(m)), evt)
	if reduction.decision == projectedTranscriptDecisionSkip && reduction.duplicateToolStarts {
		m.logTranscriptEventDiag("transcript.diag.client.append_entries", evt, map[string]string{
			"path":           "live_event",
			"incoming_count": strconv.Itoa(incomingCount),
			"reason":         reduction.skipReason,
			"applied_count":  "0",
		})
		return nil, false, false
	}
	plan := reduction.plan
	m.logProjectedTranscriptPlanDiag(evt, plan, incomingCount)
	switch reduction.decision {
	case projectedTranscriptDecisionSkip:
		if evt.CommittedTranscriptChanged {
			m.transcriptRevision = max(m.transcriptRevision, evt.TranscriptRevision)
			m.transcriptTotalEntries = max(m.transcriptTotalEntries, evt.CommittedEntryCount)
		}
		m.logTranscriptEventDiag("transcript.diag.client.append_entries", evt, map[string]string{
			"path":           "live_event",
			"incoming_count": strconv.Itoa(incomingCount),
			"reason":         reduction.skipReason,
			"applied_count":  "0",
		})
		return nil, false, false
	case projectedTranscriptDecisionHydrate:
		if cmd, applied := a.applyActiveAssistantFinalizerGapAsRecentTail(evt); applied {
			m.logTranscriptEventDiag("transcript.diag.client.append_entries", evt, map[string]string{
				"path":           "live_event",
				"incoming_count": strconv.Itoa(incomingCount),
				"reason":         "active_finalizer_recent_tail",
				"divergence":     plan.divergence,
				"applied_count":  strconv.Itoa(len(evt.TranscriptEntries)),
			})
			return cmd, true, false
		}
		m.beginCommittedTranscriptContinuityRecovery()
		m.logTranscriptEventDiag("transcript.diag.client.append_entries", evt, map[string]string{
			"path":           "live_event",
			"incoming_count": strconv.Itoa(incomingCount),
			"reason":         "requires_hydration",
			"divergence":     plan.divergence,
			"applied_count":  "0",
		})
		if m.hasRuntimeClient() {
			if reduction.hydrationCause != clientui.TranscriptRecoveryCauseNone {
				return m.requestRuntimeTranscriptSyncForContinuityLoss(reduction.hydrationCause), false, true
			}
			return m.requestRuntimeCommittedGapSync(), false, true
		}
		return nil, false, false
	case projectedTranscriptDecisionDefer:
		m.deferProjectedCommittedTail(evt)
		m.logTranscriptEventDiag("transcript.diag.client.append_entries", evt, map[string]string{
			"path":           "live_event",
			"incoming_count": strconv.Itoa(incomingCount),
			"reason":         reduction.skipReason,
			"applied_count":  "0",
		})
		return nil, false, false
	}
	entries := plan.entries
	previousNativeStableProjection := tui.TranscriptProjection{}
	nativeSurfaceConfigured := m.nativeSurfaceConfigured()
	nativeStableReady := nativeSurfaceConfigured && m.nativeSurface.initialized()
	if nativeSurfaceConfigured {
		previousNativeStableProjection = m.nativeCommittedProjectionForEntries(m.transcriptEntries)
	}
	m.transcriptLiveDirty = true
	startOffset := m.transcriptBaseOffset + plan.rangeStart
	convertedEntries := make([]tui.TranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		convertedEntries = append(convertedEntries, transcriptEntryFromProjectedChatEntry(entry, reduction.projectedTransient, reduction.projectedCommitted))
	}
	nativeAssistantStreamText := m.view.OngoingStreamingText()
	committedAppendClearsAssistantStream := shouldClearAssistantStreamForCommittedTranscriptEntries(convertedEntries, m.view.OngoingStreamingText())
	nativeAssistantStreamWasIncomplete := m.nativeAssistantStreamIncomplete
	nativeAssistantStreamActive := m.nativeSurfaceConfigured() &&
		m.nativeSurface.AssistantStreaming() &&
		committedAppendClearsAssistantStream
	if committedAppendClearsAssistantStream {
		m.clearAssistantStreamForCommittedAppend()
	}
	showTransientInCurrentView := m.view.Mode() != tui.ModeDetail || !allTranscriptEntriesTransient(convertedEntries)
	replaceLoadedTransientEntries := shouldReplaceLoadedTransientEntriesWithCommittedAppend(m, convertedEntries)
	if plan.mode == projectedTranscriptEntryPlanAppend {
		for _, transcriptEntry := range convertedEntries {
			m.transcriptEntries = append(m.transcriptEntries, transcriptEntry)
			if showTransientInCurrentView && !replaceLoadedTransientEntries {
				m.forwardToView(appendTranscriptMsgFromEntry(transcriptEntry))
			}
		}
	} else {
		prefix := append([]tui.TranscriptEntry(nil), m.transcriptEntries[:plan.rangeStart]...)
		suffix := append([]tui.TranscriptEntry(nil), m.transcriptEntries[plan.rangeEnd:]...)
		m.transcriptEntries = append(prefix, convertedEntries...)
		m.transcriptEntries = append(m.transcriptEntries, suffix...)
	}
	m.transcriptRevision = max(m.transcriptRevision, evt.TranscriptRevision)
	m.transcriptTotalEntries = max(m.transcriptTotalEntries, max(evt.CommittedEntryCount, m.transcriptBaseOffset+len(m.transcriptEntries)))
	m.refreshRollbackCandidates()
	if plan.mode == projectedTranscriptEntryPlanAppend && replaceLoadedTransientEntries {
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   m.transcriptBaseOffset,
			TotalEntries: m.transcriptTotalEntries,
			Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
			Ongoing:      m.view.OngoingStreamingText(),
			OngoingError: m.view.OngoingErrorText(),
		})
	}
	if m.detailTranscript.loaded && !allTranscriptEntriesTransient(convertedEntries) {
		page := clientui.TranscriptPage{
			Revision:            m.transcriptRevision,
			StartEntryCount:     startOffset,
			CommittedEntryCount: m.transcriptTotalEntries,
			Entries:             cloneChatEntries(entries),
			Streaming:           m.view.OngoingStreamingText(),
			StreamingError:      m.view.OngoingErrorText(),
		}
		m.detailTranscript.apply(page)
	}
	if plan.mode == projectedTranscriptEntryPlanReplace && showTransientInCurrentView {
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   m.transcriptBaseOffset,
			TotalEntries: m.transcriptTotalEntries,
			Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
			Ongoing:      m.view.OngoingStreamingText(),
			OngoingError: m.view.OngoingErrorText(),
		})
	}
	if plan.mode == projectedTranscriptEntryPlanAppend && m.view.Mode() == tui.ModeDetail && !allTranscriptEntriesTransient(convertedEntries) && m.detailTranscript.loaded && m.view.TranscriptBaseOffset() == m.detailTranscript.offset {
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   m.detailTranscript.offset,
			TotalEntries: m.detailTranscript.totalEntries,
			Entries:      append([]tui.TranscriptEntry(nil), m.detailTranscript.entries...),
			Ongoing:      m.view.OngoingStreamingText(),
			OngoingError: m.view.OngoingErrorText(),
		})
	}
	if showTransientInCurrentView {
		m.clearMirroredTransientStatus(convertedEntries)
	}
	if (plan.mode == projectedTranscriptEntryPlanAppend || plan.mode == projectedTranscriptEntryPlanReplace) && nativeSurfaceConfigured {
		currentNativeStableProjection := m.nativeCommittedProjectionForEntries(m.transcriptEntries)
		if err := m.deliverNativeStableProjectionChange(previousNativeStableProjection, currentNativeStableProjection, nativeStableReady, nativeAssistantStreamActive, nativeAssistantStreamWasIncomplete, nativeAssistantStreamText); err != nil {
			return m.nativeSurfaceErrorCmd("steer committed transcript", err), true, false
		}
	}
	m.logProjectedTranscriptAppliedDiag(evt, plan, incomingCount, len(entries), startOffset, entries)
	return nil, true, false
}

func (a uiRuntimeAdapter) applyActiveAssistantFinalizerGapAsRecentTail(evt clientui.Event) (tea.Cmd, bool) {
	m := a.model
	if m == nil || len(evt.TranscriptEntries) == 0 || !evt.CommittedTranscriptChanged {
		return nil, false
	}
	if m.view.Mode() != tui.ModeDetail {
		return nil, false
	}
	state := newProjectedTranscriptEventState(projectedTranscriptEventSnapshotFromModel(m))
	if !isAssistantStreamFinalizerEvent(state, evt) {
		return nil, false
	}
	start, _, ok := projectedTranscriptEventRange(evt, len(evt.TranscriptEntries))
	if !ok || start < 0 {
		return nil, false
	}
	entries := make([]tui.TranscriptEntry, 0, len(evt.TranscriptEntries))
	for _, entry := range evt.TranscriptEntries {
		entries = append(entries, transcriptEntryFromProjectedChatEntry(entry, false, evt.CommittedTranscriptChanged))
	}
	if shouldClearAssistantStreamForCommittedTranscriptEntries(entries, m.view.OngoingStreamingText()) {
		m.clearAssistantStreamForCommittedAppend()
	}
	page := clientui.TranscriptPage{
		Revision:            evt.TranscriptRevision,
		StartEntryCount:     start,
		CommittedEntryCount: max(evt.CommittedEntryCount, start+len(evt.TranscriptEntries)),
		Entries:             cloneChatEntries(evt.TranscriptEntries),
		Streaming:           m.view.OngoingStreamingText(),
		StreamingError:      m.view.OngoingErrorText(),
	}
	detailPinnedAwayFromTail := m.detailTranscript.loaded && m.detailTranscript.hasMoreBelow
	if detailPinnedAwayFromTail {
		return m.requestRuntimeCommittedGapRecentTailSync(), true
	}
	a.applyAuthoritativeRecentTailPage(page, entries, false)
	if m.detailTranscript.loaded {
		m.detailTranscript.apply(page)
	}
	switch {
	case m.detailTranscript.loaded:
		detailPage := m.detailTranscript.page()
		detailPage.SessionID = page.SessionID
		detailPage.SessionName = page.SessionName
		detailPage.Revision = page.Revision
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   detailPage.StartEntryCount,
			TotalEntries: detailPage.CommittedEntryCount,
			Entries:      transcriptEntriesFromPage(detailPage),
			Ongoing:      detailPage.Streaming,
			OngoingError: detailPage.StreamingError,
		})
	default:
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   page.StartEntryCount,
			TotalEntries: page.CommittedEntryCount,
			Entries:      entries,
			Ongoing:      page.Streaming,
			OngoingError: page.StreamingError,
		})
	}
	return nil, true
}

func (m *uiModel) clearMirroredTransientStatus(entries []tui.TranscriptEntry) {
	if m == nil || m.transientStatusNoticeID == "" {
		return
	}
	for _, entry := range entries {
		if entry.NoticeID != m.transientStatusNoticeID {
			continue
		}
		m.transientStatus = ""
		m.transientStatusKind = uiStatusNoticeNeutral
		m.transientStatusNoticeID = ""
		return
	}
}

func (m *uiModel) clearMirroredTransientStatusByNoticeID(noticeID string) {
	if m == nil || m.transientStatusNoticeID == "" || noticeID == "" {
		return
	}
	if noticeID != m.transientStatusNoticeID {
		return
	}
	m.transientStatus = ""
	m.transientStatusKind = uiStatusNoticeNeutral
	m.transientStatusNoticeID = ""
}
