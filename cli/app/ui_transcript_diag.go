package app

import (
	"strconv"
	"strings"

	"core/shared/clientui"
	"core/shared/transcriptdiag"
)

func (m *uiModel) transcriptModeLabel() string {
	if m == nil {
		return "ongoing"
	}
	return string(m.view.Mode())
}

func (m *uiModel) logTranscriptEventDiag(name string, evt clientui.Event, extra map[string]string) {
	if m == nil || !m.transcriptDiagnosticsEnabled() {
		return
	}
	fields := map[string]string{
		"session_id":            strings.TrimSpace(m.sessionID),
		"mode":                  m.transcriptModeLabel(),
		"current_base_offset":   strconv.Itoa(m.transcriptBaseOffset),
		"current_entries_count": strconv.Itoa(len(m.transcriptEntries)),
		"current_total_entries": strconv.Itoa(m.transcriptTotalEntries),
		"busy":                  strconv.FormatBool(m.isBusy()),
		"compacting":            strconv.FormatBool(m.isCompacting()),
		"saw_assistant_delta":   strconv.FormatBool(m.sawAssistantDelta),
		"kind":                  string(evt.Kind),
		"step_id":               strings.TrimSpace(evt.StepID),
		"event_digest":          transcriptdiag.EventDigest(evt),
		"assistant_delta_chars": strconv.Itoa(len(evt.AssistantDelta)),
	}
	fields = transcriptdiag.AddEntriesFields(fields, evt.TranscriptEntries)
	if evt.ReasoningDelta != nil {
		fields["reasoning_key"] = strings.TrimSpace(evt.ReasoningDelta.Key)
		fields["reasoning_chars"] = strconv.Itoa(len(evt.ReasoningDelta.Text))
	}
	for key, value := range extra {
		fields[key] = value
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine(name, fields))
}

func (m *uiModel) logTranscriptPageDiag(name string, req clientui.TranscriptPageRequest, page clientui.TranscriptPage, extra map[string]string) {
	if m == nil || !m.transcriptDiagnosticsEnabled() {
		return
	}
	fields := map[string]string{
		"session_id":            firstNonEmpty(page.SessionID, m.sessionID),
		"mode":                  m.transcriptModeLabel(),
		"current_revision":      strconv.FormatInt(m.transcriptRevision, 10),
		"current_base_offset":   strconv.Itoa(m.transcriptBaseOffset),
		"current_entries_count": strconv.Itoa(len(m.transcriptEntries)),
		"current_total_entries": strconv.Itoa(m.transcriptTotalEntries),
		"transcript_live_dirty": strconv.FormatBool(m.transcriptLiveDirty),
		"reasoning_live_dirty":  strconv.FormatBool(m.reasoningLiveDirty),
		"busy":                  strconv.FormatBool(m.isBusy()),
		"compacting":            strconv.FormatBool(m.isCompacting()),
		"saw_assistant_delta":   strconv.FormatBool(m.sawAssistantDelta),
		"view_ongoing_chars":    strconv.Itoa(len(m.view.OngoingStreamingText())),
		"view_ongoing_error":    strconv.Itoa(len(m.view.OngoingErrorText())),
	}
	for key, value := range transcriptdiag.RequestFields(req) {
		fields[key] = value
	}
	fields = transcriptdiag.AddPageFields(fields, page)
	for key, value := range extra {
		fields[key] = value
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine(name, fields))
}

