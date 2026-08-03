package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DiagnosticSessionCopy owns an isolated durable copy of one Session's active
// event-log segment. Runtime inspection can mutate the copy through ordinary
// persistence paths without changing the source Session.
type DiagnosticSessionCopy struct {
	mu    sync.Mutex
	root  string
	store *Store
}

var ErrDiagnosticLegacyEventLogUnsupported = errors.New(
	"diagnostic request inspection does not support legacy session event logs",
)

func OpenDiagnosticSessionCopy(
	ctx context.Context,
	resolver PersistedSessionResolver,
	sessionID string,
) (_ *DiagnosticSessionCopy, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if resolver == nil {
		return nil, errors.New("diagnostic Session copy requires a persisted Session resolver")
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil, errors.New("diagnostic Session copy requires a Session identity")
	}
	record, err := resolver.ResolvePersistedSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := validatePersistedSessionRecord(id, record); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "kent-session-inspection-")
	if err != nil {
		return nil, fmt.Errorf("create diagnostic Session copy root: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.RemoveAll(root))
		}
	}()
	copyDir := filepath.Join(root, "session")
	if err := os.Mkdir(copyDir, 0o700); err != nil {
		return nil, fmt.Errorf("create diagnostic Session copy: %w", err)
	}
	record, err = copyDiagnosticSessionEvents(ctx, resolver, id, record, copyDir)
	if err != nil {
		return nil, err
	}
	authority := &diagnosticCopyPersistence{
		record: PersistedSessionRecord{
			SessionDir: copyDir,
			Meta:       diagnosticCopyMeta(record.Meta),
		},
	}
	store, err := OpenResolved(
		authority.record,
		WithPersistenceObserver(authority),
		WithPersistedSessionResolver(authority),
	)
	if err != nil {
		return nil, fmt.Errorf("open diagnostic Session copy: %w", err)
	}
	return &DiagnosticSessionCopy{root: root, store: store}, nil
}

func (c *DiagnosticSessionCopy) Store() *Store {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.store
}

func (c *DiagnosticSessionCopy) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	root := c.root
	c.root = ""
	c.store = nil
	c.mu.Unlock()
	if root == "" {
		return nil
	}
	return os.RemoveAll(root)
}

func copyDiagnosticSessionEvents(
	ctx context.Context,
	resolver PersistedSessionResolver,
	sessionID string,
	initial PersistedSessionRecord,
	destinationDir string,
) (_ PersistedSessionRecord, resultErr error) {
	lock, stableLockPath, err := acquireEventLogPersistenceLock(initial.SessionDir)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	defer joinEventLogPersistenceLockRelease(&resultErr, lock, stableLockPath)
	record, err := resolver.ResolvePersistedSession(ctx, sessionID)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	if err := validatePersistedSessionRecord(sessionID, record); err != nil {
		return PersistedSessionRecord{}, err
	}
	if record.SessionDir != initial.SessionDir {
		return PersistedSessionRecord{}, fmt.Errorf(
			"diagnostic Session source moved from %q to %q",
			initial.SessionDir,
			record.SessionDir,
		)
	}
	if err := copyDiagnosticEventLog(
		filepath.Join(record.SessionDir, eventsFile),
		filepath.Join(destinationDir, eventsFile),
	); err != nil {
		return PersistedSessionRecord{}, err
	}
	if err := initializeEventLogPersistenceLock(destinationDir); err != nil {
		return PersistedSessionRecord{}, err
	}
	return record, nil
}

