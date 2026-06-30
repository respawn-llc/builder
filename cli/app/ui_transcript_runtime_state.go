package app

import (
	"strings"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcript"
)

func (m *uiModel) invalidateTransientTranscriptState() {
	if m == nil {
		return
	}
	m.clearDeferredCommittedTail("invalidate_transient")
	hadTransient := false
	committed := make([]tui.TranscriptEntry, 0, len(m.transcriptEntries))
	for _, entry := range m.transcriptEntries {
		if entry.Transient && !entry.Committed {
			hadTransient = true
			continue
		}
		committed = append(committed, entry)
	}
	if hadTransient {
		m.transcriptEntries = committed
		m.refreshRollbackCandidates()
	}
	m.transcriptLiveDirty = false
	m.reasoningLiveDirty = false
	m.sawAssistantDelta = false
	if m.detailTranscript.loaded {
		m.detailTranscript.ongoing = ""
		m.detailTranscript.ongoingError = ""
	}
	if !hadTransient && strings.TrimSpace(m.view.OngoingStreamingText()) == "" && strings.TrimSpace(m.view.OngoingErrorText()) == "" {
		return
	}
	m.forwardToView(tui.ClearStreamingReasoningMsg{})
	page := m.localRuntimeTranscript()
	baseOffset := m.transcriptBaseOffset
	totalEntries := m.transcriptTotalEntries
	if m.view.Mode() == tui.ModeDetail && m.detailTranscript.loaded {
		page = m.detailTranscript.page()
		baseOffset = m.detailTranscript.offset
		totalEntries = m.detailTranscript.totalEntries
	}
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   baseOffset,
		TotalEntries: totalEntries,
		Entries:      transcriptEntriesFromPage(page),
		Ongoing:      "",
		OngoingError: "",
	})
}

func shouldReplaceLoadedTransientEntriesWithCommittedAppend(m *uiModel, entries []tui.TranscriptEntry) bool {
	if m == nil || m.view.Mode() != tui.ModeOngoing || len(entries) == 0 {
		return false
	}
	loaded := m.view.LoadedTranscriptEntries()
	if len(loaded) == 0 {
		return false
	}
	for _, loadedEntry := range loaded {
		if !loadedEntry.Transient || loadedEntry.Committed {
			continue
		}
		for _, committedEntry := range entries {
			if committedEntry.Transient || !committedEntry.Committed {
				continue
			}
			if transcript.EntryPayloadEqual(transcriptPayloadFromTUIEntry(loadedEntry), transcriptPayloadFromTUIEntry(committedEntry)) {
				return true
			}
		}
	}
	return false
}

func (m *uiModel) deferProjectedCommittedTail(evt clientui.Event) {
	if m == nil {
		return
	}
	reduction := reduceDeferredCommittedTailDefer(newDeferredCommittedTailState(deferredCommittedTailSnapshotFromModel(m)), evt)
	if !reduction.shouldDefer {
		return
	}
	m.deferredCommittedTail = append(m.deferredCommittedTail, reduction.tail)
	m.transcriptRevision = reduction.revisionAfter
	m.transcriptTotalEntries = reduction.totalEntriesAfter
	m.logDeferredCommittedTailDeferDiag(evt, reduction)
}

func (m *uiModel) clearDeferredCommittedTail(reason string) {
	if m == nil {
		return
	}
	m.logDeferredCommittedTailClearDiag(reason)
	m.deferredCommittedTail = nil
}

func (m *uiModel) beginCommittedTranscriptContinuityRecovery() {
	if m == nil {
		return
	}
	m.logCommittedTranscriptContinuityRecoveryStartDiag()
	m.invalidateTransientTranscriptState()
}

func shouldClearAssistantStreamForCommittedAssistantEvent(evt clientui.Event, activeStream string) bool {
	if evt.Kind != clientui.EventAssistantMessage {
		return false
	}
	trimmedActiveStream := strings.TrimSpace(activeStream)
	for _, entry := range evt.TranscriptEntries {
		if isFinalAssistantProjectedEntry(entry) {
			return true
		}
		if trimmedActiveStream != "" &&
			tui.TranscriptRoleFromWire(entry.Role) == tui.TranscriptRoleAssistant &&
			strings.TrimSpace(entry.Text) == trimmedActiveStream {
			return true
		}
	}
	return false
}

func shouldClearAssistantStreamForCommittedTranscriptEntries(entries []tui.TranscriptEntry, activeStream string) bool {
	trimmedActiveStream := strings.TrimSpace(activeStream)
	for _, entry := range entries {
		if entry.Role != tui.TranscriptRoleAssistant || entry.Transient && !entry.Committed {
			continue
		}
		if isFinalAssistantTranscriptEntry(entry) {
			return true
		}
		if trimmedActiveStream != "" && strings.TrimSpace(entry.Text) == trimmedActiveStream {
			return true
		}
	}
	return false
}

func isFinalAssistantProjectedEntry(entry clientui.ChatEntry) bool {
	if tui.TranscriptRoleFromWire(entry.Role) != tui.TranscriptRoleAssistant {
		return false
	}
	phase := strings.TrimSpace(entry.Phase)
	return phase == "" || phase == string(clientui.MessagePhaseFinal)
}

