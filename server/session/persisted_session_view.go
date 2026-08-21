package session

import (
	"context"
	"errors"
	"fmt"
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

func openPersistedSessionEventLog(sessionDir string, meta Meta) (_ *currentEventLog, resultErr error) {
	path := filepath.Join(sessionDir, eventsFile)
	fp, err := openRegularSessionFile(path, "current event log")
	if err != nil {
		return nil, fmt.Errorf("open current event log: %w", err)
	}
	defer func() {
		if closeErr := fp.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close current event log: %w", closeErr))
		}
	}()
	info, err := fp.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat current event log: %w", err)
	}
	size := info.Size()
	if size == 0 {
		if meta.LastSequence != 0 {
			return nil, eventLogBoundaryConflict(meta, 0)
		}
		frozen := int64(0)
		return &currentEventLog{
			path:             path,
			version:          EventLogVersionV2,
			firstEventOffset: 0,
			frozenEndOffset:  &frozen,
			mode:             currentEventLogReadOnly,
		}, nil
	}
	header, firstEventOffset, err := readCurrentEventLogHeader(fp)
	if err != nil {
		return nil, persistedEventLogFormatError(err)
	}
	lastRecord, endOffset, tornTail, err := readLastCurrentEventRecordBoundary(
		fp,
		size,
		firstEventOffset,
		true,
	)
	if err != nil {
		return nil, persistedEventLogFormatError(err)
	}
	observedSequence := int64(0)
	if lastRecord != nil {
		observedSequence = lastRecord.Seq()
	}
	if tornTail {
		conflict := eventLogBoundaryConflict(meta, observedSequence)
		conflict.BoundaryIncomplete = true
		return nil, conflict
	}
	if observedSequence != meta.LastSequence {
		return nil, eventLogBoundaryConflict(meta, observedSequence)
	}
	frozen := endOffset
	return &currentEventLog{
		path:             path,
		version:          header.Version,
		firstEventOffset: firstEventOffset,
		lastSequence:     observedSequence,
		frozenEndOffset:  &frozen,
		mode:             currentEventLogReadOnly,
	}, nil
}

func persistedEventLogFormatError(err error) error {
	return wrapEventLogMaterializationError(
		EventLogMaterializationStageReconciliation,
		false,
		false,
		err,
	)
}

func eventLogBoundaryConflict(meta Meta, observedSequence int64) EventLogReconciliationConflictError {
	return EventLogReconciliationConflictError{
		SessionID:            strings.TrimSpace(meta.SessionID),
		ObservedLastSequence: observedSequence,
		CurrentLastSequence:  meta.LastSequence,
	}
}

func (v *PersistedSessionView) Meta() Meta {
	return cloneMeta(v.meta)
}

func (v *PersistedSessionView) Revision() (int64, error) {
	return v.eventLog.lastSequence, nil
}

func (v *PersistedSessionView) ConversationFreshness() (ConversationFreshness, error) {
	if v.meta.ConversationEstablished {
		return ConversationFreshnessEstablished, nil
	}
	return ConversationFreshnessFresh, nil
}

func (v *PersistedSessionView) ReadRecentRecords(maxRecords int) (EventRecordWindow, error) {
	return v.eventLog.readRecentRecords(maxRecords, activeTailReverseChunkBytes)
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
