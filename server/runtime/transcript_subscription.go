package runtime

import (
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

type TranscriptHydrationSnapshot struct {
	CommittedRows           []TranscriptCommittedRowFact
	ActiveAssistantText     string
	ActiveAssistantMetadata *AssistantStreamMetadata
	ActiveAssistantStreamID *uuid.UUID
	ActiveAssistantPhase    llm.MessagePhase
	ActiveThinkingStatus    *TranscriptThinkingStatusState
	ActiveReasoningTraces   []TranscriptReasoningTraceState
	InFlightTools           []TranscriptLiveToolStart
	QueuedMessages          []QueuedUserMessage
	ActiveCompaction        *TranscriptCompactionState
	CompactionCount         int
	ContextUsage            *ContextUsage
	Goal                    *session.GoalState
	GoalSuspended           bool
}

type TranscriptThinkingStatusState struct {
	StepID string
	Text   string
}

type TranscriptReasoningTraceIdentity struct {
	Provider *llm.ReasoningItemIdentity
	Kent     *runtimeids.ReasoningTraceID
}

type TranscriptReasoningTraceState struct {
	StepID           string
	Source           llm.ReasoningSourceCoordinate
	Identity         TranscriptReasoningTraceIdentity
	ProviderMetadata *llm.ReasoningItemIdentity
	Text             string
	startedAt        time.Time
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
	thinkingStatus, reasoningTraces := e.transcriptRuntimeState().ReasoningSnapshot()
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
		ActiveThinkingStatus:    thinkingStatus,
		ActiveReasoningTraces:   reasoningTraces,
		InFlightTools:           e.transcriptRuntimeState().LiveToolSnapshot(),
		QueuedMessages:          queuedMessages,
		ActiveCompaction:        e.compactionRuntimeState().ActiveSnapshot(),
		CompactionCount:         e.CompactionCount(),
		ContextUsage:            &usage,
		Goal:                    e.Goal(),
		GoalSuspended:           e.GoalLoopSuspended(),
	}
}
