package runtime

import (
	"fmt"
	"strings"

	"core/server/session"
)

type TranscriptCommittedRowProvenance struct {
	EventSequence     int64
	CommittedAtUnixMs *int64
	ToolCallID        string
	ProjectedOrdinal  *int64
}

func transcriptCommittedProvenanceBefore(
	left *TranscriptCommittedRowProvenance,
	right *TranscriptCommittedRowProvenance,
) bool {
	if left == nil || right == nil {
		return false
	}
	if left.EventSequence != right.EventSequence {
		return left.EventSequence < right.EventSequence
	}
	if left.ProjectedOrdinal != nil && right.ProjectedOrdinal != nil {
		return *left.ProjectedOrdinal < *right.ProjectedOrdinal
	}
	return false
}

func transcriptProvenanceFromRecord(record session.EventRecord) (TranscriptCommittedRowProvenance, error) {
	if record.Seq() <= 0 {
		return TranscriptCommittedRowProvenance{}, fmt.Errorf("committed transcript provenance has invalid event sequence %d", record.Seq())
	}
	payload, err := record.Payload()
	if err != nil {
		return TranscriptCommittedRowProvenance{}, err
	}
	provenance := TranscriptCommittedRowProvenance{
		EventSequence:     record.Seq(),
		CommittedAtUnixMs: record.CommittedAtUnixMs(),
	}
	switch typed := payload.(type) {
	case session.MessageRecord:
		if typed.ToolCallID != nil {
			provenance.ToolCallID = strings.TrimSpace(*typed.ToolCallID)
		}
	case session.ToolCompletionRecord:
		provenance.ToolCallID = strings.TrimSpace(typed.CallID)
	case session.LocalEntryRecord:
		if typed.AfterToolCallID != nil {
			provenance.ToolCallID = strings.TrimSpace(*typed.AfterToolCallID)
		}
	}
	return provenance, nil
}

func cloneTranscriptCommittedRowProvenance(value *TranscriptCommittedRowProvenance) *TranscriptCommittedRowProvenance {
	if value == nil {
		return nil
	}
	copyValue := *value
	if value.CommittedAtUnixMs != nil {
		committedAt := *value.CommittedAtUnixMs
		copyValue.CommittedAtUnixMs = &committedAt
	}
	if value.ProjectedOrdinal != nil {
		ordinal := *value.ProjectedOrdinal
		copyValue.ProjectedOrdinal = &ordinal
	}
	return &copyValue
}
