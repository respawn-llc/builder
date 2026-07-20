package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"core/shared/invariant"
)

// MaterializedEventLog is a borrowed capability owned by its Store. It has no
// close operation and cannot outlive the Store that issued it.
type MaterializedEventLog struct {
	store *Store
	log   *currentEventLog
}

type EventLogArtifactLease struct {
	mu           sync.Mutex
	closed       bool
	store        *Store
	log          *currentEventLog
	artifactRoot string
	remove       func(string) error
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
	if s.options.filelessEvents {
		s.mu.Unlock()
		return MaterializedEventLog{}, errors.New(
			"fileless session requires scoped event log materialization",
		)
	}
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

func (s *Store) MaterializeFilelessEventLog(ctx context.Context) (
	MaterializedEventLog,
	*EventLogArtifactLease,
	error,
) {
	if s == nil {
		return MaterializedEventLog{}, nil, errors.New("session store is required")
	}
	if ctx == nil {
		return MaterializedEventLog{}, nil, errors.New(
			"fileless event-log materialization context is required",
		)
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return MaterializedEventLog{}, nil, err
	}
	s.mu.Lock()
	if !s.options.filelessEvents {
		s.mu.Unlock()
		return MaterializedEventLog{}, nil, errors.New(
			"scoped event log materialization requires fileless persistence",
		)
	}
	if s.materializedEventLog != nil {
		log := s.materializedEventLog
		lease := s.filelessEventLogLease
		s.mu.Unlock()
		if lease != nil {
			return MaterializedEventLog{store: s, log: log}, lease, nil
		}
		return MaterializedEventLog{store: s, log: log}, &EventLogArtifactLease{}, nil
	}
	if !s.persisted {
		s.mu.Unlock()
		return MaterializedEventLog{}, nil, errors.New(
			"fileless current event log requires persisted session metadata",
		)
	}
	path := s.eventsFP
	options := s.options.eventLog
	expectedRevision := s.meta.LastSequence
	s.mu.Unlock()

	classification, err := classifyEventLogSource(path)
	if err != nil {
		return MaterializedEventLog{}, nil, err
	}
	switch classification.source {
	case eventLogSourceMissing:
		return MaterializedEventLog{}, nil, os.ErrNotExist
	case eventLogSourceCurrent:
		log, err := openCurrentEventLog(path, currentEventLogReadOnly, options)
		if err != nil {
			return MaterializedEventLog{}, nil, err
		}
		if log.lastSequence != expectedRevision {
			return MaterializedEventLog{}, nil, fmt.Errorf(
				"current event log revision %d does not match metadata revision %d",
				log.lastSequence,
				expectedRevision,
			)
		}
		s.mu.Lock()
		s.materializedEventLog = log
		s.mu.Unlock()
		return MaterializedEventLog{store: s, log: log}, &EventLogArtifactLease{}, nil
	case eventLogSourceLegacy, eventLogSourceEmpty:
		return s.materializeLegacyFilelessEventLogWithMutationHeld(
			ctx,
			path,
			options,
			expectedRevision,
		)
	default:
		return MaterializedEventLog{}, nil, fmt.Errorf(
			"unsupported fileless event-log source classification %d",
			classification.source,
		)
	}
}

func (s *Store) materializeLegacyFilelessEventLogWithMutationHeld(
	ctx context.Context,
	sourcePath string,
	options eventLogOptions,
	_ int64,
) (
	capability MaterializedEventLog,
	lease *EventLogArtifactLease,
	resultErr error,
) {
	artifactRoot, err := os.MkdirTemp("", "kent-session-events-v1-")
	if err != nil {
		return MaterializedEventLog{}, nil, fmt.Errorf(
			"create fileless event-log artifact directory: %w",
			err,
		)
	}
	keepArtifact := false
	defer func() {
		if !keepArtifact {
			resultErr = errors.Join(
				resultErr,
				removeFilelessEventLogArtifact(artifactRoot),
			)
		}
	}()
	spoolDir := filepath.Join(artifactRoot, eventLogMigrationSpoolDir)
	if err := os.Mkdir(spoolDir, 0o700); err != nil {
		return MaterializedEventLog{}, nil, fmt.Errorf(
			"create fileless event-log spool directory: %w",
			err,
		)
	}
	artifactPath := filepath.Join(artifactRoot, eventsFile)
	if _, err := transformLegacyEventLogToCurrentFile(
		ctx,
		sourcePath,
		artifactPath,
		spoolDir,
		osMigrationSpoolStorage{},
	); err != nil {
		return MaterializedEventLog{}, nil, err
	}
	log, err := openCurrentEventLog(
		artifactPath,
		currentEventLogReadOnly,
		options,
	)
	if err != nil {
		return MaterializedEventLog{}, nil, err
	}
	lease = &EventLogArtifactLease{
		store:        s,
		log:          log,
		artifactRoot: artifactRoot,
		remove:       os.RemoveAll,
	}
	s.mu.Lock()
	s.materializedEventLog = log
	s.filelessEventLogLease = lease
	s.mu.Unlock()
	keepArtifact = true
	return MaterializedEventLog{store: s, log: log}, lease, nil
}

func (l *EventLogArtifactLease) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	if l.store != nil {
		l.store.mutationMu.Lock()
		defer l.store.mutationMu.Unlock()
	}
	if l.artifactRoot != "" {
		if l.remove == nil {
			return errors.New(
				"fileless event-log artifact lease has no cleanup operation",
			)
		}
		if err := l.remove(l.artifactRoot); err != nil {
			return fmt.Errorf("remove fileless event-log artifact: %w", err)
		}
		if l.store != nil {
			l.store.mu.Lock()
			if l.store.materializedEventLog == l.log {
				l.store.materializedEventLog = nil
			}
			if l.store.filelessEventLogLease == l {
				l.store.filelessEventLogLease = nil
			}
			l.store.mu.Unlock()
		}
	}
	l.closed = true
	return nil
}

func removeFilelessEventLogArtifact(root string) error {
	if root == "" {
		return nil
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove fileless event-log artifact: %w", err)
	}
	return nil
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
	_, err := walkCurrentEventLogComplete(
		path,
		newMigrationResourceLedger(),
		visit,
	)
	return err
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
