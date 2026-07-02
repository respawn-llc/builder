package app

import (
	"fmt"
	"strconv"
	"strings"

	"core/shared/clientui"
	"core/shared/invariant"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) preflightNativeCommittedTranscriptEvent(state projectedTranscriptEventState, evt clientui.Event) (tea.Cmd, bool) {
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
