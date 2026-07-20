package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (s *Store) materializePreparedDurableEventLogWithMutationHeld() (
	capability MaterializedEventLog,
	resultErr error,
) {
	s.mu.Lock()
	sessionDir := s.sessionDir
	s.mu.Unlock()

	lock, lockPath, err := acquireEventLogMigrationLock(sessionDir)
	if err != nil {
		return MaterializedEventLog{}, wrapEventLogPreparationError(false, err)
	}
	committed := false
	defer func() {
		if closeErr := lock.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("release event-log migration lock %s: %w", lockPath, closeErr),
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
	case eventLogMigrationStaged:
		migrationCommitted, migrateErr := s.installLegacyEventLogWithStableLockHeld(
			context.Background(),
			preparation,
		)
		committed = committed || migrationCommitted
		if migrateErr != nil {
			return MaterializedEventLog{}, wrapEventLogPreparationError(committed, migrateErr)
		}
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

func (s *Store) installLegacyEventLogWithStableLockHeld(
	ctx context.Context,
	preparation eventLogPreparationResult,
) (
	committed bool,
	resultErr error,
) {
	if preparation.State != eventLogMigrationStaged ||
		preparation.Source != eventLogSourceLegacy {
		return false, fmt.Errorf(
			"legacy event-log installation requires staged legacy source, got state=%d source=%d",
			preparation.State,
			preparation.Source,
		)
	}
	s.mu.Lock()
	eventsPath := s.eventsFP
	s.mu.Unlock()
	workspace := preparation.WorkspacePath
	if workspace == "" || preparation.StagedLogPath == "" {
		panic("legacy event-log installation invariant violated: preparation paths are missing")
	}
	workspaceReady := false
	defer func() {
		if !committed && workspaceReady {
			resultErr = errors.Join(resultErr, removeOwnedEventLogMigrationWorkspace(workspace))
		}
		if committed {
			resultErr = wrapEventLogPreparationError(true, resultErr)
		}
	}()

	if err := ensureOwnedEventLogMigrationWorkspace(workspace); err != nil {
		return false, err
	}
	workspaceReady = true
	spoolDir := filepath.Join(workspace, eventLogMigrationSpoolDir)
	if err := os.Mkdir(spoolDir, 0o700); err != nil {
		return false, fmt.Errorf("create event-log migration spool directory: %w", err)
	}

	if _, err := transformLegacyEventLogToCurrentFile(
		ctx,
		eventsPath,
		preparation.StagedLogPath,
		spoolDir,
		osMigrationSpoolStorage{},
	); err != nil {
		return false, err
	}
	if err := atomicallyReplaceEventLog(preparation.StagedLogPath, eventsPath); err != nil {
		return false, err
	}
	s.setEventLogMaterializationState(
		eventLogCurrentReconciliationPending,
		eventLogSourceLegacy,
		nil,
	)
	committed = true
	if err := removeOwnedEventLogMigrationWorkspace(workspace); err != nil {
		return true, err
	}
	workspaceReady = false
	if err := syncEventLogDirectory(filepath.Dir(eventsPath)); err != nil {
		return true, err
	}
	return true, nil
}

func closeEventLogMigrationFile(description string, file *os.File) error {
	if file == nil {
		return nil
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", description, err)
	}
	return nil
}

func (s *Store) issueCurrentEventLogCapabilityWithMutationHeld() (
	MaterializedEventLog,
	error,
) {
	s.mu.Lock()
	path := s.eventsFP
	options := s.options.eventLog
	expectedRevision := s.meta.LastSequence
	if s.eventLogMaterialization == nil ||
		s.eventLogMaterialization.state != eventLogCurrent {
		s.mu.Unlock()
		return MaterializedEventLog{}, errors.New(
			"current event-log capability requires reconciled materialization",
		)
	}
	s.mu.Unlock()

	log, err := openCurrentEventLog(path, currentEventLogAuthoritative, options)
	if err != nil {
		return MaterializedEventLog{}, wrapEventLogMaterializationError(
			EventLogMaterializationStageReconciliation,
			true,
			false,
			err,
		)
	}
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
