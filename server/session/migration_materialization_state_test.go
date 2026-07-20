package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestEventLogPreparationWaitsOnStableSiblingLockBeforePathnameClassification(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	writeEventLogPreparationSource(t, eventsPath, []byte("{not-json}\n"))

	lock := flock.New(filepath.Join(sessionDir, eventLogMigrationLockFile))
	if err := lock.Lock(); err != nil {
		t.Fatalf("acquire stable sibling lock: %v", err)
	}
	defer lock.Close()

	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	resultCh := make(chan eventLogPreparationResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := store.prepareEventLogMaterialization()
		resultCh <- result
		errCh <- err
	}()
	assertEventLogPreparationBlocked(t, resultCh, errCh)

	if err := os.Remove(eventsPath); err != nil {
		t.Fatalf("remove legacy source while lock is held: %v", err)
	}
	createCurrentEventLogPreparationSource(t, eventsPath)
	if err := lock.Unlock(); err != nil {
		t.Fatalf("release stable sibling lock: %v", err)
	}

	result := awaitEventLogPreparation(t, resultCh, errCh)
	assertEventLogPreparationResult(
		t,
		result,
		eventLogCurrentReconciliationPending,
		eventLogSourceCurrent,
	)
}

func TestEventLogPreparationThreeIndependentStoresSerializeOnStableLock(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	lock := flock.New(filepath.Join(sessionDir, eventLogMigrationLockFile))
	if err := lock.Lock(); err != nil {
		t.Fatalf("acquire stable sibling lock: %v", err)
	}
	defer lock.Close()

	stores := []*Store{
		newEventLogPreparationStore(t, sessionDir, Meta{}),
		newEventLogPreparationStore(t, sessionDir, Meta{}),
		newEventLogPreparationStore(t, sessionDir, Meta{}),
	}
	type outcome struct {
		result eventLogPreparationResult
		err    error
	}
	outcomes := make(chan outcome, len(stores))
	var waiters sync.WaitGroup
	for _, store := range stores {
		waiters.Add(1)
		go func(store *Store) {
			defer waiters.Done()
			result, err := store.prepareEventLogMaterialization()
			outcomes <- outcome{result: result, err: err}
		}(store)
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case outcome := <-outcomes:
		t.Fatalf("lock waiter completed before release: %+v", outcome)
	default:
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("release stable sibling lock: %v", err)
	}
	waiters.Wait()
	close(outcomes)
	missing := 0
	current := 0
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("independent store preparation: %v", outcome.err)
		}
		if outcome.result.State != eventLogCurrentReconciliationPending {
			t.Fatalf("independent store result = %+v", outcome.result)
		}
		switch outcome.result.Source {
		case eventLogSourceMissing:
			missing++
		case eventLogSourceCurrent:
			current++
		default:
			t.Fatalf("independent store source = %+v", outcome.result)
		}
	}
	if missing != 1 || current != 2 {
		t.Fatalf("independent store classifications: missing=%d current=%d", missing, current)
	}
}

