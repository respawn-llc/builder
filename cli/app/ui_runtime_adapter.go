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
	projectedState := newProjectedTranscriptEventState(projectedTranscriptEventSnapshotFromModel(m))
	skipDeferredTailMerge := projectedEventIsLiveOnlyUnresolvedToolStart(projectedState, evt)
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
	cmds = append(cmds, a.applyRuntimeEventReduction(reduction))
	cmds = append(cmds, a.reconcileInterruptFromRunState(evt))
	transcriptMutated := false
	awaitsHydration := false
	if len(evt.TranscriptEntries) > 0 {
		cmd, mutated, needsHydration := a.applyProjectedTranscriptEntries(evt)
		cmds = append(cmds, cmd)
		transcriptMutated = transcriptMutated || mutated
		awaitsHydration = awaitsHydration || needsHydration
		streamFinalizer := mutated && isAssistantStreamFinalizerEvent(projectedState, evt)
		if (shouldClearAssistantStreamForCommittedAssistantEvent(evt, m.view.OngoingStreamingText()) && (mutated || skippedAssistantCommitMatchesActiveLiveStream(m, evt))) || streamFinalizer {
			if stepID := strings.TrimSpace(evt.StepID); stepID != "" {
				m.lastCommittedAssistantStepID = stepID
			}
			if err := m.finishNativeAssistantStreaming(); err != nil {
				cmds = append(cmds, m.nativeSurfaceErrorCmd("finish assistant stream", err))
			}
			m.sawAssistantDelta = false
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
	if transcriptSync.Reason != runtimestate.RuntimeTranscriptSyncNone {
		syncDecision := a.syncConversationFromRuntimeTranscriptCommand(transcriptSync)
		cmds = append(cmds, syncDecision.cmd)
		awaitsHydration = awaitsHydration || syncDecision.awaitsHydration
	} else if shouldRefreshDeferredCommittedTailOnRunEnd(m, evt) {
		cmds = append(cmds, m.requestRuntimeCommittedConversationSync())
	}
	return runtimeEventApplyResult{cmd: batchCmds(cmds...), transcriptMutated: transcriptMutated, awaitsHydration: awaitsHydration}
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
		if m.nativeSurface.StableBuffer() == nil {
			m.nativeAssistantStreamIncomplete = true
			return false, nil
		}
		return true, m.nativeSurface.StreamAssistantCommentaryContent(delta)
	case clientui.MessagePhaseFinal:
		if m.nativeAssistantStreamIncomplete {
			return false, nil
		}
		if m.nativeSurface.StableBuffer() == nil {
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
	if m.nativeSurface.StableBuffer() == nil {
		return nil
	}
	return m.nativeSurface.FinishAssistantStreaming()
}

func (m *uiModel) deliverNativeStableProjectionChange(previous tui.TranscriptProjection, current tui.TranscriptProjection, nativeStableReady bool, nativeAssistantStreamActive bool, nativeAssistantStreamWasIncomplete bool) error {
	if m == nil {
		return nil
	}
	nativeStableNeedsDelivery := nativeStableProjectionNeedsDelivery(previous, current)
	if !nativeStableNeedsDelivery {
		if nativeAssistantStreamActive {
			return m.finishNativeAssistantStreaming()
		}
		return nil
	}
	if !nativeStableReady {
		m.nativeAssistantStreamIncomplete = strings.TrimSpace(m.view.OngoingStreamingText()) != ""
		return nil
	}
	if !nativeAssistantStreamActive {
		return m.steerNativeStableAppend(previous, current)
	}
	if err := m.finishNativeAssistantStreaming(); err != nil {
		return err
	}
	if nativeAssistantStreamWasIncomplete {
		return m.steerNativeStableAppend(previous, current)
	}
	if _, ok := current.RenderAppendDeltaFrom(previous, tui.TranscriptDivider); !ok {
		return errors.New("native stable append is not contiguous with current transcript projection")
	}
	return m.steerNativeStableAppendFromBlock(current, len(previous.Blocks)+1)
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
