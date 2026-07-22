package session

import (
	"errors"
	"fmt"

	"core/shared/invariant"
)

// currentEventLogReconciliationObservation is the current-format observation
// seam. Slice 12 supplies transformed-log facts through the same value before
// it installs a migrated file; this slice constructs it from an already
// current log.
type currentEventLogReconciliationObservation struct {
	lastSequence             int64
	conversationEstablished  bool
	latestCompactionSequence *int64
}

func (s *Store) reconcileCurrentEventLog() error {
	if s == nil {
		return errors.New("session store is required")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.reconcileCurrentEventLogWithMutationHeld()
}

func (s *Store) reconcileCurrentEventLogWithMutationHeld() (resultErr error) {
	s.mu.Lock()
	if !s.persisted {
		s.mu.Unlock()
		return wrapEventLogMaterializationError(
			EventLogMaterializationStageReconciliation,
			false,
			false,
			errors.New("event-log reconciliation requires durable session metadata"),
		)
	}
	if s.eventLogMaterialization == nil ||
		s.eventLogMaterialization.state != eventLogCurrentReconciliationPending {
		s.mu.Unlock()
		return errors.New("current event-log reconciliation requires pending materialization")
	}
	sessionDir := s.sessionDir
	s.mu.Unlock()

	lock, lockPath, err := acquireEventLogMigrationLock(sessionDir)
	if err != nil {
		return s.wrapCurrentEventLogReconciliationError(err)
	}
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			resultErr = s.wrapCurrentEventLogReconciliationError(errors.Join(
				resultErr,
				fmt.Errorf("release event-log migration lock %s: %w", lockPath, closeErr),
			))
		}
	}()

	return s.reconcileCurrentEventLogWithStableLockHeld()
}

func (s *Store) reconcileCurrentEventLogWithStableLockHeld() error {
	current, err := s.readCurrentEventLogReconciliationObservationWithMutationHeld()
	if err != nil {
		return err
	}
	return s.reconcileCurrentEventLogObservationWithMutationHeld(current)
}

func (s *Store) readCurrentEventLogReconciliationObservationWithMutationHeld() (
	currentEventLogReconciliationObservation,
	error,
) {
	if err := s.reclassifyPendingCurrentEventLogWithMutationHeld(); err != nil {
		return currentEventLogReconciliationObservation{}, err
	}
	s.mu.Lock()
	path := s.eventsFP
	s.mu.Unlock()
	log, err := openCurrentEventLog(path, currentEventLogReadOnly)
	if err != nil {
		return currentEventLogReconciliationObservation{}, s.wrapCurrentEventLogReconciliationError(err)
	}
	current, err := s.currentEventLogReconciliationObservation(log)
	if err != nil {
		return currentEventLogReconciliationObservation{}, s.wrapCurrentEventLogReconciliationError(err)
	}
	return current, nil
}

// reclassifyPendingCurrentEventLogWithMutationHeld verifies under the stable
// event-log lock that retrying a pending repair still targets the installed
// current file. It deliberately does not clean, stage, transform, or install
// anything: retry from pending is reconciliation-only.
func (s *Store) reclassifyPendingCurrentEventLogWithMutationHeld() error {
	s.mu.Lock()
	eventsPath := s.eventsFP
	s.mu.Unlock()

	classification, err := classifyEventLogSource(eventsPath)
	if err != nil {
		return s.wrapCurrentEventLogReconciliationError(err)
	}
	if classification == nil {
		err := errors.New(
			"event-log reconciliation invariant violated: successful classification is missing",
		)
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"reclassify_pending_current_event_log",
			err,
		))
		return s.wrapCurrentEventLogReconciliationError(err)
	}
	if classification.source != eventLogSourceCurrent {
		return s.wrapCurrentEventLogReconciliationError(fmt.Errorf(
			"pending current event-log reconciliation found source classification %d",
			classification.source,
		))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.eventLogMaterialization == nil ||
		s.eventLogMaterialization.state != eventLogCurrentReconciliationPending {
		return errors.New("current event-log reconciliation requires pending materialization")
	}
	s.eventLogMaterialization.source = classification.source
	s.eventLogMaterialization.foundVersion = cloneEventLogSourceVersion(classification.foundVersion)
	return nil
}

