package runtimestate

import (
	"strings"

	"core/shared/clientui"
)

type RuntimeRunState struct {
	Run        clientui.RunLifecycle
	Compaction clientui.CompactionLifecycle
	Reviewer   clientui.ReviewerLifecycle
}

type RuntimeConversationState struct {
	Freshness clientui.ConversationFreshness
}

type RuntimeReasoningState struct {
	StatusHeader string
}

type PendingInputState struct {
	PendingInjected []clientui.QueuedUserMessage
}

type BackgroundNoticeKind uint8

const (
	BackgroundNoticeSuccess BackgroundNoticeKind = iota + 1
	BackgroundNoticeError
)

type BackgroundNotice struct {
	Message string
	Kind    BackgroundNoticeKind
}

type RuntimeTranscriptSyncReason string

const (
	RuntimeTranscriptSyncNone                  RuntimeTranscriptSyncReason = ""
	RuntimeTranscriptSyncStreamGap             RuntimeTranscriptSyncReason = "stream_gap"
	RuntimeTranscriptSyncCommittedAdvance      RuntimeTranscriptSyncReason = "committed_advance"
	RuntimeTranscriptSyncRecovery              RuntimeTranscriptSyncReason = "recovery"
	RuntimeTranscriptSyncStreamingErrorUpdated RuntimeTranscriptSyncReason = "streaming_error_updated"
)

type RuntimeTranscriptSyncCommand struct {
	Reason        RuntimeTranscriptSyncReason
	RecoveryCause clientui.TranscriptRecoveryCause
}

type RuntimeAssistantStreamCommandKind uint8

const (
	RuntimeAssistantStreamAppend RuntimeAssistantStreamCommandKind = iota + 1
	RuntimeAssistantStreamClear
)

type RuntimeAssistantStreamCommand struct {
	Kind                    RuntimeAssistantStreamCommandKind
	Delta                   string
	Phase                   clientui.MessagePhase
	StepID                  string
	AssistantStreamMetadata *clientui.AssistantStreamMetadata
}

type RuntimeTranscriptReduction struct {
	Sync            RuntimeTranscriptSyncCommand
	AssistantStream []RuntimeAssistantStreamCommand
}

type RuntimeActivityCommand uint8

const (
	RuntimeActivityUnchanged RuntimeActivityCommand = iota
	RuntimeActivityRunning
	RuntimeActivityIdle
)

type RuntimeRunStateReduction struct {
	State           RuntimeRunState
	Activity        RuntimeActivityCommand
	RuntimeActivity *clientui.RuntimeActivity
	Err             error
}

type RuntimePendingInputReduction struct {
	State                PendingInputState
	ConsumedQueueItemIDs []string
	RestoredText         string
}

type RuntimeReasoningStreamCommandKind uint8

const (
	RuntimeReasoningStreamUpsert RuntimeReasoningStreamCommandKind = iota + 1
	RuntimeReasoningStreamClear
)

type RuntimeReasoningStreamCommand struct {
	Kind  RuntimeReasoningStreamCommandKind
	Delta *clientui.ReasoningDelta
}

type RuntimeReasoningReduction struct {
	State  RuntimeReasoningState
	Stream []RuntimeReasoningStreamCommand
}

type RuntimeBackgroundProcessCommand uint8

const (
	RuntimeBackgroundProcessUnchanged RuntimeBackgroundProcessCommand = iota
	RuntimeBackgroundProcessRefresh
)

type RuntimeNoticeReduction struct {
	BackgroundNotice    *BackgroundNotice
	DiagnosticNotice    *BackgroundNotice
	TransientDiagnostic *BackgroundNotice
}

type RuntimeBackgroundProcessReduction struct {
	Command RuntimeBackgroundProcessCommand
}

type RuntimeConversationReduction struct {
	State RuntimeConversationState
}

type RuntimeEventReduction struct {
	Transcript          RuntimeTranscriptReduction
	RunState            RuntimeRunStateReduction
	Conversation        RuntimeConversationReduction
	PendingInput        RuntimePendingInputReduction
	Reasoning           RuntimeReasoningReduction
	BackgroundProcesses RuntimeBackgroundProcessReduction
	Notices             RuntimeNoticeReduction
}

