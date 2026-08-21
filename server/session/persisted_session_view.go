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
	eventLog, err := openPersistedSessionEventLog(record.SessionDir, *record.Meta)
	if err != nil {
		return nil, err
	}
	return &PersistedSessionView{
		meta:     *record.Meta,
		eventLog: eventLog,
	}, nil
}

func openPersistedSessionEventLog(sessionDir string, meta Meta) (*currentEventLog, error) {
	path := filepath.Join(sessionDir, eventsFile)
	eventLog, err := openCurrentEventLog(path, currentEventLogPersistedSnapshot)
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
	return eventLog, nil
}

func (v *PersistedSessionView) Meta() Meta {
	return cloneMeta(v.meta)
}

func (v *PersistedSessionView) Revision() int64 {
	return v.eventLog.lastSequence
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
