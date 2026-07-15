package metadata

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/server/session"
	"core/shared/sessioncontract"
)

type blockingOrderedSessionObserver struct {
	store     *Store
	mu        sync.Mutex
	blockNext bool
	blocked   chan struct{}
	release   chan struct{}
	persisted chan string
}

type fixedPersistedSessionResolver struct {
	record session.PersistedSessionRecord
}

func (r fixedPersistedSessionResolver) ResolvePersistedSession(context.Context, string) (session.PersistedSessionRecord, error) {
	return r.record, nil
}

func newBlockingOrderedSessionObserver(store *Store) *blockingOrderedSessionObserver {
	return &blockingOrderedSessionObserver{
		store:     store,
		blocked:   make(chan struct{}),
		release:   make(chan struct{}),
		persisted: make(chan string, 3),
	}
}

func (o *blockingOrderedSessionObserver) Arm() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.blockNext = true
}

func (o *blockingOrderedSessionObserver) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	o.mu.Lock()
	block := o.blockNext
	o.blockNext = false
	o.mu.Unlock()
	if block {
		close(o.blocked)
		select {
		case <-o.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := o.store.ImportSessionSnapshot(ctx, snapshot); err != nil {
		return err
	}
	o.persisted <- snapshot.Meta.Name
	return nil
}

func TestSessionPersistenceRejectsMissingAuthoritativeExecutionTarget(t *testing.T) {
	tests := []struct {
		name         string
		metadataJSON string
		want         error
	}{
		{
			name:         "workspace root",
			metadataJSON: `{"workspace_root":"","workspace_container":"workspace"}`,
			want:         errSessionWorkspaceRootRequired,
		},
		{
			name:         "workspace container",
			metadataJSON: `{"workspace_root":"/tmp/workspace","workspace_container":""}`,
			want:         errSessionWorkspaceContainerRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadataStore, cfg, binding := newMetadataTestStore(t)
			sessionStore := createMetadataTestSession(t, metadataStore, cfg, binding)
			if _, err := metadataStore.db.ExecContext(
				t.Context(),
				"UPDATE sessions SET workspace_id = ?, metadata_json = ? WHERE id = ?",
				sql.NullString{},
				test.metadataJSON,
				sessionStore.Meta().SessionID,
			); err != nil {
				t.Fatalf("clear authoritative execution target: %v", err)
			}

			snapshot := session.PersistedStoreSnapshot{
				SessionDir: sessionStore.Dir(),
				Meta:       sessionStore.Meta(),
			}
			snapshot.Meta.Name = "must not persist"
			err := metadataStore.ImportSessionSnapshot(t.Context(), snapshot)
			if !errors.Is(err, test.want) {
				t.Fatalf("ImportSessionSnapshot error = %v, want %v", err, test.want)
			}

			record, err := metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Meta().SessionID)
			if err != nil {
				t.Fatalf("ResolvePersistedSession: %v", err)
			}
			if record.Meta.Name == snapshot.Meta.Name {
				t.Fatalf("rejected snapshot name persisted: %q", record.Meta.Name)
			}
		})
	}
}

func TestReadOnlyOpenDoesNotRepublishResolvedSnapshot(t *testing.T) {
	metadataStore, cfg, binding := newMetadataTestStore(t)
	sessionStore := createMetadataTestSession(t, metadataStore, cfg, binding)
	staleMeta := sessionStore.Meta()
	if err := sessionStore.SetName("authoritative name"); err != nil {
		t.Fatalf("SetName: %v", err)
	}

	options := append(
		metadataStore.AuthoritativeSessionStoreOptions(),
		session.WithPersistedSessionResolver(fixedPersistedSessionResolver{record: session.PersistedSessionRecord{
			SessionDir: sessionStore.Dir(),
			Meta:       &staleMeta,
		}}),
	)
	if _, err := session.Open(sessionStore.Dir(), options...); err != nil {
		t.Fatalf("session.Open: %v", err)
	}

	record, err := metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Name != "authoritative name" {
		t.Fatalf("persisted name = %q, want authoritative name", record.Meta.Name)
	}
}

func TestReconciliationOpenUpdatesOnlyEventLogState(t *testing.T) {
	metadataStore, cfg, binding := newMetadataTestStore(t)
	sessionStore := createMetadataTestSession(t, metadataStore, cfg, binding)
	staleMeta := sessionStore.Meta()
	if err := sessionStore.SetListingMetadata("authoritative name", "authoritative preview"); err != nil {
		t.Fatalf("SetListingMetadata: %v", err)
	}
	if _, _, err := sessionStore.AppendEvent(
		"step-1",
		"message",
		map[string]string{"role": "user", "content": "authoritative event"},
	); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	options := append(
		metadataStore.AuthoritativeSessionStoreOptions(),
		session.WithPersistedSessionResolver(fixedPersistedSessionResolver{record: session.PersistedSessionRecord{
			SessionDir: sessionStore.Dir(),
			Meta:       &staleMeta,
		}}),
	)
	if _, err := session.Open(sessionStore.Dir(), options...); err != nil {
		t.Fatalf("session.Open: %v", err)
	}

	record, err := metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Name != "authoritative name" || record.Meta.FirstPromptPreview != "authoritative preview" {
		t.Fatalf("persisted listing metadata = %+v, want authoritative values", record.Meta)
	}
	if record.Meta.LastSequence != 1 {
		t.Fatalf("persisted last sequence = %d, want reconciled 1", record.Meta.LastSequence)
	}
}

func TestConcurrentSessionPersistencePublishesSnapshotsInMutationOrder(t *testing.T) {
	metadataStore, cfg, binding := newMetadataTestStore(t)
	observer := newBlockingOrderedSessionObserver(metadataStore)
	sessionStore, err := session.Create(
		filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions"),
		binding.WorkspaceName,
		cfg.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		session.WithPersistenceObserver(observer),
		session.WithPersistedSessionResolver(metadataStore),
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	<-observer.persisted

	observer.Arm()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- sessionStore.SetName("first update")
	}()
	select {
	case <-observer.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("first persistence did not reach the observer")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- sessionStore.SetName("second update")
	}()
	close(observer.release)

	if err := <-firstDone; err != nil {
		t.Fatalf("first SetName: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second SetName: %v", err)
	}
	persistedNames := make([]string, 0, 2)
	for len(persistedNames) < 2 {
		select {
		case name := <-observer.persisted:
			persistedNames = append(persistedNames, name)
		case <-time.After(2 * time.Second):
			t.Fatal("persistence observations did not complete")
		}
	}
	if persistedNames[0] != "first update" || persistedNames[1] != "second update" {
		t.Fatalf("persisted names = %q, want mutation order", persistedNames)
	}

	reopened, err := session.OpenByID(
		cfg.PersistenceRoot,
		sessionStore.Meta().SessionID,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	if reopened.Meta().Name != "second update" {
		t.Fatalf("reopened name = %q, want latest mutation", reopened.Meta().Name)
	}
}
