package session

import (
	"context"
	"path/filepath"
	"strings"
)

// PersistedSessionView is a bounded read-only projection captured from one
// persisted metadata record and one exact event-log boundary.
type PersistedSessionView struct {
	meta     Meta
	eventLog *currentEventLog
}

func ResolvePersistedSessionView(
	ctx context.Context,
	resolver PersistedSessionResolver,
	sessionID string,
) (*PersistedSessionView, error) {
	record, err := ResolvePersistedSessionRecord(ctx, resolver, sessionID)
	if err != nil {
		return nil, err
	}
	meta := *record.Meta
	eventLog, err := openCurrentEventLog(
		filepath.Join(record.SessionDir, eventsFile),
		currentEventLogPersistedSnapshot,
	)
	if err != nil {
		return nil, err
	}
	if eventLog.boundaryIncomplete || eventLog.lastSequence != meta.LastSequence {
		return nil, EventLogReconciliationConflictError{
			SessionID:            strings.TrimSpace(meta.SessionID),
			ObservedLastSequence: eventLog.lastSequence,
			CurrentLastSequence:  meta.LastSequence,
			BoundaryIncomplete:   eventLog.boundaryIncomplete,
		}
	}
	eventLog.frozenEndOffset = &eventLog.lastCompleteOffset
	return &PersistedSessionView{meta: meta, eventLog: eventLog}, nil
}

func (v *PersistedSessionView) Meta() Meta {
	return cloneMeta(v.meta)
}

func (v *PersistedSessionView) ConversationFreshness() ConversationFreshness {
	if v.meta.ConversationEstablished {
		return ConversationFreshnessEstablished
	}
	return ConversationFreshnessFresh
}

func (v *PersistedSessionView) ReadNewestSegmentBackward(match func(EventRecord) bool) (EventRecordWindow, error) {
	return v.eventLog.readNewestSegmentBackward(activeTailReverseChunkBytes, match)
}

func (v *PersistedSessionView) ReadSegmentBackward(endOffset int64, match func(EventRecord) bool) (EventRecordWindow, error) {
	return v.eventLog.readSegmentBackward(endOffset, activeTailReverseChunkBytes, match)
}

func (v *PersistedSessionView) ReadSegmentForward(startOffset int64, match func(EventRecord) bool) (EventRecordWindow, error) {
	return v.eventLog.readSegmentForward(startOffset, activeTailReverseChunkBytes, match)
}
