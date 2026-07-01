package app

import (
	"errors"
	"strconv"
	"strings"

	"core/cli/app/internal/runtimestate"
	"core/cli/tui"
	"core/shared/clientui"

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
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEventsBatch(events []clientui.Event) runtimeEventApplyResult {
	cmds := make([]tea.Cmd, 0, len(events)+1)
	transcriptMutated := false
	awaitsHydration := false
	for _, evt := range events {
		result := a.applyProjectedRuntimeEvent(evt)
		cmds = append(cmds, result.cmd)
		transcriptMutated = transcriptMutated || result.transcriptMutated
		awaitsHydration = awaitsHydration || result.awaitsHydration
	}
	batchedCmd := batchCmds(cmds...)
	if !transcriptMutated {
		return runtimeEventApplyResult{cmd: batchedCmd, awaitsHydration: awaitsHydration}
	}
	return runtimeEventApplyResult{cmd: batchedCmd, transcriptMutated: true, awaitsHydration: awaitsHydration}
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
	if m.turnQueueHook != nil {
		m.turnQueueHook.OnProjectedRuntimeEvent(evt)
	}
	turnBoundaryCmd, turnBoundaryMutated, turnBoundaryAwaitsHydration := a.flushDeferredCommittedTailAtNewTurnBoundary(projectedState, evt)
	turnBoundaryResetCmd := tea.Cmd(nil)
	if m.shouldResetActiveAssistantStreamForNewStep(projectedState, evt) {
		turnBoundaryResetCmd = m.resetActiveAssistantStreamForNewStep(evt.StepID)
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
		cmd, mutated, needsHydration := a.applyProjectedTranscriptEntries(evt)
		cmds = append(cmds, cmd)
		transcriptMutated = transcriptMutated || mutated
		awaitsHydration = awaitsHydration || needsHydration
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

func (a uiRuntimeAdapter) flushDeferredCommittedTailAtNewTurnBoundary(state projectedTranscriptEventState, evt clientui.Event) (tea.Cmd, bool, bool) {
	m := a.model
	if m == nil || !m.deferredCommittedTailCanFlushAtNewTurnBoundary(state, evt) {
		return nil, false, false
	}
	flushEvent, remaining, ok := deferredCommittedTailFinalizerFlushEvent(
		newDeferredCommittedTailState(deferredCommittedTailSnapshotFromModel(m)),
		state.liveAssistantText,
		state.liveAssistantStepID,
	)
	if !ok {
		return nil, false, false
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

func (m *uiModel) resetActiveAssistantStreamForNewStep(stepID string) tea.Cmd {
	if m == nil {
		return nil
	}
	trimmedStepID := strings.TrimSpace(stepID)
	if trimmedStepID == "" || strings.TrimSpace(m.activeAssistantStreamText()) == "" || strings.TrimSpace(m.activeAssistantStreamStepID) == trimmedStepID {
		return nil
	}
	nativeStreaming := m.nativeSurfaceConfigured() && m.nativeSurface.AssistantStreaming()
	m.sawAssistantDelta = false
	m.nativeAssistantStreamIncomplete = false
	m.clearActiveAssistantStreamSource()
	m.forwardToView(tui.ClearOngoingAssistantMsg{})
	if nativeStreaming {
		return m.nativeSurfaceErrorCmd("reset native assistant stream", errNativeAssistantStreamStepChanged)
	}
	return nil
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
	if strings.TrimSpace(state.liveAssistantStepID) == "" && m != nil && m.nativeSurfaceConfigured() && m.nativeSurface.AssistantStreaming() {
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
	if !m.nativeSurface.initialized() {
		return nil
	}
	return m.nativeSurface.FinishAssistantStreaming()
}

func (m *uiModel) deliverNativeStableProjectionChange(intent nativeStableDeliveryIntent, previous tui.TranscriptProjection, current tui.TranscriptProjection, nativeStableReady bool, nativeAssistantStreamActive bool, nativeAssistantStreamWasIncomplete bool, nativeAssistantStreamText string) (err error) {
	if m == nil {
		return nil
	}
	if nativeStableReady {
		defer func() {
			if err == nil {
				m.nativePendingStableIntent = nativeStableDeliveryIntent{}
			}
		}()
	}
	nativeStableNeedsDelivery := m.nativeStableProjectionNeedsDelivery(intent, previous, current)
	if !nativeStableNeedsDelivery {
		if nativeAssistantStreamActive {
			if !intent.allowActiveStreamFinalizeFromText() {
				return m.nativeStableProjectionDeliveryError(intent, nativeStableProjectionActiveStreamMismatchReason, previous, current)
			}
			return m.finishNativeAssistantStreaming()
		}
		return nil
	}
	if !nativeStableReady {
		m.nativePendingStableIntent = nativeStableMergePendingDeliveryIntent(m.nativePendingStableIntent, intent)
		m.nativeAssistantStreamIncomplete = strings.TrimSpace(m.view.OngoingStreamingText()) != ""
		return nil
	}
	if nativeAssistantStreamActive && !intent.allowActiveStreamFinalizeFromText() {
		return m.nativeStableProjectionDeliveryError(intent, nativeStableProjectionActiveStreamMismatchReason, previous, current)
	}
	if !nativeAssistantStreamActive {
		return m.steerNativeStableRuntimeProjectionChange(intent, previous, current)
	}
	appendBlocks, ok := m.nativeStableAppendBlocksForProjectionChange(intent, previous, current)
	if !ok {
		return m.nativeStableProjectionDeliveryError(intent, nativeStableProjectionNonContiguousReason, previous, current)
	}
	if len(appendBlocks) == 0 {
		return nil
	}
	streamAppendPosition := -1
	streamBlockIndex := -1
	for position, blockIndex := range appendBlocks {
		if blockIndex >= len(current.Blocks) {
			return m.nativeStableProjectionDeliveryError(intent, nativeStableProjectionActiveStreamMismatchReason, previous, current)
		}
		if !nativeStableProjectionBlockCanFinalizeAssistantStream(current.Blocks[blockIndex]) {
			continue
		}
		streamAppendPosition = position
		streamBlockIndex = blockIndex
		break
	}
	if streamAppendPosition < 0 {
		if err := m.steerNativeStableAppendBlocks(current, previous, appendBlocks); err != nil {
			return err
		}
		m.nativeDeliveredStableProjection = nativeStableProjectionWithAppendedBlocks(previous, current, appendBlocks)
		return nil
	}
	preStreamAppendBlocks := appendBlocks[:streamAppendPosition]
	for _, blockIndex := range preStreamAppendBlocks {
		if blockIndex >= len(current.Blocks) || !nativeStableCurrentLocalAppendOnlyBlock(current.Blocks[blockIndex]) {
			return m.nativeStableProjectionDeliveryError(intent, nativeStableProjectionActiveStreamMismatchReason, previous, current)
		}
	}
	if streamBlockIndex >= len(current.Blocks) {
		return m.nativeStableProjectionDeliveryError(intent, nativeStableProjectionActiveStreamMismatchReason, previous, current)
	}
	if !m.nativeAssistantStreamMatchesProjectionBlock(nativeAssistantStreamText, current.Blocks[streamBlockIndex]) {
		return m.nativeStableProjectionDeliveryError(intent, nativeStableProjectionActiveStreamMismatchReason, previous, current)
	}
	if err := m.finishNativeAssistantStreaming(); err != nil {
		return err
	}
	if nativeAssistantStreamWasIncomplete {
		return m.steerNativeStableRuntimeProjectionChange(intent, previous, current)
	}
	streamDeliveredBlocks := []int{streamBlockIndex}
	streamDeliveredProjection := nativeStableProjectionWithAppendedBlocks(previous, current, streamDeliveredBlocks)
	appendBlocks = append(append([]int(nil), preStreamAppendBlocks...), appendBlocks[streamAppendPosition+1:]...)
	if err := m.steerNativeStableAppendBlocks(current, streamDeliveredProjection, appendBlocks); err != nil {
		return err
	}
	deliveredAppendBlocks := append(streamDeliveredBlocks, appendBlocks...)
	m.nativeDeliveredStableProjection = nativeStableProjectionWithAppendedBlocks(previous, current, deliveredAppendBlocks)
	return nil
}

func nativeStableProjectionBlockCanFinalizeAssistantStream(block tui.TranscriptProjectionBlock) bool {
	return block.Role == tui.RenderIntentAssistant || block.Role == tui.RenderIntentAssistantCommentary
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
