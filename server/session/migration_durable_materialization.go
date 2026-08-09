package session

import (
	"errors"
	"fmt"
)

func (s *Store) materializePreparedDurableEventLogWithMutationHeld() (
	capability MaterializedEventLog,
	resultErr error,
) {
	s.mu.Lock()
	sessionDir := s.sessionDir
	s.mu.Unlock()

	lock, lockPath, err := acquireEventLogPersistenceLock(sessionDir)
	if err != nil {
		return MaterializedEventLog{}, wrapEventLogPreparationError(false, err)
	}
	committed := false
	defer func() {
		if closeErr := releaseEventLogPersistenceLock(lock, lockPath); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				closeErr,
			)
			if resultErr != nil {
				resultErr = wrapEventLogMaterializationError(
					EventLogMaterializationStagePreparation,
					committed,
					false,
					resultErr,
				)
			}
		}
	}()

	preparation, preparationCommitted, err :=
		s.prepareEventLogMaterializationWithStableLockHeld()
	committed = preparationCommitted
	if err != nil {
		return MaterializedEventLog{}, wrapEventLogPreparationError(committed, err)
	}
	switch preparation.State {
	case eventLogCurrentReconciliationPending:
	default:
		return MaterializedEventLog{}, wrapEventLogPreparationError(
			committed,
			fmt.Errorf(
				"event-log materialization preparation reached state %d",
				preparation.State,
			),
		)
	}

	if err := s.reconcileCurrentEventLogWithStableLockHeld(); err != nil {
		return MaterializedEventLog{}, err
	}
	return s.issueCurrentEventLogCapabilityWithMutationHeld()
}

func (s *Store) issueCurrentEventLogCapabilityWithMutationHeld() (
	MaterializedEventLog,
	error,
) {
	s.mu.Lock()
	path := s.eventsFP
	expectedRevision := s.meta.LastSequence
	if s.eventLogMaterialization == nil ||
		s.eventLogMaterialization.state != eventLogCurrent {
		s.mu.Unlock()
		return MaterializedEventLog{}, errors.New(
			"current event-log capability requires reconciled materialization",
		)
	}
	s.mu.Unlock()

	log, err := openCurrentEventLog(path, currentEventLogAuthoritative)
	if err != nil {
		return MaterializedEventLog{}, wrapEventLogMaterializationError(
			EventLogMaterializationStageReconciliation,
			true,
			false,
			err,
		)
	}
	log.durabilityObserver = s.options.durabilityObserver
	if log.lastSequence != expectedRevision {
		return MaterializedEventLog{}, wrapEventLogMaterializationError(
			EventLogMaterializationStageReconciliation,
			true,
			false,
			fmt.Errorf(
				"current event log revision %d does not match metadata revision %d",
				log.lastSequence,
				expectedRevision,
			),
		)
	}
	s.mu.Lock()
	s.materializedEventLog = log
	s.mu.Unlock()
	return MaterializedEventLog{store: s, log: log}, nil
}
