package sessionservice

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/sessionruntime"
	"core/shared/runtimeids"
)

type sessionRemovalMetadataDelegate struct {
	*metadata.Store
	resolve func(context.Context, string) (session.PersistedSessionRecord, error)
	delete  func(context.Context, string) error
}

type archiveExpiryErrorHandler struct {
	err chan error
}

func (h archiveExpiryErrorHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h archiveExpiryErrorHandler) Handle(_ context.Context, record slog.Record) error {
	record.Attrs(func(attribute slog.Attr) bool {
		if err, ok := attribute.Value.Any().(error); ok {
			h.err <- err
			return false
		}
		return true
	})
	return nil
}

func (h archiveExpiryErrorHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h archiveExpiryErrorHandler) WithGroup(string) slog.Handler {
	return h
}

func (m sessionRemovalMetadataDelegate) ResolvePersistedSession(ctx context.Context, sessionID string) (session.PersistedSessionRecord, error) {
	if m.resolve != nil {
		return m.resolve(ctx, sessionID)
	}
	return m.Store.ResolvePersistedSession(ctx, sessionID)
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
		WithPersistedSessionResolver(metadataStore)
	return sessionRemovalServiceFixture{service: service, authority: authority, metadata: metadataStore, session: persisted}
}

func TestSessionLifecycleRemovalReportsDurableOutcomes(t *testing.T) {
	t.Run("successful delete", func(t *testing.T) {
		fixture := newSessionRemovalServiceFixture(t)
		if err := fixture.service.Delete(context.Background(), fixture.session.Meta().SessionID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := fixture.metadata.ResolvePersistedSession(t.Context(), fixture.session.Meta().SessionID); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("resolve deleted Session = %v, want Session not found", err)
		}
	})

	t.Run("archive metadata not removed", func(t *testing.T) {
		fixture := newSessionRemovalServiceFixture(t)
		blocker := &metadata.SessionInUseError{SessionID: fixture.session.Meta().SessionID}
		fixture.service.WithPersistedSessionResolver(sessionRemovalMetadataDelegate{
			Store: fixture.metadata,
			delete: func(_ context.Context, _ string) error {
				return blocker
			},
		})
		outputPath := filepath.Join(t.TempDir(), "session.tar.zst")

		err := fixture.service.Archive(context.Background(), fixture.session.Meta().SessionID, outputPath)
		if !errors.Is(err, blocker) {
			t.Fatalf("Archive error = %v, want metadata blocker", err)
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
		fixture.service.WithPersistedSessionResolver(sessionRemovalMetadataDelegate{
			Store: fixture.metadata,
			delete: func(ctx context.Context, sessionID string) error {
				if err := fixture.metadata.DeleteSession(ctx, sessionID); err != nil {
					return err
				}
				if err := os.Remove(eventsPath); err != nil {
					return err
				}
				if err := os.Mkdir(eventsPath, 0o700); err != nil {
					return err
				}
				return os.WriteFile(filepath.Join(eventsPath, "remaining"), nil, 0o600)
			},
		})
		outputPath := filepath.Join(t.TempDir(), "session.tar.zst")

		err := fixture.service.Archive(context.Background(), fixture.session.Meta().SessionID, outputPath)
		var removalErr *SessionRemovalCleanupError
		if !errors.As(err, &removalErr) {
			t.Fatalf("Archive error = %v, want cleanup failure for %q", err, eventsPath)
		}
		if removalErr.RemainingPath != eventsPath {
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

func TestArchiveDestinationRejectionLeavesIdleRuntimeOpen(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	fixture.openRuntime(t)
	service := NewGlobalSessionLifecycleService("", fixture.authority, nil).
		WithPersistedSessionResolver(fixture.metadata)
	assertRuntimeOpen := func() {
		t.Helper()
		if err := fixture.authority.WithCurrentRuntime(
			t.Context(),
			fixture.childID,
			func(context.Context, *runtime.Engine) error { return nil },
		); err != nil {
			t.Fatalf("idle Runtime changed by destination rejection: %v", err)
		}
	}

	err := service.Archive(t.Context(), fixture.child.Meta().SessionID, "relative.tar.zst")
	var invalid *session.InvalidArchiveOutputPathError
	if !errors.As(err, &invalid) {
		t.Fatalf("Archive relative-path error = %v, want invalid output path", err)
	}
	assertRuntimeOpen()

	outputPath := filepath.Join(t.TempDir(), "existing.tar.zst")
	if err := os.WriteFile(outputPath, []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing archive destination: %v", err)
	}

	err = service.Archive(t.Context(), fixture.child.Meta().SessionID, outputPath)
	var exists *session.ArchiveOutputExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("Archive error = %v, want output exists", err)
	}
	assertRuntimeOpen()
	if body, err := os.ReadFile(outputPath); err != nil || string(body) != "existing" {
		t.Fatalf("existing destination = %q, %v", body, err)
	}
}

func TestArchiveDestinationCreatedAfterPreflightRetainsSession(t *testing.T) {
	fixture := newSessionRemovalServiceFixture(t)
	outputDir := filepath.Join(t.TempDir(), "created", "nested")
	outputPath := filepath.Join(outputDir, "session.tar.zst")
	if _, err := os.Lstat(outputDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output parent before Archive = %v, want absent", err)
	}
	fixture.service.WithPersistedSessionResolver(sessionRemovalMetadataDelegate{
		Store: fixture.metadata,
		resolve: func(ctx context.Context, sessionID string) (session.PersistedSessionRecord, error) {
			record, err := fixture.metadata.ResolvePersistedSession(ctx, sessionID)
			if err != nil {
				return session.PersistedSessionRecord{}, err
			}
			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				return session.PersistedSessionRecord{}, err
			}
			if err := os.WriteFile(outputPath, []byte("raced"), 0o644); err != nil {
				return session.PersistedSessionRecord{}, err
			}
			return record, nil
		},
	})

	err := fixture.service.Archive(context.Background(), fixture.session.Meta().SessionID, outputPath)
	var exists *session.ArchiveOutputExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("Archive error = %v, want ArchiveOutputExistsError", err)
	}
	if exists.Path != outputPath {
		t.Fatalf("existing output path = %q, want %q", exists.Path, outputPath)
	}
	if body, readErr := os.ReadFile(outputPath); readErr != nil || string(body) != "raced" {
		t.Fatalf("raced output = %q, %v", body, readErr)
	}
	if _, err := fixture.metadata.ResolvePersistedSession(
		t.Context(),
		fixture.session.Meta().SessionID,
	); err != nil {
		t.Fatalf("Session metadata after publication failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.session.Dir(), "events.jsonl")); err != nil {
		t.Fatalf("Session artifacts after publication failure: %v", err)
	}
	entries, readErr := os.ReadDir(outputDir)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(outputPath) {
		t.Fatalf("output directory after publication failure = %v, %v", entries, readErr)
	}
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
	cfg, metadataStore, _, persisted := createAuthoritativeSessionLifecycleSession(t, t.TempDir())
	outputDir := t.TempDir()
	synctest.Test(t, func(t *testing.T) {
		authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
			PersistenceRoot: cfg.PersistenceRoot,
			StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
		})
		defer func() {
			if err := authority.Close(context.Background()); err != nil {
				t.Errorf("close Session Runtime authority: %v", err)
			}
		}()
		service := NewGlobalSessionLifecycleService(cfg.PersistenceRoot, authority, nil).
			WithPersistedSessionResolver(metadataStore).
			WithDebugMode(debug)
		resolveStarted := make(chan struct{})
		service.WithPersistedSessionResolver(sessionRemovalMetadataDelegate{
			Store: metadataStore,
			resolve: func(ctx context.Context, _ string) (session.PersistedSessionRecord, error) {
				close(resolveStarted)
				<-ctx.Done()
				return session.PersistedSessionRecord{}, context.Cause(ctx)
			},
		})
		diagnosticErrors := make(chan error, 1)
		previousLogger := slog.Default()
		slog.SetDefault(slog.New(archiveExpiryErrorHandler{err: diagnosticErrors}))
		defer slog.SetDefault(previousLogger)

		outputPath := filepath.Join(outputDir, "session.tar.zst")
		invocationCtx, cancelInvocation := context.WithCancel(context.Background())
		archiveDone := make(chan error, 1)
		go func() {
			archiveDone <- service.Archive(
				invocationCtx,
				persisted.Meta().SessionID,
				outputPath,
			)
		}()
		<-resolveStarted
		cancelInvocation()
		if err := <-archiveDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("detached Archive result = %v, want context canceled", err)
		}

		sessionID, err := runtimeids.ParseSessionID(persisted.Meta().SessionID)
		if err != nil {
			t.Fatalf("parse Session ID: %v", err)
		}
		competingAdmission := make(chan error, 1)
		go func() {
			competingAdmission <- authority.WithDestructiveSessionAdmission(
				context.Background(),
				sessionID,
				func(context.Context) error { return nil },
			)
		}()
		synctest.Wait()
		select {
		case err := <-competingAdmission:
			t.Fatalf("Session admission released before grace expiry: %v", err)
		default:
		}

		// synctest advances the fixed product grace period without changing production timing.
		time.Sleep(detachedArchiveGracePeriod)
		synctest.Wait()
		if debug {
			t.Fatal("debug detached archive grace expiry did not panic")
		}
		if err := <-competingAdmission; err != nil {
			t.Fatalf("Session admission after grace expiry: %v", err)
		}
		select {
		case diagnosticErr := <-diagnosticErrors:
			if !errors.Is(diagnosticErr, ErrDetachedArchiveGraceExpired) {
				t.Fatalf("detached archive diagnostic = %v, want grace-expiry cause", diagnosticErr)
			}
		default:
			t.Fatal("detached archive expiry diagnostic was not emitted")
		}
		if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("archive output after grace expiry = %v, want absent", err)
		}
		if _, err := metadataStore.ResolvePersistedSession(
			context.Background(),
			persisted.Meta().SessionID,
		); err != nil {
			t.Fatalf("Session metadata after grace expiry: %v", err)
		}
	})
}

