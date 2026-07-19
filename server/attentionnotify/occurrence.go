package attentionnotify

import (
	"crypto/sha256"
	"strings"
)

type OrdinaryOccurrenceOrdinal uint64

type OrdinaryOccurrenceWatermark uint64

type TaskQuestionBatchKey [sha256.Size]byte

type OccurrenceMetadata struct {
	ordinaryOrdinal *OrdinaryOccurrenceOrdinal
	taskBatchKey    *TaskQuestionBatchKey
}

func NewOrdinaryOccurrenceMetadata(ordinal OrdinaryOccurrenceOrdinal) OccurrenceMetadata {
	if ordinal == 0 {
		panic("ordinary attention occurrence ordinal must be positive")
	}
	return OccurrenceMetadata{ordinaryOrdinal: &ordinal}
}

func NewTaskQuestionBatchOccurrenceMetadata(batchID string) OccurrenceMetadata {
	trimmed := strings.TrimSpace(batchID)
	if trimmed == "" {
		panic("task question batch id is required for attention occurrence")
	}
	key := TaskQuestionBatchKey(sha256.Sum256([]byte(trimmed)))
	return OccurrenceMetadata{taskBatchKey: &key}
}

func (m OccurrenceMetadata) OrdinaryOrdinal() (OrdinaryOccurrenceOrdinal, bool) {
	if m.ordinaryOrdinal == nil {
		return 0, false
	}
	return *m.ordinaryOrdinal, true
}

func (m OccurrenceMetadata) TaskQuestionBatchKey() (TaskQuestionBatchKey, bool) {
	if m.taskBatchKey == nil {
		return TaskQuestionBatchKey{}, false
	}
	return *m.taskBatchKey, true
}

func (m OccurrenceMetadata) clone() OccurrenceMetadata {
	out := OccurrenceMetadata{}
	if m.ordinaryOrdinal != nil {
		ordinal := *m.ordinaryOrdinal
		out.ordinaryOrdinal = &ordinal
	}
	if m.taskBatchKey != nil {
		key := *m.taskBatchKey
		out.taskBatchKey = &key
	}
	return out
}
