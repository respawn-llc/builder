package runtime

import (
	"fmt"

	"core/server/llm"
	"core/server/session"
	"core/shared/rollbacktarget"
	"core/shared/textutil"
)

type rollbackCandidateLocatorTracker struct {
	carried             *rollbacktarget.CandidateLocator
	latestActiveUserSeq *int64
}

func (t *rollbackCandidateLocatorTracker) ObserveMessage(seq int64, msg llm.Message) error {
	if t == nil || !isRollbackCandidateMessage(msg) {
		return nil
	}
	if seq <= 0 {
		return fmt.Errorf("rollback candidate message has nonpositive event sequence %d", seq)
	}
	seqCopy := seq
	t.latestActiveUserSeq = &seqCopy
	return nil
}

func (t *rollbackCandidateLocatorTracker) ObserveHistoryReplacement(payload historyReplacementPayload) {
	if t == nil {
		return
	}
	t.carried = textutil.Pointer(payload.LatestRollbackCandidate)
}

func (t rollbackCandidateLocatorTracker) Resolve(activeWindowEndByte int64) (*rollbacktarget.CandidateLocator, error) {
	if t.latestActiveUserSeq == nil {
		return textutil.Pointer(t.carried), nil
	}
	// A segment window has at most one history-replacement boundary and it is
	// first, so an observed active user is always newer than the carried locator.
	locator := rollbacktarget.CandidateLocator{
		UserMessageSeq:       *t.latestActiveUserSeq,
		CandidatePageEndByte: activeWindowEndByte,
	}
	if err := locator.Validate(); err != nil {
		return nil, err
	}
	return &locator, nil
}

func rollbackCandidateLocatorFromActiveWindow(window session.EventRecordWindow) (*rollbacktarget.CandidateLocator, error) {
	var tracker rollbackCandidateLocatorTracker
	for _, record := range window.Records {
		payload, err := record.Payload()
		if err != nil {
			return nil, err
		}
		switch payload := payload.(type) {
		case session.MessageRecord:
			message, err := llmMessageFromSessionRecord(payload)
			if err != nil {
				return nil, fmt.Errorf("restore session message record for rollback candidate locator: %w", err)
			}
			if err := tracker.ObserveMessage(record.Seq(), message); err != nil {
				return nil, err
			}
		case session.HistoryReplacementRecord:
			replacement, err := historyReplacementPayloadFromSessionRecord(payload)
			if err != nil {
				return nil, fmt.Errorf("restore session history replacement record: %w", err)
			}
			tracker.ObserveHistoryReplacement(replacement)
		}
	}
	return tracker.Resolve(window.EndOffset)
}
