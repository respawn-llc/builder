package session

import (
	"errors"
	"strings"

	"core/shared/invariant"
)

// MaterializedEventLog is a borrowed capability owned by its Store. It has no
// close operation and cannot outlive the Store that issued it.
type MaterializedEventLog struct {
	store *Store
	log   *currentEventLog
}

func (c MaterializedEventLog) ValidateOwner(store *Store) error {
	if store == nil {
		return errors.New("materialized event log owner is required")
	}
	if c.store == nil {
		return errors.New("materialized event log is required")
	}
	if c.store != store {
		return errors.New("materialized event log belongs to a different Store")
	}
	store.mutationMu.Lock()
	defer store.mutationMu.Unlock()
	store.mu.Lock()
	defer store.mu.Unlock()
	if c.log == nil || store.materializedEventLog != c.log {
		return errors.New("Store no longer owns a materialized event log")
	}
	return nil
}

func (s *Store) MaterializeEventLog() (MaterializedEventLog, error) {
	if s == nil {
		return MaterializedEventLog{}, errors.New("session store is required")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.materializeDurableEventLogWithMutationHeld()
}

func (s *Store) materializeDurableEventLogWithMutationHeld() (MaterializedEventLog, error) {
	s.mu.Lock()
	if s.materializedEventLog != nil {
		log := s.materializedEventLog
		s.mu.Unlock()
		return MaterializedEventLog{store: s, log: log}, nil
	}
	if !s.persisted {
		s.mu.Unlock()
		return MaterializedEventLog{}, errors.New("current event log requires durable session metadata")
	}
	s.mu.Unlock()

	return s.materializePreparedDurableEventLogWithMutationHeld()
}

func (c MaterializedEventLog) Revision() (int64, error) {
	log, release, err := c.currentLogForQuery()
	if err != nil {
		return 0, err
	}
	defer release()
	return log.lastSequence, nil
}

func (c MaterializedEventLog) ConversationFreshness() (ConversationFreshness, error) {
	_, release, err := c.currentLogForQuery()
	if err != nil {
		return ConversationFreshnessFresh, err
	}
	defer release()
	return c.store.conversationFreshness, nil
}

func (c MaterializedEventLog) ReadRecentRecords(maxRecords int) (EventRecordWindow, error) {
	log, release, err := c.currentLogForUse()
	if err != nil {
		return EventRecordWindow{}, err
	}
	defer release()
	return log.readRecentRecords(maxRecords, activeTailReverseChunkBytes)
}

func (c MaterializedEventLog) ReadNewestSegmentBackward(
	match func(EventRecord) bool,
) (EventRecordWindow, error) {
	log, release, err := c.currentLogForUse()
	if err != nil {
		return EventRecordWindow{}, err
	}
	defer release()
	return log.readNewestSegmentBackward(activeTailReverseChunkBytes, match)
}

func (c MaterializedEventLog) PendingRecoveryStepHasTerminalAssistant(
	stepID string,
) (bool, error) {
	stepID = strings.TrimSpace(stepID)
	if stepID == "" {
		return false, errors.New("pending recovery step identity is required")
	}
	var matchErr error
	window, err := c.ReadNewestSegmentBackward(func(record EventRecord) bool {
		payload, err := record.Payload()
		if err != nil {
			matchErr = err
			return true
		}
		_, boundary := payload.(HistoryReplacementRecord)
		return boundary
	})
	if err != nil {
		return false, err
	}
	if matchErr != nil {
		return false, matchErr
	}
	for _, record := range window.Records {
		recordStepID := record.StepID()
		if recordStepID == nil || strings.TrimSpace(*recordStepID) != stepID {
			continue
		}
		payload, err := record.Payload()
		if err != nil {
			return false, err
		}
		message, ok := payload.(MessageRecord)
		if !ok {
			continue
		}
		if message.Role == MessageRoleAssistant &&
			message.Phase != nil &&
			*message.Phase == MessagePhaseFinal &&
			len(message.ToolCalls) == 0 {
			return true, nil
		}
	}
	return false, nil
}

func (c MaterializedEventLog) ReadSegmentBackward(
	endOffset int64,
	match func(EventRecord) bool,
) (EventRecordWindow, error) {
	log, release, err := c.currentLogForUse()
	if err != nil {
		return EventRecordWindow{}, err
	}
	defer release()
	return log.readSegmentBackward(endOffset, activeTailReverseChunkBytes, match)
}

func (c MaterializedEventLog) ReadSegmentForward(
	startOffset int64,
	match func(EventRecord) bool,
) (EventRecordWindow, error) {
	log, release, err := c.currentLogForUse()
	if err != nil {
		return EventRecordWindow{}, err
	}
	defer release()
	return log.readSegmentForward(startOffset, activeTailReverseChunkBytes, match)
}

// WalkRecords is reserved for explicit whole-history materialization such as
// fork and clone replay. Transcript and working-set reads must use bounded
// segment methods.
func (c MaterializedEventLog) WalkRecords(visit func(EventRecord) error) error {
	if visit == nil {
		return errors.New("event record visitor is required")
	}
	if c.store == nil {
		return errors.New("materialized event log owning Store is required")
	}
	s := c.store
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	s.mu.Lock()
	if c.log == nil || s.materializedEventLog != c.log {
		s.mu.Unlock()
		return errors.New("event walk requires materialized event-log capability")
	}
	path := s.materializedEventLog.path
	s.mu.Unlock()
	log, err := openCurrentEventLog(path, currentEventLogReadOnly)
	if err != nil {
		return err
	}
	offset := log.firstEventOffset
	for {
		window, err := log.readSegmentForward(offset, activeTailReverseChunkBytes, nil)
		if err != nil {
			return err
		}
		for _, record := range window.Records {
			if err := visit(record); err != nil {
				return err
			}
		}
		if window.ReachedEnd {
			return nil
		}
		if window.EndOffset <= offset {
			return errors.New("event walk did not advance")
		}
		offset = window.EndOffset
	}
}

func (c MaterializedEventLog) currentLogForUse() (*currentEventLog, func(), error) {
	if c.store == nil {
		err := errors.New("materialized event log invariant violated: owning Store is missing")
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"use_materialized_event_log",
			err,
		))
		return nil, nil, err
	}
	c.store.mutationMu.Lock()
	c.store.mu.Lock()
	if c.log == nil || c.store.materializedEventLog != c.log {
		c.store.mu.Unlock()
		c.store.mutationMu.Unlock()
		err := errors.New(
			"materialized event log invariant violated: Store capability is missing",
		)
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"use_materialized_event_log",
			err,
		))
		return nil, nil, err
	}
	log := c.log
	c.store.mu.Unlock()
	return log, c.store.mutationMu.Unlock, nil
}

// currentLogForQuery snapshots event-derived scalar state while holding only
// Store.mu. Persistence observers run while mutationMu remains held so later
// mutations cannot overtake their committed snapshot; scalar queries must
// remain reentrant from those observers.
func (c MaterializedEventLog) currentLogForQuery() (*currentEventLog, func(), error) {
	if c.store == nil {
		err := errors.New("materialized event log invariant violated: owning Store is missing")
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"use_materialized_event_log",
			err,
		))
		return nil, nil, err
	}
	c.store.mu.Lock()
	if c.log == nil || c.store.materializedEventLog != c.log {
		c.store.mu.Unlock()
		err := errors.New(
			"materialized event log invariant violated: Store capability is missing",
		)
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"use_materialized_event_log",
			err,
		))
		return nil, nil, err
	}
	return c.log, c.store.mu.Unlock, nil
}