func TestEventLogPreparationPreservesStableLockInodeAcrossHeaderInstallRename(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	lockPath := filepath.Join(sessionDir, eventLogMigrationLockFile)
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		t.Fatalf("create stable lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close stable lock: %v", err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock before source rename: %v", err)
	}

	result, err := store.prepareEventLogMaterialization()
	if err != nil {
		t.Fatalf("prepare missing source: %v", err)
	}
	assertEventLogPreparationResult(
		t,
		result,
		eventLogCurrentReconciliationPending,
		eventLogSourceMissing,
	)
	if result.WorkspacePath != filepath.Join(sessionDir, eventLogMigrationWorkspaceDir) ||
		result.StagedLogPath != filepath.Join(
			sessionDir,
			eventLogMigrationWorkspaceDir,
			eventLogMigrationStagedLogFile,
		) {
		t.Fatalf("preparation workspace paths = %+v", result)
	}
	if _, err := os.Stat(result.StagedLogPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("renamed staged log remains at its workspace path: %v", err)
	}
	if _, err := os.Stat(result.WorkspacePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned workspace remains after installation: %v", err)
	}
	after, err := os.Stat(lockPath)
	if err != nil {
		t.Fatalf("stat lock after source rename: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("event-log source installation replaced the stable lock inode")
	}
}

func TestEventLogPreparationCleansOnlyRecognizedOwnedWorkspaceArtifacts(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	workspace := eventLogMigrationWorkspacePath(sessionDir)
	if err := ensureOwnedEventLogMigrationWorkspace(workspace); err != nil {
		t.Fatalf("create owned workspace: %v", err)
	}
	writeEventLogPreparationSource(
		t,
		filepath.Join(workspace, eventLogMigrationStagedLogFile),
		[]byte("stale"),
	)
	spoolPath := filepath.Join(workspace, eventLogMigrationSpoolDir)
	if err := os.Mkdir(spoolPath, 0o700); err != nil {
		t.Fatalf("create recognized spool directory: %v", err)
	}
	writeEventLogPreparationSource(t, filepath.Join(spoolPath, "future-spool"), []byte("stale"))
	eventsPath := filepath.Join(sessionDir, eventsFile)
	source := []byte("{not-json}\n")
	writeEventLogPreparationSource(t, eventsPath, source)

	result, err := newEventLogPreparationStore(t, sessionDir, Meta{}).prepareEventLogMaterialization()
	if err != nil {
		t.Fatalf("prepare legacy source: %v", err)
	}
	assertEventLogPreparationResult(t, result, eventLogMigrationStaged, eventLogSourceLegacy)
	if _, err := os.Stat(filepath.Join(workspace, eventLogMigrationStagedLogFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged remnant survived cleanup: %v", err)
	}
	if _, err := os.Stat(spoolPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("spool remnant survived cleanup: %v", err)
	}
	if err := validateEventLogMigrationWorkspaceMarker(workspace); err != nil {
		t.Fatalf("owned workspace marker was not preserved: %v", err)
	}
	if got, err := os.ReadFile(eventsPath); err != nil || string(got) != string(source) {
		t.Fatalf("legacy source changed: bytes=%q err=%v", got, err)
	}
}

func TestEventLogPreparationPreservesUnknownWorkspaceContentAndSurfacesError(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	workspace := eventLogMigrationWorkspacePath(sessionDir)
	if err := ensureOwnedEventLogMigrationWorkspace(workspace); err != nil {
		t.Fatalf("create owned workspace: %v", err)
	}
	unknownPath := filepath.Join(workspace, "operator-note")
	writeEventLogPreparationSource(t, unknownPath, []byte("do not delete"))
	eventsPath := filepath.Join(sessionDir, eventsFile)
	source := []byte("{not-json}\n")
	writeEventLogPreparationSource(t, eventsPath, source)

	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	_, err := store.prepareEventLogMaterialization()
	var unknown unknownEventLogMigrationWorkspaceContentError
	if !errors.As(err, &unknown) || unknown.Name != "operator-note" {
		t.Fatalf("prepare unknown workspace content error = %v", err)
	}
	if got, readErr := os.ReadFile(unknownPath); readErr != nil || string(got) != "do not delete" {
		t.Fatalf("unknown workspace content changed: bytes=%q err=%v", got, readErr)
	}
	if got, readErr := os.ReadFile(eventsPath); readErr != nil || string(got) != string(source) {
		t.Fatalf("unknown workspace cleanup changed source: bytes=%q err=%v", got, readErr)
	}
	store.mu.Lock()
	snapshot := store.eventLogMaterialization
	store.mu.Unlock()
	if snapshot != nil {
		t.Fatalf("unknown workspace produced materialization snapshot %+v", snapshot)
	}
}

func TestEventLogPreparationOperationalClassificationFailureLeavesNoSnapshot(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatalf("create non-regular event-log source: %v", err)
	}
	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	if _, err := store.prepareEventLogMaterialization(); err == nil {
		t.Fatal("expected non-regular event-log source classification failure")
	}
	store.mu.Lock()
	snapshot := store.eventLogMaterialization
	store.mu.Unlock()
	if snapshot != nil {
		t.Fatalf("operational classification failure produced snapshot %+v", snapshot)
	}
}

func TestEventLogPreparationSurfacesOwnedWorkspaceCleanupFailureBeforeClassification(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	workspace := eventLogMigrationWorkspacePath(sessionDir)
	if err := ensureOwnedEventLogMigrationWorkspace(workspace); err != nil {
		t.Fatalf("create owned workspace: %v", err)
	}
	writeEventLogPreparationSource(
		t,
		filepath.Join(workspace, eventLogMigrationStagedLogFile),
		[]byte("stale"),
	)
	release := armEventLogWorkspaceCleanupFailure(t, workspace)
	defer release()
	eventsPath := filepath.Join(sessionDir, eventsFile)
	source := []byte("{not-json}\n")
	writeEventLogPreparationSource(t, eventsPath, source)

	_, err := newEventLogPreparationStore(t, sessionDir, Meta{}).prepareEventLogMaterialization()
	if err == nil {
		t.Fatal("expected owned workspace cleanup failure")
	}
	if got, readErr := os.ReadFile(eventsPath); readErr != nil || string(got) != string(source) {
		t.Fatalf("cleanup failure changed source: bytes=%q err=%v", got, readErr)
	}
}

func TestEventLogPreparationClassifiesSourceAndTransitionsDeterministically(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name       string
		source     []byte
		create     bool
		meta       Meta
		wantState  eventLogMaterializationState
		wantSource eventLogSourceClassification
		wantHeader bool
	}{
		{
			name:       "missing fresh remains pending until reconciliation",
			meta:       Meta{},
			wantState:  eventLogCurrentReconciliationPending,
			wantSource: eventLogSourceMissing,
			wantHeader: true,
		},
		{
			name:       "missing metadata claiming events remains pending",
			meta:       Meta{LastSequence: 1},
			wantState:  eventLogCurrentReconciliationPending,
			wantSource: eventLogSourceMissing,
			wantHeader: true,
		},
		{
			name:       "zero byte fresh remains pending until reconciliation",
			create:     true,
			meta:       Meta{},
			wantState:  eventLogCurrentReconciliationPending,
			wantSource: eventLogSourceEmpty,
			wantHeader: true,
		},
		{
			name:       "zero byte conversation established remains pending",
			create:     true,
			meta:       Meta{ConversationEstablished: true},
			wantState:  eventLogCurrentReconciliationPending,
			wantSource: eventLogSourceEmpty,
			wantHeader: true,
		},
		{
			name:       "empty legacy remains pending until reconciliation",
			create:     true,
			source:     []byte("\n"),
			meta:       Meta{},
			wantState:  eventLogCurrentReconciliationPending,
			wantSource: eventLogSourceEmpty,
			wantHeader: true,
		},
		{
			name:       "whitespace only legacy without newline remains pending",
			create:     true,
			source:     []byte(" \t"),
			meta:       Meta{},
			wantState:  eventLogCurrentReconciliationPending,
			wantSource: eventLogSourceEmpty,
			wantHeader: true,
		},
		{
			name:       "nonempty legacy is staged unchanged",
			create:     true,
			source:     []byte(`{"seq":1,"kind":"message","payload":{}}` + "\n"),
			meta:       Meta{},
			wantState:  eventLogMigrationStaged,
			wantSource: eventLogSourceLegacy,
		},
		{
			name:       "current waits for reconciliation",
			create:     true,
			source:     currentEventLogHeaderBytes(t),
			meta:       Meta{},
			wantState:  eventLogCurrentReconciliationPending,
			wantSource: eventLogSourceCurrent,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionDir := newEventLogPreparationSessionDir(t)
			eventsPath := filepath.Join(sessionDir, eventsFile)
			if test.create {
				writeEventLogPreparationSource(t, eventsPath, test.source)
			}
			meta := test.meta
			meta.SessionID = "session"
			meta.CreatedAt = now
			meta.UpdatedAt = now
			store := newEventLogPreparationStore(t, sessionDir, meta)
			result, err := store.prepareEventLogMaterialization()
			if err != nil {
				t.Fatalf("prepare source: %v", err)
			}
			assertEventLogPreparationResult(t, result, test.wantState, test.wantSource)
			if test.wantHeader {
				if _, err := openCurrentEventLog(eventsPath, currentEventLogReadOnly, eventLogOptions{}); err != nil {
					t.Fatalf("installed header-only v1 source is invalid: %v", err)
				}
			} else if got, readErr := os.ReadFile(eventsPath); readErr != nil || string(got) != string(test.source) {
				t.Fatalf("legacy source changed: bytes=%q err=%v", got, readErr)
			}
			repeated, err := store.prepareEventLogMaterialization()
			if err != nil {
				t.Fatalf("repeat preparation: %v", err)
			}
			assertEventLogPreparationResult(t, repeated, test.wantState, func() eventLogSourceClassification {
				if test.wantHeader {
					return eventLogSourceCurrent
				}
				return test.wantSource
			}())
		})
	}
}

