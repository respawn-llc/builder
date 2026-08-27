package runtime

import (
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"

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
	PendingWork             runtimeinput.PendingWork
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
	StepID    string
	RequestID *runtimeids.CompactionRequestID
	Mode      string
	Count     int
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
	thinkingStatus, reasoningTraces := e.transcriptRuntimeState().ReasoningSnapshot()
	pendingWork, err := e.PendingWorkSnapshot()
	if err != nil {
		e.surfaceRunError(err)
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
		PendingWork:             pendingWork,
		ActiveCompaction:        e.compactionRuntimeState().ActiveSnapshot(),
		CompactionCount:         e.CompactionCount(),
		ContextUsage:            &usage,
		Goal:                    e.Goal(),
		GoalSuspended:           e.GoalLoopSuspended(),
	}
}
