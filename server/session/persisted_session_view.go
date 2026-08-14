package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PersistedSessionView is a bounded read-only projection captured from one
// persisted metadata record and one exact event-log boundary.
type PersistedSessionView struct {
	meta                  Meta
	contextFacts          SessionContextFacts
	conversationFreshness ConversationFreshness
	eventLog              *currentEventLog
}

func ResolvePersistedSessionView(
	ctx context.Context,
	resolver PersistedSessionResolver,
	sessionID string,
) (*PersistedSessionView, error) {
	if resolver == nil {
		return nil, errPersistedSessionResolverRequired
	}
	id := strings.TrimSpace(sessionID)
	record, err := resolver.ResolvePersistedSession(ctx, id)
	if err != nil {
		return nil, err
	}
	return OpenPersistedSessionView(id, record)
}

func OpenPersistedSessionView(sessionID string, record PersistedSessionRecord) (*PersistedSessionView, error) {
	if record.Meta == nil {
		return nil, errResolverRecordMissingMetadata
	}
	if err := validatePersistedSessionRecord(strings.TrimSpace(sessionID), record); err != nil {
		return nil, err
	}
	meta := cloneMeta(*record.Meta)
	if err := normalizeMetaContinuation(&meta); err != nil {
		return nil, fmt.Errorf("validate session continuation: %w", err)
	}
	if err := normalizeMetaChatSettings(&meta); err != nil {
		return nil, fmt.Errorf("validate session Chat settings: %w", err)
	}
	if meta.ActiveWorkflowAssignment != nil {
		assignment, err := normalizeMessageRecord(*meta.ActiveWorkflowAssignment)
		if err != nil {
			return nil, fmt.Errorf("validate active workflow assignment: %w", err)
		}
		meta.ActiveWorkflowAssignment = &assignment
	}
	if err := normalizeMetaWorktreeReminder(&meta); err != nil {
		return nil, fmt.Errorf("validate session worktree context: %w", err)
	}
	if err := validateMetaCategory(&meta); err != nil {
		return nil, err
	}
	eventLog, err := openPersistedSessionEventLog(record.SessionDir, meta)
	if err != nil {
		return nil, err
	}
	freshness := ConversationFreshnessFresh
	if meta.ConversationEstablished {
		freshness = ConversationFreshnessEstablished
	}
	return &PersistedSessionView{
		meta:                  meta,
		contextFacts:          normalizeSessionContextFacts(record.ContextFacts),
		conversationFreshness: freshness,
		eventLog:              eventLog,
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
	if v == nil {
		return Meta{}
	}
	return cloneMeta(v.meta)
}

func (v *PersistedSessionView) ContextFacts() SessionContextFacts {
	if v == nil {
		return SessionContextFacts{}
	}
	return v.contextFacts.Clone()
}

func (v *PersistedSessionView) ContextSnapshot() ContextSnapshot {
	return ContextSnapshot{Meta: v.Meta(), Facts: v.ContextFacts()}
}

func (v *PersistedSessionView) Revision() (int64, error) {
	if v == nil || v.eventLog == nil {
		return 0, errors.New("persisted Session event log is required")
	}
	return v.eventLog.lastSequence, nil
}

func (v *PersistedSessionView) ConversationFreshness() (ConversationFreshness, error) {
	if v == nil || v.eventLog == nil {
		return ConversationFreshnessFresh, errors.New("persisted Session event log is required")
	}
	return v.conversationFreshness, nil
}

func (v *PersistedSessionView) ReadRecentRecords(maxRecords int) (EventRecordWindow, error) {
	if v == nil || v.eventLog == nil {
		return EventRecordWindow{}, errors.New("persisted Session event log is required")
	}
	return v.eventLog.readRecentRecords(maxRecords, activeTailReverseChunkBytes)
}

func (v *PersistedSessionView) ReadNewestSegmentBackward(match func(EventRecord) bool) (EventRecordWindow, error) {
	if v == nil || v.eventLog == nil {
		return EventRecordWindow{}, errors.New("persisted Session event log is required")
	}
	return v.eventLog.readNewestSegmentBackward(activeTailReverseChunkBytes, match)
}

func (v *PersistedSessionView) ReadSegmentBackward(endOffset int64, match func(EventRecord) bool) (EventRecordWindow, error) {
	if v == nil || v.eventLog == nil {
		return EventRecordWindow{}, errors.New("persisted Session event log is required")
	}
	if endOffset <= 0 && v.eventLog.frozenEndOffset != nil {
		endOffset = *v.eventLog.frozenEndOffset
	}
	return v.eventLog.readSegmentBackward(endOffset, activeTailReverseChunkBytes, match)
}

func (v *PersistedSessionView) ReadSegmentForward(startOffset int64, match func(EventRecord) bool) (EventRecordWindow, error) {
	if v == nil || v.eventLog == nil {
		return EventRecordWindow{}, errors.New("persisted Session event log is required")
	}
	return v.eventLog.readSegmentForward(startOffset, activeTailReverseChunkBytes, match)
}
