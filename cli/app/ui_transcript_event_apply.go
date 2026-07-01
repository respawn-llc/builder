package app

import (
	"strconv"

	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (a uiRuntimeAdapter) applyProjectedTranscriptEntries(evt clientui.Event) (tea.Cmd, bool, bool, bool) {
	m := a.model
	if len(evt.TranscriptEntries) == 0 {
		return nil, false, false, false
	}
	incomingCount := len(evt.TranscriptEntries)
	state := newProjectedTranscriptEventState(projectedTranscriptEventSnapshotFromModel(m))
	if cmd, fatal := m.preflightNativeCommittedTranscriptEvent(state, evt); fatal {
		return cmd, false, false, true
	}
	reduction := reduceProjectedTranscriptEvent(state, evt)
	if reduction.decision == projectedTranscriptDecisionSkip && reduction.duplicateToolStarts {
		m.logTranscriptEventDiag("transcript.diag.client.append_entries", evt, map[string]string{
			"path":           "live_event",
			"incoming_count": strconv.Itoa(incomingCount),
			"reason":         reduction.skipReason,
			"applied_count":  "0",
		})
		return nil, false, false, false
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
		return nil, false, false, false
	case projectedTranscriptDecisionHydrate:
		if cmd, applied := a.applyActiveAssistantFinalizerGapAsRecentTail(evt); applied {
			m.logTranscriptEventDiag("transcript.diag.client.append_entries", evt, map[string]string{
				"path":           "live_event",
				"incoming_count": strconv.Itoa(incomingCount),
				"reason":         "active_finalizer_recent_tail",
				"divergence":     plan.divergence,
				"applied_count":  strconv.Itoa(len(evt.TranscriptEntries)),
			})
			return cmd, true, false, false
		}
		if m.nativeSurfaceConfigured() && m.nativeImmutableTranscriptWritten && reduction.hydrationCause == clientui.TranscriptRecoveryCauseNone {
			m.logNativeTranscriptInvariant("hydrate committed transcript", errNativeStableNonGapHydration, state, evt, reduction)
			if cmd, fatal := m.nativeInvariantViolationCmd("hydrate committed transcript", errNativeStableNonGapHydration); fatal {
				return cmd, false, false, true
			}
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
				return m.requestRuntimeTranscriptSyncForContinuityLoss(reduction.hydrationCause), false, true, false
			}
			return m.requestRuntimeCommittedGapSync(), false, true, false
		}
		return nil, false, false, false
	case projectedTranscriptDecisionDefer:
		m.deferProjectedCommittedTail(evt)
		m.logTranscriptEventDiag("transcript.diag.client.append_entries", evt, map[string]string{
			"path":           "live_event",
			"incoming_count": strconv.Itoa(incomingCount),
			"reason":         reduction.skipReason,
			"applied_count":  "0",
		})
		return nil, false, false, false
	}
	entries := plan.entries
	convertedEntries := make([]tui.TranscriptEntry, 0, len(entries))
	for _, entry := range entries {
		convertedEntries = append(convertedEntries, transcriptEntryFromProjectedEventEntry(evt, entry, reduction.projectedTransient, reduction.projectedCommitted))
	}
	nativeSurfaceConfigured := m.nativeSurfaceConfigured()
	nativeAssistantStreamText := m.activeAssistantStreamText()
	committedAppendClearsAssistantStream := shouldClearAssistantStreamForCommittedTranscriptEntries(convertedEntries, nativeAssistantStreamText)
	nativeAssistantStreamNeedsPrefixValidation := nativeSurfaceConfigured &&
		nativeAssistantStreamText != "" &&
		committedAppendClearsAssistantStream
	nativeAssistantStreamActive := nativeSurfaceConfigured &&
		m.nativeSurface.AssistantStreaming() &&
		committedAppendClearsAssistantStream
	if plan.mode == projectedTranscriptEntryPlanAppend && nativeAssistantStreamNeedsPrefixValidation {
		if _, err := planNativeAssistantStreamFinalizerEmission(convertedEntries, nativeAssistantStreamText); err != nil {
			m.logNativeTranscriptInvariant("finalize native assistant stream", err, state, evt, reduction)
			if cmd, fatal := m.nativeInvariantViolationCmd("finalize native assistant stream", err); fatal {
				return cmd, false, false, true
			}
		}
	}
	if nativeSurfaceConfigured && !m.nativeScratchHydrationPending && !m.nativeResizeRehydratePending() {
		m.ensureNativeStableSurfaceForCurrentGeometry()
	}
	m.transcriptLiveDirty = true
	startOffset := m.transcriptBaseOffset + plan.rangeStart
	if committedAppendClearsAssistantStream && !nativeAssistantStreamActive {
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
			Ongoing:      m.activeAssistantStreamText(),
			OngoingError: m.view.OngoingErrorText(),
		})
	}
	if m.detailTranscript.loaded && !allTranscriptEntriesTransient(convertedEntries) {
		m.detailTranscript.setKnownBounds(startOffset, m.transcriptTotalEntries)
		page := clientui.TranscriptPage{
			Revision:       m.transcriptRevision,
			HasMoreAbove:   startOffset > 0,
			Entries:        cloneChatEntries(entries),
			Streaming:      m.activeAssistantStreamText(),
			StreamingError: m.view.OngoingErrorText(),
		}
		m.detailTranscript.apply(page)
	}
	if plan.mode == projectedTranscriptEntryPlanReplace && showTransientInCurrentView {
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   m.transcriptBaseOffset,
			TotalEntries: m.transcriptTotalEntries,
			Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
			Ongoing:      m.activeAssistantStreamText(),
			OngoingError: m.view.OngoingErrorText(),
		})
	}
	if plan.mode == projectedTranscriptEntryPlanAppend && m.view.Mode() == tui.ModeDetail && !allTranscriptEntriesTransient(convertedEntries) && m.detailTranscript.loaded && m.view.TranscriptBaseOffset() == m.detailTranscript.offset {
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   m.detailTranscript.offset,
			TotalEntries: m.detailTranscript.totalEntries,
			Entries:      append([]tui.TranscriptEntry(nil), m.detailTranscript.entries...),
			Ongoing:      m.activeAssistantStreamText(),
			OngoingError: m.view.OngoingErrorText(),
		})
	}
	if showTransientInCurrentView {
		m.clearMirroredTransientStatus(convertedEntries)
	}
	if plan.mode == projectedTranscriptEntryPlanAppend && nativeSurfaceConfigured {
		nativeEntries := convertedEntries
		nativeRangeStart := plan.rangeStart
		if nativeAssistantStreamActive {
			var err error
			var skippedLeading int
			nativeEntries, skippedLeading, err = m.nativeCommittedEntriesAfterActiveAssistantFinalizer(convertedEntries, nativeAssistantStreamText)
			if err != nil {
				return m.nativeFatalSurfaceErrorCmd("finalize native assistant stream", err), true, false, true
			}
			nativeRangeStart += skippedLeading
		}
		committedNativeEntries := committedTranscriptEntriesForApp(nativeEntries)
		if len(committedNativeEntries) > 0 {
			prependDivider := m.nativePrependDividerBeforeRange(nativeRangeStart, committedNativeEntries)
			if m.nativeStableOutputReady() {
				if err := m.emitNativeCommittedEntries(committedNativeEntries, prependDivider); err != nil {
					return m.nativeSurfaceErrorCmd("steer committed transcript", err), true, false, false
				}
			} else if err := m.queueNativeEmission(nativePendingEmission{kind: nativePendingEmissionEntries, entries: cloneTUITranscriptEntries(committedNativeEntries), prependDivider: prependDivider}); err != nil {
				return m.nativeSurfaceErrorCmd("queue committed transcript", err), true, false, false
			}
			if m.nativeScratchHydrationPending {
				return m.requestRuntimeNativeScratchTranscriptSync(), true, true, false
			}
		}
	} else if plan.mode == projectedTranscriptEntryPlanReplace && nativeSurfaceConfigured && m.nativeImmutableTranscriptWritten && !allTranscriptEntriesTransient(convertedEntries) {
		if evt.RecoveryCause == clientui.TranscriptRecoveryCauseStreamGap {
			return m.requestRuntimeNativeScratchTranscriptSync(), true, true, false
		}
		if err := errNativeStableNonAppend; err != nil {
			m.logNativeTranscriptInvariant("steer committed transcript", err, state, evt, reduction)
			if cmd, fatal := m.nativeInvariantViolationCmd("steer committed transcript", err); fatal {
				return cmd, true, false, true
			}
		}
	}
	m.logProjectedTranscriptAppliedDiag(evt, plan, incomingCount, len(entries), startOffset, entries)
	return nil, true, false, false
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
	if shouldClearAssistantStreamForCommittedTranscriptEntries(entries, m.activeAssistantStreamText()) {
		m.clearAssistantStreamForCommittedAppend()
	}
	totalEntries := max(evt.CommittedEntryCount, start+len(evt.TranscriptEntries))
	m.transcriptTotalEntries = max(m.transcriptTotalEntries, totalEntries)
	page := clientui.TranscriptPage{
		Revision:       evt.TranscriptRevision,
		HasMoreAbove:   start > 0,
		Entries:        cloneChatEntries(evt.TranscriptEntries),
		Streaming:      m.activeAssistantStreamText(),
		StreamingError: m.view.OngoingErrorText(),
	}
	detailPinnedAwayFromTail := m.detailTranscript.loaded && m.detailTranscript.hasMoreBelow
	if detailPinnedAwayFromTail {
		return m.requestRuntimeCommittedGapRecentTailSync(), true
	}
	a.applyAuthoritativeRecentTailPage(page, entries, false)
	if m.detailTranscript.loaded {
		m.detailTranscript.setKnownBounds(start, totalEntries)
		m.detailTranscript.apply(page)
	}
	switch {
	case m.detailTranscript.loaded:
		detailPage := m.detailTranscript.page()
		detailPage.SessionID = page.SessionID
		detailPage.SessionName = page.SessionName
		detailPage.Revision = page.Revision
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   m.detailTranscript.offset,
			TotalEntries: m.detailTranscript.totalEntries,
			Entries:      transcriptEntriesFromPage(detailPage),
			Ongoing:      detailPage.Streaming,
			OngoingError: detailPage.StreamingError,
		})
	default:
		m.forwardToView(tui.SetConversationMsg{
			BaseOffset:   m.transcriptBaseOffset,
			TotalEntries: m.transcriptTotalEntries,
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
