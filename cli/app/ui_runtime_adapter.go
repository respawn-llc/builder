package app

import (
	"errors"
	"strconv"
	"strings"

	"core/cli/app/internal/runtimestate"
	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/invariant"

	tea "github.com/charmbracelet/bubbletea"
)

type uiRuntimeAdapter struct {
	model *uiModel
}

var errNativeAssistantStreamStepChanged = errors.New("native assistant stream step changed before the previous stream finalized")

type runtimeEventApplyResult struct {
	cmd               tea.Cmd
	transcriptMutated bool
	awaitsHydration   bool
	fatal             bool
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEventsBatch(events []clientui.Event) runtimeEventApplyResult {
	cmds := make([]tea.Cmd, 0, len(events)+1)
	transcriptMutated := false
	awaitsHydration := false
	fatal := false
	for idx, evt := range events {
		result := a.applyProjectedRuntimeEvent(evt)
		cmds = append(cmds, result.cmd)
		transcriptMutated = transcriptMutated || result.transcriptMutated
		awaitsHydration = awaitsHydration || result.awaitsHydration
		if result.fatal {
			fatal = true
			break
		}
		if result.awaitsHydration {
			if remaining := events[idx+1:]; len(remaining) > 0 && a.model != nil {
				a.model.pendingRuntimeEvents = append(append([]clientui.Event(nil), remaining...), a.model.pendingRuntimeEvents...)
			}
			break
		}
	}
	batchedCmd := batchCmds(cmds...)
	if !transcriptMutated {
		return runtimeEventApplyResult{cmd: batchedCmd, awaitsHydration: awaitsHydration, fatal: fatal}
	}
	return runtimeEventApplyResult{cmd: batchedCmd, transcriptMutated: true, awaitsHydration: awaitsHydration, fatal: fatal}
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEvent(evt clientui.Event) runtimeEventApplyResult {
	m := a.model
	if runtimeEventHasReadModelPayload(evt) && evt.ReadModelVersion.Validate() != nil {
		decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
		return runtimeEventApplyResult{
			cmd:             tea.Batch(decision.cmd, m.sendTransientStatusWithNoticeID("invalid runtime read-model update ignored; refreshing session view", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")),
			awaitsHydration: decision.started,
		}
	}
	if runtimeEventHasReadModelPayload(evt) {
		switch m.acceptRuntimeReadModelVersion(evt.ReadModelVersion, false) {
		case runtimeReadModelVersionIgnore:
			return runtimeEventApplyResult{cmd: m.runtimeReadModelConflictDiagnosticCmd(evt)}
		case runtimeReadModelVersionRefresh:
			decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
			return runtimeEventApplyResult{cmd: decision.cmd, awaitsHydration: decision.started}
		}
	}
	projectedState := newProjectedTranscriptEventState(projectedTranscriptEventSnapshotFromModel(m))
	skipDeferredTailMerge := projectedEventIsLiveOnlyUnresolvedToolStart(projectedState, evt) ||
		m.deferredCommittedTailBypassesMergeAtNewTurnBoundary(projectedState, evt)
	if !skipDeferredTailMerge {
		if merge := reduceDeferredCommittedTailMerge(newDeferredCommittedTailState(deferredCommittedTailSnapshotFromModel(m)), evt); merge.merged {
			evt = merge.event
			m.deferredCommittedTail = merge.remaining
			m.logDeferredCommittedTailMergeDiag(evt, merge)
		}
	}
	if cmd, fatal := m.preflightNativeCommittedTranscriptEvent(projectedState, evt); fatal {
		return runtimeEventApplyResult{cmd: cmd, fatal: true}
	}
	if m.turnQueueHook != nil {
		m.turnQueueHook.OnProjectedRuntimeEvent(evt)
	}
	turnBoundaryCmd, turnBoundaryMutated, turnBoundaryAwaitsHydration, turnBoundaryFatal := a.flushDeferredCommittedTailAtNewTurnBoundary(projectedState, evt)
	if turnBoundaryFatal {
		return runtimeEventApplyResult{
			cmd:               turnBoundaryCmd,
			transcriptMutated: turnBoundaryMutated,
			awaitsHydration:   turnBoundaryAwaitsHydration,
			fatal:             true,
		}
	}
	turnBoundaryResetCmd := tea.Cmd(nil)
	if m.shouldResetActiveAssistantStreamForNewStep(projectedState, evt) {
		var fatal bool
		turnBoundaryResetCmd, fatal = m.resetActiveAssistantStreamForNewStep(evt.StepID)
		if fatal {
			return runtimeEventApplyResult{
				cmd:               sequenceCmds(turnBoundaryCmd, turnBoundaryResetCmd),
				transcriptMutated: turnBoundaryMutated,
				awaitsHydration:   turnBoundaryAwaitsHydration,
				fatal:             true,
			}
		}
	}
	reduction := runtimestate.ReduceRuntimeEvent(
		a.runtimeRunState(),
		a.runtimeConversationState(),
		a.pendingInputState(),
		a.runtimeReasoningState(),
		m.activity == uiActivityRunning,
		evt,
	)
	transcriptSync := a.effectiveRuntimeTranscriptSync(evt, reduction.Transcript.Sync)
	m.logTranscriptEventDiag("transcript.diag.client.apply_event", evt, map[string]string{
		"path":                     "live_event",
		"recovery_cause":           string(evt.RecoveryCause),
		"sync_session_view":        strconv.FormatBool(transcriptSync.Reason != runtimestate.RuntimeTranscriptSyncNone),
		"sync_reason":              runtimeTranscriptSyncReasonLabel(transcriptSync),
		"consumed_queued_messages": strconv.Itoa(len(reduction.PendingInput.ConsumedQueueItemIDs)),
	})
	m.markActiveSubmitFlushed(evt)
	m.trackRuntimeActivityToken(evt)
	m.applyRuntimeEventStatus(evt)
	if !m.processList.open {
		m.applyBackgroundProcessEventToCache(evt.Background)
	}
	cmds := make([]tea.Cmd, 0, 5)
	if evt.Kind == clientui.EventStreamingErrorUpdated {
		if err := m.finishNativeAssistantStreaming(); err != nil {
			cmds = append(cmds, m.nativeSurfaceErrorCmd("finish assistant stream", err))
		}
	}
	cmds = append(cmds, turnBoundaryCmd)
	cmds = append(cmds, turnBoundaryResetCmd)
	cmds = append(cmds, a.applyRuntimeEventReduction(reduction))
	cmds = append(cmds, a.reconcileInterruptFromRunState(evt))
	cmds = append(cmds, a.reconcileInterruptFromRuntimeActivity(evt))
	transcriptMutated := turnBoundaryMutated
	awaitsHydration := turnBoundaryAwaitsHydration
	if len(evt.TranscriptEntries) > 0 {
		cmd, mutated, needsHydration, fatal := a.applyProjectedTranscriptEntries(evt)
		cmds = append(cmds, cmd)
		transcriptMutated = transcriptMutated || mutated
		awaitsHydration = awaitsHydration || needsHydration
		if fatal {
			return runtimeEventApplyResult{
				cmd:               batchCmds(cmds...),
				transcriptMutated: transcriptMutated,
				awaitsHydration:   awaitsHydration,
				fatal:             true,
			}
		}
		streamFinalizer := mutated && isAssistantStreamFinalizerEvent(projectedState, evt)
		if (shouldClearAssistantStreamForCommittedAssistantEvent(evt, m.activeAssistantStreamText()) && (mutated || skippedAssistantCommitMatchesActiveLiveStream(m, evt))) || streamFinalizer {
			if stepID := strings.TrimSpace(evt.StepID); stepID != "" {
				m.lastCommittedAssistantStepID = stepID
			}
			if err := m.finishNativeAssistantStreaming(); err != nil {
				cmds = append(cmds, m.nativeSurfaceErrorCmd("finish assistant stream", err))
			}
			m.sawAssistantDelta = false
			m.clearActiveAssistantStreamSource()
			m.forwardToView(tui.ClearOngoingAssistantMsg{})
		}
	}
	for _, streamCommand := range reduction.Transcript.AssistantStream {
		switch streamCommand.Kind {
		case runtimestate.RuntimeAssistantStreamAppend:
			delta := streamCommand.Delta
			if shouldIgnoreStaleAssistantDelta(m, evt, delta) {
				continue
			}
			if isNoopFinalText(delta) {
				continue
			}
			m.sawAssistantDelta = true
			m.appendActiveAssistantStreamDelta(streamCommand.StepID, delta)
			if handled, err := m.streamNativeAssistantDelta(delta, streamCommand.Phase); handled && err != nil {
				cmds = append(cmds, m.nativeSurfaceErrorCmd("stream assistant content", err))
			}
			m.forwardToView(tui.StreamAssistantMsg{Delta: delta})
		case runtimestate.RuntimeAssistantStreamClear:
			if stepID := strings.TrimSpace(streamCommand.StepID); stepID != "" {
				m.lastCommittedAssistantStepID = stepID
			}
			if err := m.finishNativeAssistantStreaming(); err != nil {
				cmds = append(cmds, m.nativeSurfaceErrorCmd("finish assistant stream", err))
			}
			m.sawAssistantDelta = false
			m.clearActiveAssistantStreamSource()
			m.forwardToView(tui.ClearOngoingAssistantMsg{})
			cmds = append(cmds, m.releaseDeferredRuntimeSyncs())
		}
	}
	for _, streamCommand := range reduction.Reasoning.Stream {
		switch streamCommand.Kind {
		case runtimestate.RuntimeReasoningStreamUpsert:
			if streamCommand.Delta == nil {
				continue
			}
			m.reasoningLiveDirty = true
			m.forwardToView(tui.UpsertStreamingReasoningMsg{Key: streamCommand.Delta.Key, Role: streamCommand.Delta.Role, Text: streamCommand.Delta.Text})
		case runtimestate.RuntimeReasoningStreamClear:
			m.reasoningLiveDirty = false
			m.forwardToView(tui.ClearStreamingReasoningMsg{})
			cmds = append(cmds, m.releaseDeferredRuntimeSyncs())
		}
	}
	if reduction.Notices.BackgroundNotice != nil {
		kind := uiStatusNoticeSuccess
		if reduction.Notices.BackgroundNotice.Kind == runtimestate.BackgroundNoticeError {
			kind = uiStatusNoticeError
		}
		cmds = append(cmds, m.sendTransientStatusWithNoticeID(reduction.Notices.BackgroundNotice.Message, kind, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	if reduction.Notices.DiagnosticNotice != nil {
		kind := uiStatusNoticeNeutral
		if reduction.Notices.DiagnosticNotice.Kind == runtimestate.BackgroundNoticeError {
			kind = uiStatusNoticeError
		}
		cmds = append(cmds, m.inputController().appendSystemFeedbackWithMirroredStatus(reduction.Notices.DiagnosticNotice.Message, kind))
	}
	if reduction.Notices.TransientDiagnostic != nil {
		kind := uiStatusNoticeNeutral
		if reduction.Notices.TransientDiagnostic.Kind == runtimestate.BackgroundNoticeError {
			kind = uiStatusNoticeError
		}
		cmds = append(cmds, m.sendTransientStatusWithNoticeID(reduction.Notices.TransientDiagnostic.Message, kind, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	if transcriptSync.Reason != runtimestate.RuntimeTranscriptSyncNone {
		syncDecision := a.syncConversationFromRuntimeTranscriptCommand(transcriptSync)
		cmds = append(cmds, syncDecision.cmd)
		awaitsHydration = awaitsHydration || syncDecision.awaitsHydration
	} else if shouldRefreshDeferredCommittedTailOnRunEnd(m, evt) {
		cmds = append(cmds, m.requestRuntimeCommittedConversationSync())
	}
	return runtimeEventApplyResult{cmd: batchCmds(cmds...), transcriptMutated: transcriptMutated, awaitsHydration: awaitsHydration}
}

func (m *uiModel) preflightNativeCommittedTranscriptEvent(state projectedTranscriptEventState, evt clientui.Event) (tea.Cmd, bool) {
	if m == nil || len(evt.TranscriptEntries) == 0 || !m.nativeSurfaceConfigured() {
		return nil, false
	}
	reduction := reduceProjectedTranscriptEvent(state, evt)
	if reduction.decision == projectedTranscriptDecisionHydrate &&
		m.nativeImmutableTranscriptWritten &&
		reduction.hydrationCause == clientui.TranscriptRecoveryCauseNone &&
		!(m.view.Mode() == tui.ModeDetail && isAssistantStreamFinalizerEvent(state, evt)) {
		m.logNativeTranscriptInvariant("hydrate committed transcript", errNativeStableNonGapHydration, state, evt, reduction)
		return m.nativeInvariantViolationCmd("hydrate committed transcript", errNativeStableNonGapHydration)
	}
	activeStream := state.liveAssistantText
	if activeStream == "" || !evt.CommittedTranscriptChanged || reduction.plan.mode != projectedTranscriptEntryPlanAppend {
		return nil, false
	}
	convertedEntries := make([]tui.TranscriptEntry, 0, len(reduction.plan.entries))
	for _, entry := range reduction.plan.entries {
		convertedEntries = append(convertedEntries, transcriptEntryFromProjectedEventEntry(evt, entry, reduction.projectedTransient, reduction.projectedCommitted))
	}
	if !shouldClearAssistantStreamForCommittedTranscriptEntries(convertedEntries, activeStream) {
		return nil, false
	}
	if _, err := planNativeAssistantStreamFinalizerEmission(convertedEntries, activeStream); err != nil {
		m.logNativeTranscriptInvariant("finalize native assistant stream", err, state, evt, reduction)
		return m.nativeInvariantViolationCmd("finalize native assistant stream", err)
	}
	return nil, false
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

func (a uiRuntimeAdapter) flushDeferredCommittedTailAtNewTurnBoundary(state projectedTranscriptEventState, evt clientui.Event) (tea.Cmd, bool, bool, bool) {
	m := a.model
	if m == nil || !m.deferredCommittedTailCanFlushAtNewTurnBoundary(state, evt) {
		return nil, false, false, false
	}
	flushEvent, remaining, ok := deferredCommittedTailFinalizerFlushEvent(
		newDeferredCommittedTailState(deferredCommittedTailSnapshotFromModel(m)),
		state.liveAssistantText,
		state.liveAssistantStepID,
	)
	if !ok {
		return nil, false, false, false
	}
	m.deferredCommittedTail = remaining
	m.logDeferredCommittedTailTurnBoundaryFlushDiag(evt, flushEvent)
	return a.applyProjectedTranscriptEntries(flushEvent)
}

func (m *uiModel) deferredCommittedTailCanFlushAtNewTurnBoundary(state projectedTranscriptEventState, evt clientui.Event) bool {
	if m == nil || len(m.deferredCommittedTail) == 0 || !eventCanFlushDeferredCommittedTailAtNewTurnBoundary(state, evt) {
		return false
	}
	_, _, ok := deferredCommittedTailFinalizerFlushEvent(
		newDeferredCommittedTailState(deferredCommittedTailSnapshotFromModel(m)),
		state.liveAssistantText,
		state.liveAssistantStepID,
	)
	return ok
}

func (m *uiModel) deferredCommittedTailBypassesMergeAtNewTurnBoundary(state projectedTranscriptEventState, evt clientui.Event) bool {
	if m == nil || len(m.deferredCommittedTail) == 0 || !eventCanFlushDeferredCommittedTailAtNewTurnBoundary(state, evt) {
		return false
	}
	deferredState := newDeferredCommittedTailState(deferredCommittedTailSnapshotFromModel(m))
	_, _, canFlush := deferredCommittedTailFinalizerFlushEvent(
		deferredState,
		state.liveAssistantText,
		state.liveAssistantStepID,
	)
	return canFlush || deferredCommittedTailHasKnownMismatchedActiveFinalizer(deferredState, state.liveAssistantText, state.liveAssistantStepID)
}

func (m *uiModel) resetActiveAssistantStreamForNewStep(stepID string) (tea.Cmd, bool) {
	if m == nil {
		return nil, false
	}
	trimmedStepID := strings.TrimSpace(stepID)
	if trimmedStepID == "" || strings.TrimSpace(m.activeAssistantStreamText()) == "" || strings.TrimSpace(m.activeAssistantStreamStepID) == trimmedStepID {
		return nil, false
	}
	nativeStreaming := m.nativeSurfaceConfigured() && m.nativeSurface.AssistantStreaming()
	if nativeStreaming {
		if cmd, fatal := m.nativeInvariantViolationCmd("reset native assistant stream", errNativeAssistantStreamStepChanged); fatal {
			return cmd, true
		}
	}
	m.sawAssistantDelta = false
	m.nativeAssistantStreamIncomplete = false
	m.clearActiveAssistantStreamSource()
	m.forwardToView(tui.ClearOngoingAssistantMsg{})
	return nil, false
}

func eventStartsDifferentAssistantStep(state projectedTranscriptEventState, evt clientui.Event) bool {
	activeStepID := strings.TrimSpace(state.liveAssistantStepID)
	eventStepID := strings.TrimSpace(evt.StepID)
	if eventStepID == "" || activeStepID == eventStepID || !state.liveAssistantPending {
		return false
	}
	switch evt.Kind {
	case clientui.EventAssistantDelta, clientui.EventToolCallStarted:
		return true
	case clientui.EventAssistantMessage:
		return committedAssistantMessageStartsDifferentStep(state, evt)
	default:
		return false
	}
}

func committedAssistantMessageStartsDifferentStep(state projectedTranscriptEventState, evt clientui.Event) bool {
	if evt.Kind != clientui.EventAssistantMessage || !evt.CommittedTranscriptChanged || len(evt.TranscriptEntries) == 0 {
		return false
	}
	if strings.TrimSpace(state.liveAssistantStepID) == "" {
		return false
	}
	activeStream := strings.TrimSpace(state.liveAssistantText)
	if activeStream == "" {
		return true
	}
	for _, entry := range evt.TranscriptEntries {
		if tui.TranscriptRoleFromWire(entry.Role) == tui.TranscriptRoleAssistant && strings.TrimSpace(entry.Text) == activeStream {
			return false
		}
	}
	return true
}

func eventCanFlushDeferredCommittedTailAtNewTurnBoundary(state projectedTranscriptEventState, evt clientui.Event) bool {
	if !eventStepDiffersFromActiveAssistant(state, evt) {
		return false
	}
	switch evt.Kind {
	case clientui.EventAssistantDelta,
		clientui.EventAssistantMessage,
		clientui.EventToolCallStarted,
		clientui.EventUserMessageFlushed:
		return true
	default:
		return false
	}
}

func eventStepDiffersFromActiveAssistant(state projectedTranscriptEventState, evt clientui.Event) bool {
	activeStepID := strings.TrimSpace(state.liveAssistantStepID)
	eventStepID := strings.TrimSpace(evt.StepID)
	return eventStepID != "" && activeStepID != eventStepID && state.liveAssistantPending
}

func (m *uiModel) shouldResetActiveAssistantStreamForNewStep(state projectedTranscriptEventState, evt clientui.Event) bool {
	if !eventStartsDifferentAssistantStep(state, evt) {
		return false
	}
	return true
}

func runtimeTranscriptSyncReasonLabel(sync runtimestate.RuntimeTranscriptSyncCommand) string {
	if sync.Reason == runtimestate.RuntimeTranscriptSyncNone {
		return ""
	}
	return string(sync.Reason)
}

func (m *uiModel) streamNativeAssistantDelta(delta string, phase clientui.MessagePhase) (bool, error) {
	if m == nil || !m.nativeSurfaceConfigured() {
		return false, nil
	}
	m.dropNativeSurfaceIfGeometryUnsupported()
	if !m.nativeSurfaceGeometrySupported() {
		m.nativeAssistantStreamIncomplete = true
		return false, nil
	}
	if m.nativeResizeRehydratePending() {
		m.nativeAssistantStreamIncomplete = true
		return false, nil
	}
	switch phase {
	case clientui.MessagePhaseCommentary:
		if m.nativeAssistantStreamIncomplete {
			return false, nil
		}
		if !m.nativeSurface.initialized() {
			m.nativeAssistantStreamIncomplete = true
			return false, nil
		}
		return true, m.nativeSurface.StreamAssistantCommentaryContent(delta)
	case clientui.MessagePhaseFinal:
		if m.nativeAssistantStreamIncomplete {
			return false, nil
		}
		if !m.nativeSurface.initialized() {
			m.nativeAssistantStreamIncomplete = true
			return false, nil
		}
		return true, m.nativeSurface.StreamAssistantFinalAnswerContent(delta)
	default:
		m.nativeAssistantStreamIncomplete = true
		return false, nil
	}
}

func (m *uiModel) finishNativeAssistantStreaming() error {
	if m == nil || m.nativeSurface == nil {
		return nil
	}
	defer func() {
		m.nativeAssistantStreamIncomplete = false
	}()
	m.dropNativeSurfaceIfGeometryUnsupported()
	if !m.nativeSurfaceGeometrySupported() {
		return nil
	}
	if !m.nativeSurface.initialized() {
		return nil
	}
	return m.nativeSurface.FinishAssistantStreaming()
}

func (m *uiModel) nativeSurfaceErrorCmd(action string, err error) tea.Cmd {
	if m == nil || err == nil {
		return nil
	}
	m.nativeLiveAreaError = err
	if m.nativeSurface != nil {
		m.closeNativeSurface()
	}
	action = strings.TrimSpace(action)
	if action == "" {
		action = "native terminal write"
	}
	m.logf("native.surface action=%q err=%q", action, err.Error())
	return m.sendTransientStatusWithNoticeID(action+" failed: "+err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
}

func (m *uiModel) nativeFatalSurfaceErrorCmd(action string, err error) tea.Cmd {
	if err == nil {
		return nil
	}
	cmd := m.nativeSurfaceErrorCmd(action, err)
	if invariant.NewPolicy().Mode() != invariant.ModePanic {
		return cmd
	}
	return sequenceCmds(cmd, tea.Quit)
}

func (m *uiModel) nativeInvariantViolationCmd(action string, err error) (tea.Cmd, bool) {
	if err == nil {
		return nil, false
	}
	if invariant.NewPolicy().Mode() == invariant.ModePanic {
		return m.nativeFatalSurfaceErrorCmd(action, err), true
	}
	m.disableNativeOutputForInvariant(action, err)
	return nil, false
}

func (m *uiModel) disableNativeOutputForInvariant(action string, err error) {
	if m == nil || err == nil {
		return
	}
	action = strings.TrimSpace(action)
	if action == "" {
		action = "native invariant"
	}
	m.logf("native.invariant action=%q err=%q disabling_native=true", action, err.Error())
	m.nativeLiveAreaError = nil
	if m.nativeSurface != nil {
		m.dropNativeSurface()
	}
}

func (m *uiModel) nativeSurfaceDropErrorCmd(action string, err error) tea.Cmd {
	if m == nil || err == nil {
		return nil
	}
	m.nativeLiveAreaError = err
	if m.nativeSurface != nil {
		m.dropNativeSurface()
	}
	action = strings.TrimSpace(action)
	if action == "" {
		action = "native terminal write"
	}
	m.logf("native.surface action=%q err=%q", action, err.Error())
	return m.sendTransientStatusWithNoticeID(action+" failed: "+err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
}

func (a uiRuntimeAdapter) syncConversationFromRuntimeTranscriptCommand(sync runtimestate.RuntimeTranscriptSyncCommand) runtimeTranscriptSyncDecision {
	switch sync.Reason {
	case runtimestate.RuntimeTranscriptSyncRecovery, runtimestate.RuntimeTranscriptSyncStreamGap:
		return a.model.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(a.model.transcriptRequestForCurrentMode(), false, runtimeTranscriptSyncCauseContinuityRecovery, sync.RecoveryCause))
	case runtimestate.RuntimeTranscriptSyncCommittedAdvance, runtimestate.RuntimeTranscriptSyncStreamingErrorUpdated:
		return a.syncConversationFromEngine()
	default:
		return runtimeTranscriptSyncDecision{}
	}
}

func (a uiRuntimeAdapter) syncConversationFromEngine() runtimeTranscriptSyncDecision {
	m := a.model
	if !m.hasRuntimeClient() {
		return runtimeTranscriptSyncDecision{}
	}
	return m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), false, runtimeTranscriptSyncCauseCommittedConversation, clientui.TranscriptRecoveryCauseNone))
}

func waitAskEvent(ch <-chan askEvent) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil
		}
		return askEventMsg{event: evt}
	}
}

func waitRuntimeConnectionStateChange(ch <-chan runtimeConnectionStateChangedMsg) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func waitRuntimeReconnectWarning(ch <-chan runtimeReconnectWarningMsg) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}