func TestEventLogPreparationReclassifiesInPlaceSourceMutationUnderLock(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	writeEventLogPreparationSource(t, eventsPath, []byte("{not-json}\n"))
	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	first, err := store.prepareEventLogMaterialization()
	if err != nil {
		t.Fatalf("stage legacy source: %v", err)
	}
	assertEventLogPreparationResult(t, first, eventLogMigrationStaged, eventLogSourceLegacy)

	writeEventLogPreparationSource(t, eventsPath, currentEventLogHeaderBytes(t))
	second, err := store.prepareEventLogMaterialization()
	if err != nil {
		t.Fatalf("reclassify in-place source mutation: %v", err)
	}
	assertEventLogPreparationResult(
		t,
		second,
		eventLogCurrentReconciliationPending,
		eventLogSourceCurrent,
	)
}

func TestEventLogPreparationLeavesNewerHeaderUnmaterialized(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	source := []byte(`{"contract":"kent.session.events","version":2}` + "\n")
	writeEventLogPreparationSource(t, eventsPath, source)
	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	_, err := store.prepareEventLogMaterialization()
	var unsupported UnsupportedEventLogVersionError
	if !errors.As(err, &unsupported) {
		t.Fatalf("prepare newer source error = %v, want typed unsupported version", err)
	}
	if unsupported.FoundVersion != 2 || unsupported.SupportedVersion != EventLogVersionV1 {
		t.Fatalf("unsupported version error = %+v", unsupported)
	}
	if snapshot := eventLogPreparationStoreSnapshot(t, store); snapshot.state != eventLogUnmaterialized ||
		snapshot.source != eventLogSourceNewer {
		t.Fatalf(
			"newer source state/classification = %d/%d, want unmaterialized/newer",
			snapshot.state,
			snapshot.source,
		)
	}
	if got, readErr := os.ReadFile(eventsPath); readErr != nil || string(got) != string(source) {
		t.Fatalf("newer source changed: bytes=%q err=%v", got, readErr)
	}
}