func ReduceRuntimeEvent(
	runState RuntimeRunState,
	conversationState RuntimeConversationState,
	input PendingInputState,
	reasoningState RuntimeReasoningState,
	activityRunning bool,
	evt clientui.Event,
) RuntimeEventReduction {
	conversationReduction := RuntimeConversationReduction{State: conversationState}
	if evt.Kind == clientui.EventUserMessageFlushed {
		conversationReduction = RuntimeConversationReduction{State: RuntimeConversationState{Freshness: clientui.ConversationFreshnessEstablished}}
	}
	backgroundProcessReduction := RuntimeBackgroundProcessReduction{}
	if evt.Kind == clientui.EventBackgroundUpdated {
		backgroundProcessReduction = RuntimeBackgroundProcessReduction{Command: RuntimeBackgroundProcessRefresh}
	}
	return RuntimeEventReduction{
		Transcript:          ReduceRuntimeTranscriptEvent(evt),
		RunState:            ReduceRuntimeRunStateEvent(runState, activityRunning, evt),
		Conversation:        conversationReduction,
		PendingInput:        ReduceRuntimePendingInputEvent(input, evt),
		Reasoning:           ReduceRuntimeReasoningEvent(reasoningState, evt),
		BackgroundProcesses: backgroundProcessReduction,
		Notices:             ReduceRuntimeNoticeEvent(evt),
	}
}

func ReduceRuntimeTranscriptEvent(evt clientui.Event) RuntimeTranscriptReduction {
	switch evt.Kind {
	case clientui.EventStreamGap:
		return RuntimeTranscriptReduction{Sync: RuntimeTranscriptSyncCommand{Reason: RuntimeTranscriptSyncStreamGap, RecoveryCause: evt.RecoveryCause}}
	case clientui.EventConversationUpdated:
		if evt.RecoveryCause != clientui.TranscriptRecoveryCauseNone {
			return RuntimeTranscriptReduction{Sync: RuntimeTranscriptSyncCommand{Reason: RuntimeTranscriptSyncRecovery, RecoveryCause: evt.RecoveryCause}}
		}
		if evt.CommittedTranscriptChanged {
			return RuntimeTranscriptReduction{Sync: RuntimeTranscriptSyncCommand{Reason: RuntimeTranscriptSyncCommittedAdvance}}
		}
	case clientui.EventStreamingErrorUpdated:
		return RuntimeTranscriptReduction{Sync: RuntimeTranscriptSyncCommand{Reason: RuntimeTranscriptSyncStreamingErrorUpdated}}
	case clientui.EventAssistantDelta:
		return RuntimeTranscriptReduction{AssistantStream: []RuntimeAssistantStreamCommand{{Kind: RuntimeAssistantStreamAppend, Delta: evt.AssistantDelta, Phase: evt.AssistantDeltaPhase, StepID: evt.StepID, AssistantStreamMetadata: cloneAssistantStreamMetadata(evt.AssistantStreamMetadata)}}}
	case clientui.EventAssistantDeltaReset:
		return RuntimeTranscriptReduction{AssistantStream: []RuntimeAssistantStreamCommand{{Kind: RuntimeAssistantStreamClear, StepID: evt.StepID, AssistantStreamMetadata: cloneAssistantStreamMetadata(evt.AssistantStreamMetadata)}}}
	}
	return RuntimeTranscriptReduction{}
}

func cloneAssistantStreamMetadata(metadata *clientui.AssistantStreamMetadata) *clientui.AssistantStreamMetadata {
	if metadata == nil {
		return nil
	}
	copyMetadata := *metadata
	return &copyMetadata
}

func ReduceRuntimeRunStateEvent(state RuntimeRunState, activityRunning bool, evt clientui.Event) RuntimeRunStateReduction {
	next := state
	reduction := RuntimeRunStateReduction{State: next}
	switch evt.Kind {
	case clientui.EventCompactionStarted:
		reduction.State.Compaction = clientui.NewCompactionLifecycle(true)
	case clientui.EventCompactionCompleted, clientui.EventCompactionFailed:
		reduction.State.Compaction = clientui.NewCompactionLifecycle(false)
	case clientui.EventReviewerStarted:
		reviewer, err := clientui.NewReviewerLifecycle(true, true)
		if err == nil {
			reduction.State.Reviewer = reviewer
		}
	case clientui.EventReviewerCompleted:
		reduction.State.Reviewer = clientui.ReviewerLifecycleIdle
	case clientui.EventRunStateChanged:
		if evt.RunState == nil {
			return reduction
		}
		if err := evt.RunState.Lifecycle.Validate(); err != nil {
			reduction.Err = err
			return reduction
		}
	case clientui.EventRuntimeActivityChanged:
		if evt.RuntimeActivity == nil {
			return reduction
		}
		reduction.RuntimeActivity = evt.RuntimeActivity
		if evt.RuntimeActivity.ActiveForControl() {
			reduction.State.Run = clientui.MustRunLifecycle(clientui.RunLifecycleRunning, runModeFromRuntimeActivityKind(evt.RuntimeActivity.ActiveKind))
			reduction.Activity = RuntimeActivityRunning
			return reduction
		}
		reduction.State.Run = clientui.IdleRunLifecycle()
		if activityRunning {
			reduction.Activity = RuntimeActivityIdle
		}
	}
	return reduction
}