func TestAuthorityCloseCancelsAndJoinsAcceptedSessionRemoval(t *testing.T) {
	tests := []struct {
		name         string
		run          func(sessionRemovalServiceFixture, string) error
		verifyOutput func(string) error
	}{
		{name: "archive", run: func(f sessionRemovalServiceFixture, outputPath string) error {
			return f.service.Archive(context.Background(), f.session.Meta().SessionID, outputPath)
		}, verifyOutput: func(outputPath string) error { _, err := os.Stat(outputPath); return err }},
		{name: "delete", run: func(f sessionRemovalServiceFixture, _ string) error {
			return f.service.Delete(context.Background(), f.session.Meta().SessionID)
		}, verifyOutput: func(string) error { return nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRemovalServiceFixture(t)
			deleteStarted := make(chan struct{})
			fixture.service.WithPersistedSessionResolver(sessionRemovalMetadataDelegate{
				Store: fixture.metadata,
				delete: func(ctx context.Context, _ string) error {
					close(deleteStarted)
					<-ctx.Done()
					return context.Cause(ctx)
				},
			})
			operationDone := make(chan error, 1)
			outputPath := filepath.Join(t.TempDir(), "session.tar.zst")
			go func() {
				operationDone <- test.run(fixture, outputPath)
			}()
			select {
			case <-deleteStarted:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not reach metadata removal", test.name)
			}

			sessionID, err := runtimeids.ParseSessionID(fixture.session.Meta().SessionID)
			if err != nil {
				t.Fatalf("parse Session ID: %v", err)
			}
			competingAdmission := make(chan error, 1)
			go func() {
				competingAdmission <- fixture.authority.WithDestructiveSessionAdmission(
					context.Background(),
					sessionID,
					func(context.Context) error { return nil },
				)
			}()
			select {
			case err := <-competingAdmission:
				t.Fatalf("Session admission released before %s completed: %v", test.name, err)
			case <-time.After(20 * time.Millisecond):
			}

			if err := fixture.authority.Close(context.Background()); err != nil {
				t.Fatalf("close Authority: %v", err)
			}
			if err := <-operationDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("%s result = %v, want cancellation", test.name, err)
			}
			if err := <-competingAdmission; !errors.Is(err, sessionruntime.ErrAuthorityClosed) {
				t.Fatalf("competing admission after shutdown = %v, want Authority closed", err)
			}
			if _, err := fixture.metadata.ResolvePersistedSession(
				t.Context(),
				fixture.session.Meta().SessionID,
			); err != nil {
				t.Fatalf("Session metadata after canceled %s: %v", test.name, err)
			}
			if err := test.verifyOutput(outputPath); err != nil {
				t.Fatalf("%s output after shutdown cancellation: %v", test.name, err)
			}
		})
	}
}