func TestEventLogPreparationRejectsMalformedCurrentLookingHeader(t *testing.T) {
	tests := []struct {
		name   string
		source []byte
	}{
		{
			name:   "missing version",
			source: []byte(`{"contract":"kent.session.events"}` + "\n"),
		},
		{
			name:   "wrong version type",
			source: []byte(`{"contract":"kent.session.events","version":"1"}` + "\n"),
		},
		{
			name:   "syntactically malformed after current contract",
			source: []byte(`{"contract":"kent.session.events","version":` + "\n"),
		},
		{
			name:   "syntactically malformed contract value",
			source: []byte(`{"contract":` + "\n"),
		},
		{
			name: "oversized malformed after current contract",
			source: []byte(
				`{"contract":"kent.session.events","unknown":"` +
					strings.Repeat("x", currentEventLogHeaderMaxBytes) +
					`","version":`,
			),
		},
		{
			name:   "unexpected contract",
			source: []byte(`{"contract":"other.events","version":1}` + "\n"),
		},
		{
			name:   "unterminated current header",
			source: []byte(`{"contract":"kent.session.events","version":1}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionDir := newEventLogPreparationSessionDir(t)
			eventsPath := filepath.Join(sessionDir, eventsFile)
			writeEventLogPreparationSource(t, eventsPath, test.source)
			store := newEventLogPreparationStore(t, sessionDir, Meta{})
			_, err := store.prepareEventLogMaterialization()
			var malformed MalformedEventLogHeaderError
			if !errors.As(err, &malformed) {
				t.Fatalf("prepare malformed current-looking header error = %v", err)
			}
			if snapshot := eventLogPreparationStoreSnapshot(t, store); snapshot.state != eventLogUnmaterialized ||
				snapshot.source != eventLogSourceMalformed {
				t.Fatalf(
					"malformed state/classification = %d/%d, want unmaterialized/malformed",
					snapshot.state,
					snapshot.source,
				)
			}
			if got, readErr := os.ReadFile(eventsPath); readErr != nil || string(got) != string(test.source) {
				t.Fatalf("malformed source changed: bytes=%q err=%v", got, readErr)
			}
		})
	}
}

func TestEventLogPreparationStagesLargeHeaderlessLegacyRecord(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	source := []byte(
		`{"seq":1,"kind":"message","payload":{"content":"` +
			strings.Repeat("x", currentEventLogHeaderMaxBytes+1) +
			`"}}` + "\n",
	)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	writeEventLogPreparationSource(t, eventsPath, source)
	result, err := newEventLogPreparationStore(t, sessionDir, Meta{}).prepareEventLogMaterialization()
	if err != nil {
		t.Fatalf("stage large headerless legacy source: %v", err)
	}
	assertEventLogPreparationResult(t, result, eventLogMigrationStaged, eventLogSourceLegacy)
	if got, err := os.ReadFile(eventsPath); err != nil || string(got) != string(source) {
		t.Fatalf("large legacy source changed: bytes=%q err=%v", got, err)
	}
}

func TestHeaderInstallMarksStorePendingBeforePostRenameWorkspaceCleanupFailure(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	workspace := eventLogMigrationWorkspacePath(sessionDir)
	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	var release func()
	err := installHeaderOnlyCurrentEventLog(eventsPath, workspace, func() {
		store.setEventLogMaterializationState(
			eventLogCurrentReconciliationPending,
			eventLogSourceMissing,
			nil,
		)
		release = armEventLogWorkspaceCleanupFailure(t, workspace)
	})
	defer func() {
		if release != nil {
			release()
		}
	}()
	if err == nil {
		t.Fatal("expected post-rename workspace cleanup failure")
	}
	if snapshot := eventLogPreparationStoreSnapshot(t, store); snapshot.state != eventLogCurrentReconciliationPending {
		t.Fatalf("post-rename failure state = %d, want pending", snapshot.state)
	}
	if _, err := openCurrentEventLog(eventsPath, currentEventLogReadOnly, eventLogOptions{}); err != nil {
		t.Fatalf("post-rename source is not current v1: %v", err)
	}
}

func newEventLogPreparationSessionDir(t *testing.T) string {
	t.Helper()
	sessionDir := filepath.Join(t.TempDir(), "session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	return sessionDir
}

func newEventLogPreparationStore(t *testing.T, sessionDir string, meta Meta) *Store {
	t.Helper()
	now := time.Now().UTC()
	if meta.SessionID == "" {
		meta.SessionID = "session"
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = now
	}
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = now
	}
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	})
	if err != nil {
		t.Fatalf("open metadata-bound preparation store: %v", err)
	}
	return store
}

func eventLogPreparationStoreSnapshot(
	t *testing.T,
	store *Store,
) eventLogMaterializationSnapshot {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.eventLogMaterialization == nil {
		t.Fatal("store has no event-log materialization snapshot")
	}
	snapshot := *store.eventLogMaterialization
	snapshot.foundVersion = cloneEventLogSourceVersion(snapshot.foundVersion)
	return snapshot
}

func writeEventLogPreparationSource(t *testing.T, path string, source []byte) {
	t.Helper()
	if err := os.WriteFile(path, source, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func createCurrentEventLogPreparationSource(t *testing.T, path string) {
	t.Helper()
	if _, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	}); err != nil {
		t.Fatalf("create current source: %v", err)
	}
}

func currentEventLogHeaderBytes(t *testing.T) []byte {
	t.Helper()
	header, err := encodeEventLogHeaderV1()
	if err != nil {
		t.Fatalf("encode current event-log header: %v", err)
	}
	return append(header, '\n')
}

func assertEventLogPreparationBlocked(
	t *testing.T,
	resultCh <-chan eventLogPreparationResult,
	errCh <-chan error,
) {
	t.Helper()
	select {
	case result := <-resultCh:
		t.Fatalf("preparation bypassed held lock with result %+v", result)
	case err := <-errCh:
		t.Fatalf("preparation bypassed held lock with error %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func awaitEventLogPreparation(
	t *testing.T,
	resultCh <-chan eventLogPreparationResult,
	errCh <-chan error,
) eventLogPreparationResult {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("prepare event log: %v", err)
		}
		return <-resultCh
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not proceed after stable sibling lock release")
		return eventLogPreparationResult{}
	}
}

func assertEventLogPreparationResult(
	t *testing.T,
	result eventLogPreparationResult,
	wantState eventLogMaterializationState,
	wantSource eventLogSourceClassification,
) {
	t.Helper()
	if result.State != wantState || result.Source != wantSource {
		t.Fatalf(
			"preparation result state/source = %d/%d, want %d/%d (%+v)",
			result.State,
			result.Source,
			wantState,
			wantSource,
			result,
		)
	}
}
