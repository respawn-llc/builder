package runtime

import (
	"core/server/llm"
	"core/server/session"

	"github.com/google/uuid"
)

type TranscriptHydrationSnapshot struct {
	CommittedRows           []TranscriptCommittedRowFact
	ActiveAssistantText     string
	ActiveAssistantMetadata *AssistantStreamMetadata
	ActiveAssistantStreamID *uuid.UUID
	ActiveAssistantPhase    llm.MessagePhase
	ActiveReasoning         *TranscriptReasoningState
	InFlightTools           []TranscriptLiveToolStart
	QueuedMessages          []QueuedUserMessage
	ActiveReviewer          *TranscriptReviewerState
	ActiveCompaction        *TranscriptCompactionState
	CompactionCount         int
	ContextUsage            *ContextUsage
	Goal                    *session.GoalState
	GoalSuspended           bool
}

type TranscriptReasoningState struct {
	StepID        string
	Key           string
	Text          string
	CurrentStatus *llm.ReasoningStatus
}

type TranscriptReviewerState struct {
	StepID string
}

type TranscriptCompactionState struct {
	StepID string
	Mode   string
	Count  int
}

func (e *Engine) WithTranscriptHydrationSnapshot(fn func(TranscriptHydrationSnapshot) error) error {
	if e == nil {
		if fn == nil {
			return nil
		}
		return fn(TranscriptHydrationSnapshot{})
	}
	e.outputMutationMu.Lock()
	defer e.outputMutationMu.Unlock()
	if fn == nil {
		return nil
	}
	e.ensureOrchestrationCollaborators()
	return fn(e.transcriptHydrationSegmentLocked())
}

func (e *Engine) transcriptHydrationSegmentLocked() TranscriptHydrationSnapshot {
	if e == nil {
		return TranscriptHydrationSnapshot{}
	}
	chat := e.transcriptRuntimeState().chatProjection()
	if chat == nil {
		return TranscriptHydrationSnapshot{}
	}
	snapshot := chat.deliverySnapshot()
	reasoning := e.transcriptRuntimeState().ReasoningSnapshot()
	var queuedMessages []QueuedUserMessage
	if e.messageFlow != nil {
		queuedMessages = e.messageFlow.PendingUserMessages()
	}
	usage := e.ContextUsage()
	return TranscriptHydrationSnapshot{
		CommittedRows:           snapshot.Rows,
		ActiveAssistantText:     snapshot.Streaming,
		ActiveAssistantMetadata: cloneAssistantStreamMetadata(snapshot.StreamingMetadata),
		ActiveAssistantStreamID: cloneTranscriptStreamID(snapshot.StreamingStreamID),
		ActiveAssistantPhase:    snapshot.StreamingPhase,
		ActiveReasoning:         reasoning,
		InFlightTools:           e.transcriptRuntimeState().LiveToolSnapshot(),
		QueuedMessages:          queuedMessages,
		ActiveReviewer:          e.reviewerRuntimeState().ActiveStepSnapshot(),
		ActiveCompaction:        e.compactionRuntimeState().ActiveSnapshot(),
		CompactionCount:         e.CompactionCount(),
		ContextUsage:            &usage,
		Goal:                    e.Goal(),
		GoalSuspended:           e.GoalLoopSuspended(),
	}
}