func runModeFromRuntimeActivityKind(kind clientui.RuntimeActivityActiveKind) clientui.RunMode {
	if kind == clientui.RuntimeActivityActiveKindGoalLoop {
		return clientui.RunModeGoalLoop
	}
	return clientui.RunModeTurn
}

func ReduceRuntimePendingInputEvent(input PendingInputState, evt clientui.Event) RuntimePendingInputReduction {
	next := clonePendingInputState(input)
	reduction := RuntimePendingInputReduction{State: next}
	switch evt.Kind {
	case clientui.EventUserMessageFlushed:
		consumed := consumedQueuedUserMessages(reduction.State.PendingInjected, evt.UserMessageBatchQueueItemIDs)
		if len(consumed) > 0 {
			reduction.State.PendingInjected = append([]clientui.QueuedUserMessage(nil), reduction.State.PendingInjected[len(consumed):]...)
			reduction.ConsumedQueueItemIDs = append([]string(nil), evt.UserMessageBatchQueueItemIDs[:len(consumed)]...)
		}
	case clientui.EventQueuedUserMessageStatus:
		status := evt.QueuedUserMessageStatus
		if status == nil {
			break
		}
		switch status.Status {
		case clientui.QueuedUserMessageSubmitted, clientui.QueuedUserMessageDiscarded:
			if _, removed := removePendingQueuedUserMessageByStatus(&reduction.State.PendingInjected, status); removed {
				reduction.consumeQueuedStatusIDs(status)
			}
		case clientui.QueuedUserMessageFailed:
			if _, removed := removePendingQueuedUserMessageByStatus(&reduction.State.PendingInjected, status); removed {
				reduction.consumeQueuedStatusIDs(status)
				reduction.RestoredText = strings.TrimSpace(status.RestoreText)
			}
		}
	}
	return reduction
}

func (reduction *RuntimePendingInputReduction) consumeQueuedStatusIDs(status *clientui.QueuedUserMessageStatusEvent) {
	if reduction == nil || status == nil {
		return
	}
	if id := strings.TrimSpace(status.QueueItemID); id != "" {
		reduction.ConsumedQueueItemIDs = append(reduction.ConsumedQueueItemIDs, id)
	}
	if id := strings.TrimSpace(status.ClientRequestID); id != "" {
		reduction.ConsumedQueueItemIDs = append(reduction.ConsumedQueueItemIDs, id)
	}
}

func ReduceRuntimeReasoningEvent(state RuntimeReasoningState, evt clientui.Event) RuntimeReasoningReduction {
	reduction := RuntimeReasoningReduction{State: state}
	switch evt.Kind {
	case clientui.EventReasoningDelta:
		delta := cloneReasoningDelta(evt.ReasoningDelta)
		reduction.Stream = append(reduction.Stream, RuntimeReasoningStreamCommand{Kind: RuntimeReasoningStreamUpsert, Delta: delta})
		if delta != nil {
			if nextHeader := ExtractReasoningStatusHeader(delta.Text); nextHeader != "" {
				reduction.State.StatusHeader = nextHeader
			}
		}
	case clientui.EventReasoningDeltaReset:
		reduction.Stream = append(reduction.Stream, RuntimeReasoningStreamCommand{Kind: RuntimeReasoningStreamClear})
	case clientui.EventRunStateChanged:
		if evt.RunState != nil {
			if err := evt.RunState.Lifecycle.Validate(); err != nil {
				break
			}
			if !evt.RunState.Lifecycle.IsRunning() {
				reduction.State.StatusHeader = ""
				reduction.Stream = append(reduction.Stream, RuntimeReasoningStreamCommand{Kind: RuntimeReasoningStreamClear})
			}
		}
	}
	return reduction
}