func (m *uiModel) logProjectedTranscriptPlanDiag(evt clientui.Event, plan projectedTranscriptEntryPlan, incomingCount int) {
	if m == nil || !m.transcriptDiagnosticsEnabled() {
		return
	}
	eventEnd := evt.CommittedEntryCount
	eventStart := eventEnd - incomingCount
	if start, _, ok := projectedTranscriptEventRange(evt, incomingCount); ok {
		eventStart = start
		eventEnd = start + incomingCount
	}
	fields := map[string]string{
		"session_id":            strings.TrimSpace(m.sessionID),
		"mode":                  m.transcriptModeLabel(),
		"kind":                  string(evt.Kind),
		"plan":                  plan.mode.label(),
		"divergence":            plan.divergence,
		"range_start":           strconv.Itoa(plan.rangeStart),
		"range_end":             strconv.Itoa(plan.rangeEnd),
		"incoming_count":        strconv.Itoa(incomingCount),
		"event_revision":        strconv.FormatInt(evt.TranscriptRevision, 10),
		"event_committed_count": strconv.Itoa(evt.CommittedEntryCount),
		"event_start":           strconv.Itoa(eventStart),
		"event_end":             strconv.Itoa(eventEnd),
		"current_revision":      strconv.FormatInt(m.transcriptRevision, 10),
		"current_base_offset":   strconv.Itoa(m.transcriptBaseOffset),
		"current_entries_count": strconv.Itoa(len(m.transcriptEntries)),
		"current_total_entries": strconv.Itoa(m.transcriptTotalEntries),
		"transcript_live_dirty": strconv.FormatBool(m.transcriptLiveDirty),
		"reasoning_live_dirty":  strconv.FormatBool(m.reasoningLiveDirty),
		"busy":                  strconv.FormatBool(m.isBusy()),
		"compacting":            strconv.FormatBool(m.isCompacting()),
		"saw_assistant_delta":   strconv.FormatBool(m.sawAssistantDelta),
		"view_ongoing_chars":    strconv.Itoa(len(m.view.OngoingStreamingText())),
		"view_ongoing_error":    strconv.Itoa(len(m.view.OngoingErrorText())),
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.projected_plan", fields))
}

func (m *uiModel) logProjectedTranscriptAppliedDiag(evt clientui.Event, plan projectedTranscriptEntryPlan, incomingCount int, appliedCount int, startOffset int, entries []clientui.ChatEntry) {
	if m == nil || !m.transcriptDiagnosticsEnabled() {
		return
	}
	fields := map[string]string{
		"session_id":            strings.TrimSpace(m.sessionID),
		"mode":                  m.transcriptModeLabel(),
		"path":                  "live_event",
		"incoming_count":        strconv.Itoa(incomingCount),
		"applied_count":         strconv.Itoa(appliedCount),
		"start_offset":          strconv.Itoa(startOffset),
		"entries_digest":        transcriptdiag.EntriesDigest(entries),
		"reconcile_mode":        plan.mode.label(),
		"event_revision":        strconv.FormatInt(evt.TranscriptRevision, 10),
		"event_committed_count": strconv.Itoa(evt.CommittedEntryCount),
		"transcript_revision":   strconv.FormatInt(m.transcriptRevision, 10),
		"transcript_total":      strconv.Itoa(m.transcriptTotalEntries),
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.append_entries", fields))
}

func (m *uiModel) logDeferredCommittedTailMergeDiag(evt clientui.Event, reduction deferredCommittedTailMergeReduction) {
	if m == nil || !m.transcriptDiagnosticsEnabled() {
		return
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.merge_deferred_tail", map[string]string{
		"session_id":     strings.TrimSpace(m.sessionID),
		"mode":           m.transcriptModeLabel(),
		"kind":           string(evt.Kind),
		"merged_start":   strconv.Itoa(reduction.mergedStart),
		"merged_count":   strconv.Itoa(reduction.mergedCount),
		"consumed_tails": strconv.Itoa(reduction.consumedTails),
	}))
}

func (m *uiModel) logDeferredCommittedTailDeferDiag(evt clientui.Event, reduction deferredCommittedTailDeferReduction) {
	if m == nil || !m.transcriptDiagnosticsEnabled() {
		return
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.defer_tail", map[string]string{
		"session_id":     strings.TrimSpace(m.sessionID),
		"mode":           m.transcriptModeLabel(),
		"kind":           string(evt.Kind),
		"range_start":    strconv.Itoa(reduction.tail.rangeStart),
		"range_end":      strconv.Itoa(reduction.tail.rangeEnd),
		"revision":       strconv.FormatInt(evt.TranscriptRevision, 10),
		"entries_digest": transcriptdiag.EntriesDigest(evt.TranscriptEntries),
		"pending_count":  strconv.Itoa(len(reduction.tail.pending)),
	}))
}

func (m *uiModel) logDeferredCommittedTailClearDiag(reason string) {
	if m == nil || !m.transcriptDiagnosticsEnabled() || len(m.deferredCommittedTail) == 0 {
		return
	}
	pendingCount := 0
	for _, deferred := range m.deferredCommittedTail {
		pendingCount += len(deferred.pending)
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.clear_deferred_tail", map[string]string{
		"session_id":    strings.TrimSpace(m.sessionID),
		"mode":          m.transcriptModeLabel(),
		"reason":        reason,
		"tail_count":    strconv.Itoa(len(m.deferredCommittedTail)),
		"pending_count": strconv.Itoa(pendingCount),
	}))
}

func (m *uiModel) logCommittedTranscriptContinuityRecoveryStartDiag() {
	if m == nil || !m.transcriptDiagnosticsEnabled() {
		return
	}
	m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.begin_continuity_recovery", map[string]string{
		"session_id":    strings.TrimSpace(m.sessionID),
		"mode":          m.transcriptModeLabel(),
		"current_base":  strconv.Itoa(m.transcriptBaseOffset),
		"current_count": strconv.Itoa(len(m.transcriptEntries)),
		"current_total": strconv.Itoa(m.transcriptTotalEntries),
	}))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
