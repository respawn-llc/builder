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

// DiagnosticSessionCopy owns an isolated durable copy of one Session. Runtime
// inspection can migrate and mutate the copy through ordinary persistence
// paths without changing the source Session.
type DiagnosticSessionCopy struct {
	mu    sync.Mutex
	root  string
	store *Store
}

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
	copyWithStableSource := func() (PersistedSessionRecord, error) {
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
		return record, nil
	}

	lockPath := filepath.Join(initial.SessionDir, eventLogPersistenceLockFile)
	if _, err := os.Lstat(lockPath); errors.Is(err, os.ErrNotExist) {
		record, copyErr := copyWithStableSource()
		if copyErr != nil {
			return PersistedSessionRecord{}, copyErr
		}
		if err := initializeEventLogPersistenceLock(destinationDir); err != nil {
			return PersistedSessionRecord{}, err
		}
		return record, nil
	} else if err != nil {
		return PersistedSessionRecord{}, fmt.Errorf("inspect source event-log lock: %w", err)
	}

	lock, stableLockPath, err := acquireEventLogPersistenceLock(initial.SessionDir)
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	defer joinEventLogPersistenceLockRelease(&resultErr, lock, stableLockPath)
	record, err := copyWithStableSource()
	if err != nil {
		return PersistedSessionRecord{}, err
	}
	if err := initializeEventLogPersistenceLock(destinationDir); err != nil {
		return PersistedSessionRecord{}, err
	}
	return record, nil
}

func copyDiagnosticEventLog(sourcePath, destinationPath string) (resultErr error) {
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
	if _, err := io.Copy(destination, source); err != nil {
		return fmt.Errorf("copy diagnostic event log: %w", err)
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("sync diagnostic event log copy: %w", err)
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
