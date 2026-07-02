package app

import (
	"fmt"
	"strconv"
	"strings"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/invariant"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) preflightNativeCommittedTranscriptEvent(state projectedTranscriptEventState, evt clientui.Event) (tea.Cmd, bool) {
	if m == nil || len(evt.TranscriptEntries) == 0 {
		return nil, false
	}
	state = projectedStateAfterDroppingTrailingTransientForCommittedEvent(state, evt)
	nativeInvariantActive := m.nativeSurfaceConfigured() || m.nativeImmutableTranscriptWritten || m.nativeAssistantStreamIncomplete
	if !nativeInvariantActive {
		return nil, false
	}
	reduction := reduceProjectedTranscriptEvent(state, evt)
	convertedEntries := func() []tui.TranscriptEntry {
		out := make([]tui.TranscriptEntry, 0, len(reduction.plan.entries))
		for _, entry := range reduction.plan.entries {
			out = append(out, transcriptEntryFromProjectedEventEntry(evt, entry, reduction.projectedTransient, reduction.projectedCommitted))
		}
		return out
	}
	nativeAssistantStreaming := m.nativeSurfaceConfigured() && m.nativeSurface.AssistantStreaming()
	if reduction.decision == projectedTranscriptDecisionHydrate &&
		(m.nativeImmutableTranscriptWritten || nativeAssistantStreaming) &&
		reduction.hydrationCause == clientui.TranscriptRecoveryCauseNone &&
		!(m.view.Mode() == tui.ModeDetail && isAssistantStreamFinalizerEvent(state, evt)) {
		m.logNativeTranscriptInvariant("hydrate committed transcript", errNativeStableNonGapHydration, state, evt, reduction)
		return m.nativeInvariantViolationCmd("hydrate committed transcript", errNativeStableNonGapHydration, m.nativeTranscriptInvariantFields(state, evt, reduction))
	}
	if reduction.decision == projectedTranscriptDecisionHydrate {
		return nil, false
	}
	if reduction.plan.mode == projectedTranscriptEntryPlanReplace &&
		m.nativeImmutableTranscriptWritten &&
		!allTranscriptEntriesTransient(convertedEntries()) {
		if evt.RecoveryCause == clientui.TranscriptRecoveryCauseStreamGap {
			return nil, false
		}
		m.logNativeTranscriptInvariant("steer committed transcript", errNativeStableNonAppend, state, evt, reduction)
		return m.nativeInvariantViolationCmd("steer committed transcript", errNativeStableNonAppend, m.nativeTranscriptInvariantFields(state, evt, reduction))
	}
	activeStream := state.liveAssistantText
	if activeStream == "" || !evt.CommittedTranscriptChanged || reduction.plan.mode != projectedTranscriptEntryPlanAppend {
		return nil, false
	}
	converted := convertedEntries()
	if !shouldClearAssistantStreamForCommittedTranscriptEntries(converted, activeStream) {
		return nil, false
	}
	if nativeAssistantStreaming && state.liveAssistantIdentity.frontier == nil {
		m.logNativeTranscriptInvariant("finalize native assistant stream", errNativeAssistantStreamMetadataMissing, state, evt, reduction)
		return m.nativeInvariantViolationCmd("finalize native assistant stream", errNativeAssistantStreamMetadataMissing, m.nativeTranscriptInvariantFields(state, evt, reduction))
	}
	if _, err := planNativeAssistantStreamFinalizerEmission(converted, activeStream); err != nil {
		m.logNativeTranscriptInvariant("finalize native assistant stream", err, state, evt, reduction)
		return m.nativeInvariantViolationCmd("finalize native assistant stream", err, m.nativeTranscriptInvariantFields(state, evt, reduction))
	}
	return nil, false
}

func (m *uiModel) nativeTranscriptInvariantFields(state projectedTranscriptEventState, evt clientui.Event, reduction projectedTranscriptReduction) map[invariant.Field]string {
	return map[invariant.Field]string{
		invariant.FieldTerminalGeometry:   m.nativeTranscriptTerminalGeometry(),
		invariant.FieldEventKind:          string(evt.Kind),
		invariant.FieldEventStepID:        strings.TrimSpace(evt.StepID),
		invariant.FieldRecoveryCause:      string(evt.RecoveryCause),
		invariant.FieldDecision:           strconv.Itoa(int(reduction.decision)),
		invariant.FieldPlan:               reduction.plan.mode.label(),
		invariant.FieldDivergence:         reduction.plan.divergence,
		invariant.FieldEventStart:         strconv.Itoa(evt.CommittedEntryStart),
		invariant.FieldEventCount:         strconv.Itoa(len(evt.TranscriptEntries)),
		invariant.FieldCommittedCount:     strconv.Itoa(evt.CommittedEntryCount),
		invariant.FieldTranscriptRevision: strconv.FormatInt(evt.TranscriptRevision, 10),
		invariant.FieldTranscriptState:    fmt.Sprintf("base=%d entries=%d revision=%d native_written=%t", state.baseOffset, len(state.entries), state.revision, m.nativeImmutableTranscriptWritten),
		invariant.FieldLiveState:          fmt.Sprintf("step_id=%q chars=%d", strings.TrimSpace(state.liveAssistantStepID), len(state.liveAssistantText)),
	}
}

func (m *uiModel) nativeTranscriptTerminalGeometry() string {
	if m == nil {
		return ""
	}
	return fmt.Sprintf("width=%d height=%d known=%t", m.termWidth, m.termHeight, m.windowSizeKnown)
}

func (m *uiModel) logNativeTranscriptInvariant(action string, err error, state projectedTranscriptEventState, evt clientui.Event, reduction projectedTranscriptReduction) {
	if m == nil || err == nil {
		return
	}
	m.logf(
		"native.invariant.transcript action=%q err=%q event_kind=%s event_step_id=%q recovery_cause=%s decision=%d plan=%s divergence=%q event_start=%d event_count=%d committed_count=%d transcript_revision=%d state_base=%d state_entries=%d state_revision=%d live_step_id=%q live_chars=%d native_written=%t",
		strings.TrimSpace(action),
		err.Error(),
		evt.Kind,
		strings.TrimSpace(evt.StepID),
		evt.RecoveryCause,
		reduction.decision,
		reduction.plan.mode.label(),
		reduction.plan.divergence,
		evt.CommittedEntryStart,
		len(evt.TranscriptEntries),
		evt.CommittedEntryCount,
		evt.TranscriptRevision,
		state.baseOffset,
		len(state.entries),
		state.revision,
		strings.TrimSpace(state.liveAssistantStepID),
		len(state.liveAssistantText),
		m.nativeImmutableTranscriptWritten,
	)
}
