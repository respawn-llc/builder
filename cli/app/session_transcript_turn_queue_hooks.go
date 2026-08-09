package app

import (
	"sync"

	"core/shared/clientui"
)

type turnQueueHook interface {
	OnTranscriptMessage(clientui.TranscriptMessage)
	OnTurnQueueDrained()
	OnTurnQueueAborted()
	OnTurnSubmissionAborted()
	OnUserCompactionCompleted(bool)
}

type taskCompletionSink interface {
	enqueueTaskCompletion(clientui.TranscriptLiveRunResult)
}

type turnQueueHooks struct {
	mu                     sync.Mutex
	notifications          *bellHooks
	taskCompletions        taskCompletionSink
	pendingTaskCompletions []clientui.TranscriptLiveRunResult
}

func newTurnQueueHooks(
	notifications *bellHooks,
	taskCompletions taskCompletionSink,
) *turnQueueHooks {
	return &turnQueueHooks{
		notifications:   notifications,
		taskCompletions: taskCompletions,
	}
}

func (h *turnQueueHooks) OnTranscriptMessage(message clientui.TranscriptMessage) {
	if h == nil {
		return
	}
	if h.notifications != nil {
		h.notifications.OnTranscriptMessage(message)
	}
	if h.taskCompletions == nil || message.Kind() != clientui.TranscriptMessageLiveRunFinished {
		return
	}
	result := message.Payload().(clientui.TranscriptLiveRunResult)
	if result.Status != clientui.LiveRunStatusCompleted ||
		result.ResultKind != clientui.LiveRunResultAssistantFinalAnswer ||
		result.FinalAnswer == nil {
		return
	}
	h.mu.Lock()
	h.pendingTaskCompletions = append(h.pendingTaskCompletions, result)
	h.mu.Unlock()
}

func (h *turnQueueHooks) OnTurnQueueDrained() {
	if h == nil {
		return
	}
	h.flushTaskCompletions()
	if h.notifications != nil {
		h.notifications.OnTurnQueueDrained()
	}
}

func (h *turnQueueHooks) flushTaskCompletions() {
	h.mu.Lock()
	pending := h.pendingTaskCompletions
	h.pendingTaskCompletions = nil
	h.mu.Unlock()
	for _, result := range pending {
		h.taskCompletions.enqueueTaskCompletion(result)
	}
}

func (h *turnQueueHooks) OnTurnQueueAborted() {
	if h == nil || h.notifications == nil {
		return
	}
	h.notifications.OnTurnQueueAborted()
}

func (h *turnQueueHooks) OnTurnSubmissionAborted() {
	if h == nil {
		return
	}
	h.flushTaskCompletions()
	if h.notifications != nil {
		h.notifications.OnTurnQueueAborted()
	}
}

func (h *turnQueueHooks) OnUserCompactionCompleted(queueDrained bool) {
	if h == nil || h.notifications == nil {
		return
	}
	h.notifications.OnUserCompactionCompleted(queueDrained)
}
