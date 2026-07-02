package app

import (
	"strings"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcript"
)

type uiAssistantStreamIdentity struct {
	stepID   string
	frontier *uiAssistantStreamFrontier
	hydrated bool
}

type uiAssistantStreamFrontier struct {
	baseRevision            int64
	baseCommittedEntryCount int
}

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
	m.clearActiveAssistantStreamSource()
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

func (m *uiModel) dropTrailingTransientTranscriptEntriesForCommittedEvent(evt clientui.Event) {
	if m == nil || !evt.CommittedTranscriptChanged || len(evt.TranscriptEntries) == 0 {
		return
	}
	eventStart, _, ok := projectedTranscriptEventRange(evt, len(evt.TranscriptEntries))
	if !ok {
		return
	}
	prefixLen, ok := trailingTransientTranscriptPrefixLen(m.transcriptEntries)
	if !ok || prefixLen == len(m.transcriptEntries) {
		return
	}
	if eventStart != m.transcriptBaseOffset+prefixLen {
		return
	}
	m.transcriptEntries = append([]tui.TranscriptEntry(nil), m.transcriptEntries[:prefixLen]...)
	m.refreshRollbackCandidates()
	m.forwardToView(tui.SetConversationMsg{
		BaseOffset:   m.transcriptBaseOffset,
		TotalEntries: m.transcriptTotalEntries,
		Entries:      append([]tui.TranscriptEntry(nil), m.transcriptEntries...),
		Ongoing:      m.activeAssistantStreamText(),
		OngoingError: m.view.OngoingErrorText(),
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

func (m *uiModel) appendActiveAssistantStreamDelta(stepID string, delta string, metadata *clientui.AssistantStreamMetadata) {
	if m == nil {
		return
	}
	nextIdentity := assistantStreamIdentityFromMetadata(stepID, metadata)
	if nextIdentity.stepID != "" {
		if m.activeAssistantStreamIdentity.stepID != "" && m.activeAssistantStreamIdentity.stepID != nextIdentity.stepID {
			m.activeAssistantStreamSource = ""
		}
		if m.activeAssistantStreamIdentity.sameStepDifferentFrontier(nextIdentity) {
			m.activeAssistantStreamSource = ""
		}
		m.activeAssistantStreamIdentity = m.activeAssistantStreamIdentity.merge(nextIdentity)
	}
	m.activeAssistantStreamSource += delta
}

func (m *uiModel) clearActiveAssistantStreamSource() {
	if m == nil {
		return
	}
	m.activeAssistantStreamSource = ""
	m.activeAssistantStreamIdentity = uiAssistantStreamIdentity{}
}

func (m *uiModel) refreshActiveAssistantStreamFromAuthoritativePageStreaming(streaming string, metadata *clientui.AssistantStreamMetadata) {
	if m == nil {
		return
	}
	if strings.TrimSpace(streaming) == "" {
		return
	}
	m.activeAssistantStreamSource = streaming
	m.activeAssistantStreamIdentity = assistantStreamIdentityFromMetadata("", metadata)
	m.activeAssistantStreamIdentity.hydrated = true
}

func (m *uiModel) activeAssistantStreamText() string {
	if m == nil {
		return ""
	}
	if m.activeAssistantStreamSource != "" {
		return m.activeAssistantStreamSource
	}
	return m.view.OngoingStreamingText()
}

func (m *uiModel) activeAssistantStreamPending() bool {
	if m == nil {
		return false
	}
	if strings.TrimSpace(m.activeAssistantStreamText()) != "" || m.sawAssistantDelta {
		return true
	}
	return m.nativeSurfaceConfigured() && m.nativeSurface.AssistantStreaming()
}

func (m *uiModel) activeAssistantStreamStepID() string {
	if m == nil {
		return ""
	}
	return m.activeAssistantStreamIdentity.stepID
}

func (m *uiModel) activeAssistantStreamMetadata() *clientui.AssistantStreamMetadata {
	if m == nil {
		return nil
	}
	return m.activeAssistantStreamIdentity.metadata()
}

func (m *uiModel) shouldApplyAssistantStreamReset(stepID string, metadata *clientui.AssistantStreamMetadata) bool {
	if m == nil {
		return false
	}
	active := m.activeAssistantStreamIdentity
	if active.stepID == "" && active.frontier == nil {
		return true
	}
	incoming := assistantStreamIdentityFromMetadata(stepID, metadata)
	if incoming.stepID != "" && active.stepID != "" && incoming.stepID != active.stepID {
		return false
	}
	if active.frontier == nil {
		return true
	}
	if incoming.frontier == nil {
		return false
	}
	return active.stepID == incoming.stepID &&
		active.frontier.baseRevision == incoming.frontier.baseRevision &&
		active.frontier.baseCommittedEntryCount == incoming.frontier.baseCommittedEntryCount
}

func (m *uiModel) shouldFailBeforeAssistantStreamDeltaWithoutMetadata(stepID string, metadata *clientui.AssistantStreamMetadata) bool {
	if m == nil || !m.nativeSurfaceConfigured() {
		return false
	}
	if strings.TrimSpace(stepID) == "" {
		return false
	}
	incoming := assistantStreamIdentityFromMetadata(stepID, metadata)
	return incoming.frontier == nil
}

func (m *uiModel) shouldRecoverBeforeAssistantStreamDelta(stepID string, metadata *clientui.AssistantStreamMetadata) bool {
	if m == nil || metadata == nil {
		return false
	}
	incoming := assistantStreamIdentityFromMetadata(stepID, metadata)
	if incoming.frontier == nil {
		return false
	}
	_, ownedEnd := committedTranscriptOwnedTailIncludingDeferredTail(m)
	if incoming.frontier.baseCommittedEntryCount > ownedEnd {
		return true
	}
	active := m.activeAssistantStreamIdentity
	if active.frontier == nil || active.stepID == "" || incoming.stepID == "" {
		return false
	}
	if active.stepID != incoming.stepID || !assistantStreamFrontiersEqual(active.frontier, incoming.frontier) {
		return m.nativeSurfaceConfigured() && m.nativeSurface.AssistantStreaming() && active.hydrated
	}
	return false
}

func (m *uiModel) shouldFailBeforeAssistantStreamDelta(stepID string, metadata *clientui.AssistantStreamMetadata) bool {
	if m == nil || metadata == nil || !m.nativeSurfaceConfigured() || !m.nativeSurface.AssistantStreaming() {
		return false
	}
	incoming := assistantStreamIdentityFromMetadata(stepID, metadata)
	active := m.activeAssistantStreamIdentity
	if active.frontier == nil || incoming.frontier == nil || active.stepID == "" || incoming.stepID == "" {
		return false
	}
	if active.hydrated {
		return false
	}
	if active.stepID != incoming.stepID {
		return false
	}
	return !assistantStreamFrontiersEqual(active.frontier, incoming.frontier)
}

func assistantStreamIdentityFromMetadata(stepID string, metadata *clientui.AssistantStreamMetadata) uiAssistantStreamIdentity {
	identity := uiAssistantStreamIdentity{stepID: strings.TrimSpace(stepID)}
	if metadata == nil {
		return identity
	}
	metadataStepID := strings.TrimSpace(metadata.StepID)
	if metadataStepID != "" {
		identity.stepID = metadataStepID
	}
	if identity.stepID == "" {
		return identity
	}
	identity.frontier = &uiAssistantStreamFrontier{
		baseRevision:            metadata.BaseRevision,
		baseCommittedEntryCount: metadata.BaseCommittedEntryCount,
	}
	return identity
}

func (identity uiAssistantStreamIdentity) merge(next uiAssistantStreamIdentity) uiAssistantStreamIdentity {
	if next.stepID == "" {
		return identity
	}
	merged := next
	if next.frontier == nil {
		merged.frontier = identity.frontier
	}
	if identity.hydrated && identity.stepID == merged.stepID && assistantStreamFrontiersEqual(identity.frontier, merged.frontier) {
		merged.hydrated = true
	}
	return merged
}

func (identity uiAssistantStreamIdentity) sameStepDifferentFrontier(next uiAssistantStreamIdentity) bool {
	if identity.stepID == "" || next.stepID == "" || identity.stepID != next.stepID {
		return false
	}
	if identity.frontier == nil || next.frontier == nil {
		return false
	}
	return identity.frontier.baseRevision != next.frontier.baseRevision ||
		identity.frontier.baseCommittedEntryCount != next.frontier.baseCommittedEntryCount
}

func (identity uiAssistantStreamIdentity) metadata() *clientui.AssistantStreamMetadata {
	if identity.stepID == "" || identity.frontier == nil {
		return nil
	}
	return &clientui.AssistantStreamMetadata{
		StepID:                  identity.stepID,
		BaseRevision:            identity.frontier.baseRevision,
		BaseCommittedEntryCount: identity.frontier.baseCommittedEntryCount,
	}
}

func assistantStreamFrontiersEqual(left, right *uiAssistantStreamFrontier) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.baseRevision == right.baseRevision &&
		left.baseCommittedEntryCount == right.baseCommittedEntryCount
}

func shouldClearAssistantStreamForCommittedAssistantEvent(evt clientui.Event, activeStream string) bool {
	if evt.Kind != clientui.EventAssistantMessage {
		return false
	}
	for _, entry := range evt.TranscriptEntries {
		if isFinalAssistantProjectedEntry(entry) {
			return true
		}
		if activeStream != "" &&
			tui.TranscriptRoleFromWire(entry.Role) == tui.TranscriptRoleAssistant &&
			entry.Text == activeStream {
			return true
		}
	}
	return false
}

func shouldClearAssistantStreamForCommittedTranscriptEntries(entries []tui.TranscriptEntry, activeStream string) bool {
	for _, entry := range entries {
		if entry.Role != tui.TranscriptRoleAssistant || entry.Transient && !entry.Committed {
			continue
		}
		if isFinalAssistantTranscriptEntry(entry) {
			return true
		}
		if activeStream != "" && entry.Text == activeStream {
			return true
		}
	}
	return false
}

func shouldDiscardWhitespaceAssistantStreamForCommittedTranscriptEntries(state projectedTranscriptEventState, evt clientui.Event, entries []tui.TranscriptEntry) bool {
	if !evt.CommittedTranscriptChanged || strings.TrimSpace(state.liveAssistantText) != "" || state.liveAssistantText == "" {
		return false
	}
	if !activeAssistantStepMatchesEvent(state, evt) {
		return false
	}
	hasCommittedNonAssistant := false
	for _, entry := range entries {
		if entry.Transient && !entry.Committed {
			continue
		}
		if entry.Role == tui.TranscriptRoleAssistant {
			return false
		}
		hasCommittedNonAssistant = true
	}
	return hasCommittedNonAssistant
}

func isFinalAssistantProjectedEntry(entry clientui.ChatEntry) bool {
	if tui.TranscriptRoleFromWire(entry.Role) != tui.TranscriptRoleAssistant {
		return false
	}
	phase := strings.TrimSpace(entry.Phase)
	return phase == "" || phase == string(clientui.MessagePhaseFinal) || phase == string(clientui.MessagePhaseCommentary)
}

func isFinalAssistantTranscriptEntry(entry tui.TranscriptEntry) bool {
	if entry.Role != tui.TranscriptRoleAssistant {
		return false
	}
	phase := strings.TrimSpace(string(entry.Phase))
	return phase == "" || phase == string(clientui.MessagePhaseFinal) || phase == string(clientui.MessagePhaseCommentary)
}

func (m *uiModel) clearAssistantStreamForCommittedAppend() {
	if m == nil {
		return
	}
	m.sawAssistantDelta = false
	m.nativeAssistantStreamIncomplete = false
	m.clearActiveAssistantStreamSource()
	m.forwardToView(tui.ClearOngoingAssistantMsg{})
}

func skippedAssistantCommitMatchesActiveLiveStream(m *uiModel, evt clientui.Event) bool {
	if m == nil || m.activeAssistantStreamText() == "" {
		return false
	}
	assistantText := ""
	for _, entry := range evt.TranscriptEntries {
		if tui.TranscriptRoleFromWire(entry.Role) != tui.TranscriptRoleAssistant {
			continue
		}
		assistantText = entry.Text
		break
	}
	if assistantText == "" || assistantText != m.activeAssistantStreamText() {
		return false
	}
	committedEntries := committedTranscriptEntriesForApp(m.transcriptEntries)
	for idx := len(committedEntries) - 1; idx >= 0; idx-- {
		entry := committedEntries[idx]
		if entry.Role != tui.TranscriptRoleAssistant {
			continue
		}
		return entry.Text == assistantText
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
	revision, chainEnd := committedTranscriptOwnedTailIncludingDeferredTail(m)
	return revision, max(m.transcriptTotalEntries, chainEnd)
}

func committedTranscriptOwnedTailIncludingDeferredTail(m *uiModel) (int64, int) {
	if m == nil {
		return 0, 0
	}
	revision := m.transcriptRevision
	count := m.transcriptBaseOffset + len(ownedCommittedTranscriptEntriesForApp(m.transcriptEntries))
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
	return revision, chainEnd
}
