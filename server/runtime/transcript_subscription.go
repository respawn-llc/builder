package runtime

import (
	"core/server/llm"

	"github.com/google/uuid"
)

type TranscriptHydrationSnapshot struct {
	CommittedRows           []TranscriptCommittedRowFact
	ActiveAssistantText     string
	ActiveAssistantMetadata *AssistantStreamMetadata
	ActiveAssistantStreamID *uuid.UUID
	ActiveAssistantPhase    llm.MessagePhase
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
	return TranscriptHydrationSnapshot{
		CommittedRows:           snapshot.Rows,
		ActiveAssistantText:     snapshot.Streaming,
		ActiveAssistantMetadata: cloneAssistantStreamMetadata(snapshot.StreamingMetadata),
		ActiveAssistantStreamID: cloneTranscriptStreamID(snapshot.StreamingStreamID),
		ActiveAssistantPhase:    snapshot.StreamingPhase,
	}
}