func (s *Store) currentEventLogReconciliationObservation(
	log *currentEventLog,
) (currentEventLogReconciliationObservation, error) {
	if log == nil {
		err := errors.New("event-log reconciliation invariant violated: current log is missing")
		invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
			invariant.ScopeSessionPersistence,
			"observe_current_event_log",
			err,
		))
		return currentEventLogReconciliationObservation{}, err
	}
	var matchErr error
	window, err := log.readNewestSegmentBackward(
		activeTailReverseChunkBytes,
		func(record EventRecord) bool {
			kind, err := record.Kind()
			if err != nil {
				matchErr = err
				return true
			}
			return kind == EventKindHistoryReplace
		},
	)
	if err != nil {
		return currentEventLogReconciliationObservation{}, err
	}
	if matchErr != nil {
		return currentEventLogReconciliationObservation{}, matchErr
	}
	established := false
	var latestCompactionSequence *int64
	for _, record := range window.Records {
		kind, err := record.Kind()
		if err != nil {
			return currentEventLogReconciliationObservation{}, err
		}
		if kind == EventKindHistoryReplace {
			sequence := record.Seq()
			latestCompactionSequence = &sequence
			// The newest replacement is the bounded durable witness that the
			// conversation was already established even when repeated
			// compaction has removed the original visible user record from the
			// active segment.
			established = true
		}
		visible, err := hasVisibleUserMessageRecord(record)
		if err != nil {
			return currentEventLogReconciliationObservation{}, err
		}
		if visible {
			established = true
		}
	}
	return currentEventLogReconciliationObservation{
		lastSequence:             log.lastSequence,
		conversationEstablished:  established,
		latestCompactionSequence: latestCompactionSequence,
	}, nil
}

func (s *Store) reconcileCurrentEventLogObservationWithMutationHeld(
	current currentEventLogReconciliationObservation,
) error {
	for attempt := 0; attempt < 2; attempt++ {
		s.mu.Lock()
		if s.eventLogMaterialization == nil ||
			s.eventLogMaterialization.state != eventLogCurrentReconciliationPending {
			s.mu.Unlock()
			return errors.New("current event-log reconciliation requires pending materialization")
		}
		observed := s.meta.LastSequence
		usageState := UsageStateReconciliationPreserve
		if current.lastSequence > observed &&
			s.meta.UsageState != nil &&
			current.latestCompactionSequence != nil &&
			*current.latestCompactionSequence > observed {
			usageState = UsageStateReconciliationInvalidate
		}
		reconciliation := PersistedEventLogReconciliation{
			SessionID:               s.meta.SessionID,
			ObservedLastSequence:    observed,
			LastSequence:            current.lastSequence,
			ConversationEstablished: current.conversationEstablished,
			UpdatedAt:               s.options.now(),
			UsageState:              usageState,
		}
		observation := &eventLogReconciliationObservation{
			reconciliation: reconciliation,
			version:        s.metadataVersion + 1,
		}
		s.mu.Unlock()

		err := s.observeEventLogReconciliation(observation)
		if err == nil {
			s.mu.Lock()
			s.meta.LastSequence = current.lastSequence
			s.meta.ConversationEstablished = current.conversationEstablished
			s.meta.UpdatedAt = reconciliation.UpdatedAt
			if usageState == UsageStateReconciliationInvalidate {
				s.meta.UsageState = nil
			}
			if current.conversationEstablished {
				s.conversationFreshness = ConversationFreshnessEstablished
			} else {
				s.conversationFreshness = ConversationFreshnessFresh
			}
			s.metadataVersion = observation.version
			s.persistedMetaVersion = observation.version
			s.eventLogMaterialization.state = eventLogCurrent
			s.mu.Unlock()
			return nil
		}
		if !errors.Is(err, ErrEventLogReconciliationConflict) {
			return s.wrapCurrentEventLogReconciliationError(err)
		}
		if refreshErr := s.refreshMetadataAfterEventLogReconciliationConflict(); refreshErr != nil {
			return s.wrapCurrentEventLogReconciliationError(errors.Join(
				err,
				fmt.Errorf(
					"refresh session metadata after event-log reconciliation conflict: %w",
					refreshErr,
				),
			))
		}
		if attempt == 1 {
			return s.wrapCurrentEventLogReconciliationError(err)
		}
		refreshedCurrent, refreshErr := s.readCurrentEventLogReconciliationObservationWithMutationHeld()
		if refreshErr != nil {
			return refreshErr
		}
		current = refreshedCurrent
	}
	err := errors.New(
		"event-log reconciliation invariant violated: bounded retry exited unexpectedly",
	)
	invariant.NewPolicy().Check(false, invariant.FailureDiagnostic(
		invariant.ScopeSessionPersistence,
		"reconcile_current_event_log_observation",
		err,
	))
	return s.wrapCurrentEventLogReconciliationError(err)
}

func (s *Store) wrapCurrentEventLogReconciliationError(err error) error {
	return wrapEventLogMaterializationError(
		EventLogMaterializationStageReconciliation,
		true,
		true,
		err,
	)
}

func (s *Store) refreshMetadataAfterEventLogReconciliationConflict() error {
	refreshed, err := resolvePersistedSessionMetaForDir(s.sessionDir, s.options)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.meta = cloneMeta(*refreshed)
	s.metadataVersion++
	s.persistedMetaVersion = s.metadataVersion
	if s.meta.ConversationEstablished {
		s.conversationFreshness = ConversationFreshnessEstablished
	} else {
		s.conversationFreshness = ConversationFreshnessFresh
	}
	return nil
}
