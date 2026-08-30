package sessionservice

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/runtimeids"
)

type sessionRemovalMetadataDelegate struct {
	*metadata.Store
	delete func(context.Context, string) error
}

func (m sessionRemovalMetadataDelegate) DeleteSession(ctx context.Context, sessionID string) error {
	if m.delete != nil {
		return m.delete(ctx, sessionID)
	}
	return m.Store.DeleteSession(ctx, sessionID)
}

type sessionRemovalServiceFixture struct {
	service   *SessionLifecycleService
	authority *sessionruntime.Authority
	metadata  *metadata.Store
	session   *session.Store
}

func newSessionRemovalServiceFixture(t *testing.T) sessionRemovalServiceFixture {
	t.Helper()
	cfg, metadataStore, _, persisted := createAuthoritativeSessionLifecycleSession(t, t.TempDir())
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close Session Runtime authority: %v", err)
		}
	})
	service := NewGlobalSessionLifecycleService(cfg.PersistenceRoot, authority, nil).
		WithPersistedSessionResolver(metadataStore).
		WithSessionRemovalMetadata(metadataStore)
	return sessionRemovalServiceFixture{
		service:   service,
		authority: authority,
		metadata:  metadataStore,
		session:   persisted,
	}
}

func TestSessionLifecycleRemovalReportsDurableOutcomes(t *testing.T) {
	t.Run("successful delete", func(t *testing.T) {
		fixture := newSessionRemovalServiceFixture(t)
		unknownPath := filepath.Join(fixture.session.Dir(), "personal-notes.txt")
		if err := os.WriteFile(unknownPath, []byte("keep"), 0o644); err != nil {
			t.Fatalf("write unknown content: %v", err)
		}

		if err := fixture.service.Delete(
			context.Background(),
			fixture.session.Meta().SessionID,
		); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := fixture.metadata.ResolvePersistedSession(t.Context(), fixture.session.Meta().SessionID); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("resolve deleted Session = %v, want Session not found", err)
		}
		if body, err := os.ReadFile(unknownPath); err != nil || string(body) != "keep" {
			t.Fatalf("unknown content after delete = %q, %v", body, err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.session.Dir(), "events.jsonl")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned event log remains after delete: %v", err)
		}
	})

	t.Run("archive metadata not removed", func(t *testing.T) {
		fixture := newSessionRemovalServiceFixture(t)
		fixture.service.WithSessionRemovalMetadata(sessionRemovalMetadataDelegate{
			Store: fixture.metadata,
			delete: func(_ context.Context, sessionID string) error {
				return &metadata.SessionInUseError{SessionID: sessionID}
			},
		})
		outputPath := filepath.Join(t.TempDir(), "session.tar.zst")

		err := fixture.service.Archive(
			context.Background(),
			fixture.session.Meta().SessionID,
			outputPath,
		)
		var removalErr *SessionRemovalFailureError
		if !errors.As(err, &removalErr) ||
			removalErr.State != SessionRemovalMetadataNotRemoved ||
			removalErr.RemainingPath != "" {
			t.Fatalf("Archive error = %v, want metadata-not-removed state", err)
		}
		if _, err := os.Stat(outputPath); err != nil {
			t.Fatalf("published archive missing: %v", err)
		}
		if _, err := fixture.metadata.ResolvePersistedSession(t.Context(), fixture.session.Meta().SessionID); err != nil {
			t.Fatalf("Session was removed after metadata blocker: %v", err)
		}
	})

	t.Run("archive metadata removed cleanup failed", func(t *testing.T) {
		fixture := newSessionRemovalServiceFixture(t)
		eventsPath := filepath.Join(fixture.session.Dir(), "events.jsonl")
		fixture.service.WithSessionRemovalMetadata(sessionRemovalMetadataDelegate{
			Store: fixture.metadata,
			delete: func(ctx context.Context, sessionID string) error {
				if err := fixture.metadata.DeleteSession(ctx, sessionID); err != nil {
					return err
				}
				return os.Chmod(fixture.session.Dir(), 0o500)
			},
		})
		t.Cleanup(func() { _ = os.Chmod(fixture.session.Dir(), 0o700) })
		outputPath := filepath.Join(t.TempDir(), "session.tar.zst")

		err := fixture.service.Archive(
			context.Background(),
			fixture.session.Meta().SessionID,
			outputPath,
		)
		var removalErr *SessionRemovalFailureError
		if !errors.As(err, &removalErr) ||
			removalErr.State != SessionRemovalMetadataRemovedCleanupFailed ||
			removalErr.RemainingPath != eventsPath {
			t.Fatalf("Archive error = %v, want cleanup failure for %q", err, eventsPath)
		}
		if _, err := os.Stat(outputPath); err != nil {
			t.Fatalf("published archive missing: %v", err)
		}
		if _, err := fixture.metadata.ResolvePersistedSession(t.Context(), fixture.session.Meta().SessionID); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("resolve Session after metadata commit = %v, want Session not found", err)
		}
	})
}