func copyDiagnosticEventLog(sourcePath, destinationPath string) (resultErr error) {
	classification, err := classifyEventLogSource(sourcePath)
	if err != nil {
		return fmt.Errorf("classify source event log for diagnostic copy: %w", err)
	}
	switch classification.source {
	case eventLogSourceCurrent:
	case eventLogSourceEmpty:
		if _, err := createCurrentEventLog(destinationPath); err != nil {
			return fmt.Errorf("create empty diagnostic event log: %w", err)
		}
		return nil
	case eventLogSourceLegacy:
		return ErrDiagnosticLegacyEventLogUnsupported
	default:
		return fmt.Errorf(
			"diagnostic request inspection requires a current event log: source=%d",
			classification.source,
		)
	}
	log, err := openCurrentEventLog(sourcePath, currentEventLogReadOnly)
	if err != nil {
		return fmt.Errorf("open source event log for diagnostic copy: %w", err)
	}
	window, err := log.readActiveSegment()
	if err != nil {
		return fmt.Errorf("read active event-log segment for diagnostic copy: %w", err)
	}
	source, err := openSessionFileReadOnly(sourcePath)
	if err != nil {
		return fmt.Errorf("open source event log for diagnostic copy: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, source.Close())
	}()
	destination, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create diagnostic event log copy: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, destination.Close())
	}()
	if err := copyDiagnosticEventLogRange(destination, source, 0, log.firstEventOffset); err != nil {
		return err
	}
	if err := copyDiagnosticEventLogRange(
		destination,
		source,
		window.StartOffset,
		window.EndOffset-window.StartOffset,
	); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync diagnostic event log copy: %w", err)
	}
	return nil
}

func copyDiagnosticEventLogRange(destination, source *os.File, offset, length int64) error {
	if length == 0 {
		return nil
	}
	if _, err := source.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek source event log for diagnostic copy: %w", err)
	}
	written, err := io.CopyN(destination, source, length)
	if err != nil {
		return fmt.Errorf("copy diagnostic event-log range: %w", err)
	}
	if written != length {
		return fmt.Errorf(
			"copy diagnostic event-log range at byte %d wrote %d bytes, want %d",
			offset,
			written,
			length,
		)
	}
	return nil
}

type diagnosticCopyPersistence struct {
	mu     sync.Mutex
	record PersistedSessionRecord
}

func (p *diagnosticCopyPersistence) ResolvePersistedSession(
	_ context.Context,
	sessionID string,
) (PersistedSessionRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.record.Meta == nil || p.record.Meta.SessionID != sessionID {
		return PersistedSessionRecord{}, ErrSessionNotFound
	}
	return PersistedSessionRecord{
		SessionDir: p.record.SessionDir,
		Meta:       diagnosticCopyMeta(p.record.Meta),
	}, nil
}

func (p *diagnosticCopyPersistence) ObservePersistedStore(
	_ context.Context,
	snapshot PersistedStoreSnapshot,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record = PersistedSessionRecord{
		SessionDir: snapshot.SessionDir,
		Meta:       diagnosticCopyMeta(&snapshot.Meta),
	}
	return nil
}

func (p *diagnosticCopyPersistence) ObserveEventLogReconciliation(
	_ context.Context,
	reconciliation PersistedEventLogReconciliation,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.record.Meta == nil || p.record.Meta.SessionID != reconciliation.SessionID {
		return ErrSessionNotFound
	}
	if p.record.Meta.LastSequence != reconciliation.ObservedLastSequence {
		return EventLogReconciliationConflictError{
			SessionID:            reconciliation.SessionID,
			ObservedLastSequence: reconciliation.ObservedLastSequence,
			CurrentLastSequence:  p.record.Meta.LastSequence,
		}
	}
	invalidateUsage, err := reconciliation.UsageState.InvalidatesUsageState()
	if err != nil {
		return err
	}
	meta := cloneMeta(*p.record.Meta)
	meta.LastSequence = reconciliation.LastSequence
	meta.ConversationEstablished = reconciliation.ConversationEstablished
	meta.UpdatedAt = reconciliation.UpdatedAt
	if invalidateUsage {
		meta.UsageState = nil
	}
	p.record.Meta = &meta
	return nil
}

func diagnosticCopyMeta(meta *Meta) *Meta {
	if meta == nil {
		return nil
	}
	copy := cloneMeta(*meta)
	return &copy
}