func ReduceRuntimeNoticeEvent(evt clientui.Event) RuntimeNoticeReduction {
	switch evt.Kind {
	case clientui.EventBackgroundUpdated:
		notice := backgroundNoticeFromEvent(evt.Background)
		if notice == nil {
			return RuntimeNoticeReduction{}
		}
		return RuntimeNoticeReduction{BackgroundNotice: notice}
	case clientui.EventSleepGuardFailed:
		msg := strings.TrimSpace(evt.Error)
		if msg == "" {
			return RuntimeNoticeReduction{}
		}
		return RuntimeNoticeReduction{DiagnosticNotice: &BackgroundNotice{Message: "sleep prevention failed: " + msg, Kind: BackgroundNoticeError}}
	case clientui.EventPromptHistoryPersistFailed:
		msg := strings.TrimSpace(evt.Error)
		if msg == "" {
			return RuntimeNoticeReduction{}
		}
		return RuntimeNoticeReduction{TransientDiagnostic: &BackgroundNotice{Message: "prompt history persistence failed: " + msg, Kind: BackgroundNoticeError}}
	}
	return RuntimeNoticeReduction{}
}

func ExtractReasoningStatusHeader(text string) string {
	trimmed := strings.TrimSpace(text)
	bytes := []byte(trimmed)
	for i := 0; i+1 < len(bytes); i++ {
		if bytes[i] != '*' || bytes[i+1] != '*' {
			continue
		}
		start := i + 2
		for j := start; j+1 < len(bytes); j++ {
			if bytes[j] != '*' || bytes[j+1] != '*' {
				continue
			}
			inner := strings.TrimSpace(trimmed[start:j])
			if inner == "" {
				return ""
			}
			return inner
		}
		return ""
	}
	return ""
}

func clonePendingInputState(input PendingInputState) PendingInputState {
	cloned := input
	if len(input.PendingInjected) > 0 {
		cloned.PendingInjected = append([]clientui.QueuedUserMessage(nil), input.PendingInjected...)
	}
	return cloned
}

func consumedQueuedUserMessages(pending []clientui.QueuedUserMessage, ids []string) []clientui.QueuedUserMessage {
	if len(pending) == 0 || len(ids) == 0 {
		return nil
	}
	consumed := make([]clientui.QueuedUserMessage, 0, len(ids))
	for index, id := range ids {
		if index >= len(pending) || pending[index].ID != id {
			return consumed
		}
		consumed = append(consumed, pending[index])
	}
	return consumed
}

func removePendingQueuedUserMessageByStatus(messages *[]clientui.QueuedUserMessage, status *clientui.QueuedUserMessageStatusEvent) (clientui.QueuedUserMessage, bool) {
	if status == nil || messages == nil || len(*messages) == 0 {
		return clientui.QueuedUserMessage{}, false
	}
	removed := false
	var removedMessage clientui.QueuedUserMessage
	filtered := (*messages)[:0]
	for _, message := range *messages {
		if queuedUserMessageMatchesStatus(message, status) {
			removed = true
			removedMessage = message
			continue
		}
		filtered = append(filtered, message)
	}
	*messages = filtered
	return removedMessage, removed
}

func queuedUserMessageMatchesStatus(message clientui.QueuedUserMessage, status *clientui.QueuedUserMessageStatusEvent) bool {
	if status == nil {
		return false
	}
	localRequestID := strings.TrimSpace(message.ClientRequestID)
	statusRequestID := strings.TrimSpace(status.ClientRequestID)
	if localRequestID != "" {
		if statusRequestID != "" {
			return localRequestID == statusRequestID
		}
		return queuedUserMessageIDMatches(message.ID, status.QueueItemID)
	}
	return queuedUserMessageIDMatches(message.ID, status.QueueItemID)
}

func queuedUserMessageIDMatches(left, right string) bool {
	return strings.TrimSpace(left) != "" && strings.TrimSpace(left) == strings.TrimSpace(right)
}

func cloneReasoningDelta(delta *clientui.ReasoningDelta) *clientui.ReasoningDelta {
	if delta == nil {
		return nil
	}
	cloned := *delta
	return &cloned
}

func backgroundNoticeFromEvent(evt *clientui.BackgroundShellEvent) *BackgroundNotice {
	if evt == nil || evt.NoticeSuppressed {
		return nil
	}
	if evt.Type != "completed" && evt.Type != "killed" {
		return nil
	}
	message := strings.TrimSpace(evt.CompactText)
	if message == "" {
		message = "background shell " + evt.ID + " " + evt.State
	}
	notice := &BackgroundNotice{
		Message: message,
		Kind:    BackgroundNoticeSuccess,
	}
	if evt.Type == "killed" && !evt.UserRequestedKill {
		notice.Kind = BackgroundNoticeError
	}
	return notice
}