type manualArchiveDetachTimer struct {
	done chan time.Time
	once sync.Once
}

func (t *manualArchiveDetachTimer) Done() <-chan time.Time {
	return t.done
}

func (*manualArchiveDetachTimer) Stop() bool {
	return true
}

func (t *manualArchiveDetachTimer) Fire() {
	t.once.Do(func() { t.done <- time.Now() })
}

func TestDetachedArchiveGraceExpiryCancelsAndDiagnosesAcceptedWork(t *testing.T) {
	if os.Getenv("KENT_ARCHIVE_GRACE_DEBUG_HELPER") == "1" {
		runDetachedArchiveGraceExpiryCase(t, true)
		return
	}
	runDetachedArchiveGraceExpiryCase(t, false)

	command := exec.Command(os.Args[0], "-test.run=^TestDetachedArchiveGraceExpiryCancelsAndDiagnosesAcceptedWork$")
	command.Env = append(os.Environ(), "KENT_ARCHIVE_GRACE_DEBUG_HELPER=1")
	if err := command.Run(); err == nil {
		t.Fatal("debug detached archive grace expiry did not panic")
	}
}

func runDetachedArchiveGraceExpiryCase(t *testing.T, debug bool) {
	t.Helper()
	fixture := newSessionRemovalServiceFixture(t)
	fixture.service.WithDebugMode(debug)
	eventsPath := filepath.Join(fixture.session.Dir(), "events.jsonl")
	events, err := os.OpenFile(eventsPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open event log fixture: %v", err)
	}
	if err := events.Truncate(1 << 30); err != nil {
		_ = events.Close()
		t.Fatalf("expand event log fixture: %v", err)
	}
	if err := events.Close(); err != nil {
		t.Fatalf("close event log fixture: %v", err)
	}

	createdTimer := make(chan *manualArchiveDetachTimer, 1)
	fixture.service.archiveDetachTimerFactory = func(duration time.Duration) archiveDetachTimer {
		if duration != detachedArchiveGracePeriod {
			t.Fatalf("detached archive grace = %s, want %s", duration, detachedArchiveGracePeriod)
		}
		timer := &manualArchiveDetachTimer{done: make(chan time.Time, 1)}
		createdTimer <- timer
		return timer
	}
	diagnostics := make(chan ArchiveDetachExpiryDiagnostic, 1)
	fixture.service.archiveDetachDiagnostic = func(diagnostic ArchiveDetachExpiryDiagnostic) {
		diagnostics <- diagnostic
	}

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "session.tar.zst")
	invocationCtx, cancelInvocation := context.WithCancel(context.Background())
	archiveDone := make(chan error, 1)
	go func() {
		archiveDone <- fixture.service.Archive(
			invocationCtx,
			fixture.session.Meta().SessionID,
			outputPath,
		)
	}()
	awaitArchiveTemporary(t, outputDir, filepath.Base(outputPath))
	cancelInvocation()
	if err := <-archiveDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("detached Archive result = %v, want context canceled", err)
	}
	timer := <-createdTimer
	timer.Fire()
	diagnostic := <-diagnostics
	if diagnostic.SessionID != fixture.session.Meta().SessionID ||
		diagnostic.OutputPath != outputPath ||
		!errors.Is(diagnostic.Cause, ErrDetachedArchiveGraceExpired) {
		t.Fatalf("detached archive diagnostic = %+v", diagnostic)
	}
	if debug {
		select {}
	}

	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive output after grace expiry = %v, want absent", err)
	}
	assertNoSessionArchiveTemporary(t, outputDir, filepath.Base(outputPath))
	if _, err := fixture.metadata.ResolvePersistedSession(t.Context(), fixture.session.Meta().SessionID); err != nil {
		t.Fatalf("Session metadata after grace expiry: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(fixture.session.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session ID: %v", err)
	}
	if err := fixture.authority.WithDestructiveSessionAdmission(
		context.Background(),
		sessionID,
		func(context.Context) error { return nil },
	); err != nil {
		t.Fatalf("Session admission remained held after grace-expired archive joined: %v", err)
	}
}

func awaitArchiveTemporary(t *testing.T, dir, outputBase string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read archive output directory: %v", err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "."+outputBase+".tmp-") {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("archive temporary was not observed")
}

func assertNoSessionArchiveTemporary(t *testing.T, dir, outputBase string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read archive output directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "."+outputBase+".tmp-") {
			t.Fatalf("archive temporary remains: %s", entry.Name())
		}
	}
}