func isFinalAssistantTranscriptEntry(entry tui.TranscriptEntry) bool {
	if entry.Role != tui.TranscriptRoleAssistant {
		return false
	}
	phase := strings.TrimSpace(string(entry.Phase))
	return phase == "" || phase == string(clientui.MessagePhaseFinal)
}

func (m *uiModel) clearAssistantStreamForCommittedAppend() {
	if m == nil {
		return
	}
	m.sawAssistantDelta = false
	m.nativeAssistantStreamIncomplete = false
	m.forwardToView(tui.ClearOngoingAssistantMsg{})
}

func skippedAssistantCommitMatchesActiveLiveStream(m *uiModel, evt clientui.Event) bool {
	if m == nil || strings.TrimSpace(m.view.OngoingStreamingText()) == "" {
		return false
	}
	assistantText := ""
	for _, entry := range evt.TranscriptEntries {
		if tui.TranscriptRoleFromWire(entry.Role) != tui.TranscriptRoleAssistant {
			continue
		}
		assistantText = strings.TrimSpace(entry.Text)
		break
	}
	if assistantText == "" || assistantText != strings.TrimSpace(m.view.OngoingStreamingText()) {
		return false
	}
	committedEntries := committedTranscriptEntriesForApp(m.transcriptEntries)
	for idx := len(committedEntries) - 1; idx >= 0; idx-- {
		entry := committedEntries[idx]
		if entry.Role != tui.TranscriptRoleAssistant {
			continue
		}
		return strings.TrimSpace(entry.Text) == assistantText
	}
	return false
}

func shouldIgnoreStaleAssistantDelta(m *uiModel, evt clientui.Event, delta string) bool {
	if m == nil || evt.Kind != clientui.EventAssistantDelta {
		return false
	}
	if strings.TrimSpace(delta) == "" {
		return false
	}
	if m.isBusy() || m.isCompacting() || m.isReviewerRunning() {
		return false
	}
	if strings.TrimSpace(m.view.OngoingStreamingText()) != "" || m.sawAssistantDelta {
		return false
	}
	if stepID := strings.TrimSpace(evt.StepID); stepID != "" && stepID != strings.TrimSpace(m.lastCommittedAssistantStepID) {
		return false
	}
	committedEntries := committedTranscriptEntriesForApp(m.transcriptEntries)
	for idx := len(committedEntries) - 1; idx >= 0; idx-- {
		entry := committedEntries[idx]
		if entry.Role != tui.TranscriptRoleAssistant {
			continue
		}
		return strings.TrimSpace(entry.Text) == strings.TrimSpace(delta)
	}
	return false
}

func shouldPauseRuntimeEventsForHydration(m *uiModel) bool {
	if m == nil {
		return false
	}
	return strings.TrimSpace(m.view.OngoingStreamingText()) == "" && !m.sawAssistantDelta
}

func transcriptContainsToolCallID(entries []tui.TranscriptEntry, toolCallID string) bool {
	trimmed := strings.TrimSpace(toolCallID)
	if trimmed == "" {
		return false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.ToolCallID) == trimmed {
			return true
		}
	}
	return false
}

func transcriptContainsCommittedToolCallID(entries []tui.TranscriptEntry, toolCallID string) bool {
	trimmed := strings.TrimSpace(toolCallID)
	if trimmed == "" {
		return false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.ToolCallID) != trimmed {
			continue
		}
		if !entry.Transient || entry.Committed {
			return true
		}
	}
	return false
}

func shouldRecoverCommittedTranscriptFromConversationUpdate(m *uiModel, evt clientui.Event) bool {
	if evt.Kind != clientui.EventConversationUpdated {
		return false
	}
	if evt.RecoveryCause != clientui.TranscriptRecoveryCauseNone {
		return true
	}
	if !evt.CommittedTranscriptChanged {
		return false
	}
	if len(evt.TranscriptEntries) > 0 {
		return false
	}
	if evt.TranscriptRevision <= 0 && evt.CommittedEntryCount <= 0 {
		return true
	}
	if m == nil {
		return true
	}
	effectiveRevision, effectiveCommittedCount := committedTranscriptStateIncludingDeferredTail(m)
	return evt.TranscriptRevision != effectiveRevision || evt.CommittedEntryCount != effectiveCommittedCount
}

func committedTranscriptStateIncludingDeferredTail(m *uiModel) (int64, int) {
	if m == nil {
		return 0, 0
	}
	revision := m.transcriptRevision
	count := m.transcriptBaseOffset + len(committedTranscriptEntriesForApp(m.transcriptEntries))
	chainEnd := count
	for _, deferred := range m.deferredCommittedTail {
		if deferred.rangeStart != chainEnd {
			break
		}
		chainEnd = deferred.rangeEnd
		if deferred.revision > revision {
			revision = deferred.revision
		}
	}
	return revision, max(m.transcriptTotalEntries, chainEnd)
}
