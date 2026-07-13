package runtime

import (
	"encoding/json"
	"fmt"

	"core/server/llm"
	"core/server/session"
	"core/shared/rollbacktarget"
	"core/shared/valuecopy"
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
	t.carried = valuecopy.Pointer(payload.LatestRollbackCandidate)
}

func (t rollbackCandidateLocatorTracker) Resolve(activeWindowEndByte int64) (*rollbacktarget.CandidateLocator, error) {
	if t.latestActiveUserSeq == nil {
		return valuecopy.Pointer(t.carried), nil
	}
	locator := rollbacktarget.CandidateLocator{
		UserMessageSeq:       *t.latestActiveUserSeq,
		CandidatePageEndByte: activeWindowEndByte,
	}
	if err := locator.Validate(); err != nil {
		return nil, err
	}
	return &locator, nil
}

func rollbackCandidateLocatorFromActiveWindow(window session.SegmentWindow) (*rollbacktarget.CandidateLocator, error) {
	var tracker rollbackCandidateLocatorTracker
	for _, event := range window.Events {
		switch event.Kind {
		case "message":
			var message llm.Message
			if err := json.Unmarshal(event.Payload, &message); err != nil {
				return nil, fmt.Errorf("decode message event for rollback candidate locator: %w", err)
			}
			if err := tracker.ObserveMessage(event.Seq, message); err != nil {
				return nil, err
			}
		case sessionEventHistoryReplaced:
			payload, ignoredLegacy, err := decodePersistedHistoryReplacementPayload(event.Payload)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", errDecodeHistoryReplacedEvent, err)
			}
			if !ignoredLegacy {
				tracker.ObserveHistoryReplacement(payload)
			}
		}
	}
	return tracker.Resolve(window.EndOffset)
}
