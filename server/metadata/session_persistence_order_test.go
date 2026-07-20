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

type reconciliationInterleavingObserver struct {
	delegate   session.PersistenceObserver
	reconciler session.EventLogReconciliationObserver
	before     func() error
}

func persistedMetaFromMetadata(metadata session.Metadata) session.Meta {
	return session.Meta{
		SessionID:                       metadata.SessionID,
		Category:                        metadata.Category,
		Name:                            metadata.Name,
		FirstPromptPreview:              metadata.FirstPromptPreview,
		InputDraft:                      metadata.InputDraft,
		InputDraftRecoveryBuffers:       metadata.InputDraftRecoveryBuffers,
		PreviousSessionID:               metadata.PreviousSessionID,
		ParentAgentSessionID:            metadata.ParentAgentSessionID,
		WorkspaceRoot:                   metadata.WorkspaceRoot,
		WorkspaceContainer:              metadata.WorkspaceContainer,
		Continuation:                    metadata.Continuation,
		CreatedAt:                       metadata.CreatedAt,
		UpdatedAt:                       metadata.UpdatedAt,
		ModelRequestCount:               metadata.ModelRequestCount,
		PromptCacheLineageGeneration:    metadata.PromptCacheLineageGeneration,
		HeadlessActive:                  metadata.HeadlessActive,
		CompactionSoonReminderIssued:    metadata.CompactionSoonReminderIssued,
		GeneratedRecoveredWarningIssued: metadata.GeneratedRecoveredWarningIssued,
		PendingModelRecovery:            metadata.PendingModelRecovery,
		WorktreeReminder:                metadata.WorktreeReminder,
		UsageState:                      metadata.UsageState,
		Goal:                            metadata.Goal,
		WorkflowSession:                 metadata.WorkflowSession,
		Locked:                          metadata.Locked,
	}
}

func eventLogRevision(t *testing.T, store *session.Store) int64 {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	return mustEventLogRevision(eventLog)
}

func (r fixedPersistedSessionResolver) ResolvePersistedSession(context.Context, string) (session.PersistedSessionRecord, error) {
	return r.record, nil
}

func (o *reconciliationInterleavingObserver) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	return o.delegate.ObservePersistedStore(ctx, snapshot)
}

func (o *reconciliationInterleavingObserver) ObserveEventLogReconciliation(ctx context.Context, reconciliation session.PersistedEventLogReconciliation) error {
	if before := o.before; before != nil {
		o.before = nil
		if err := before(); err != nil {
			return err
		}
	}
	return o.reconciler.ObserveEventLogReconciliation(ctx, reconciliation)
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
				sessionStore.Metadata().SessionID,
			); err != nil {
				t.Fatalf("clear authoritative execution target: %v", err)
			}

			snapshot := session.PersistedStoreSnapshot{
				SessionDir: sessionStore.Dir(),
				Meta:       persistedMetaFromMetadata(sessionStore.Metadata()),
			}
			snapshot.Meta.Name = "must not persist"
			err := metadataStore.ImportSessionSnapshot(t.Context(), snapshot)
			if !errors.Is(err, test.want) {
				t.Fatalf("ImportSessionSnapshot error = %v, want %v", err, test.want)
			}

			record, err := metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Metadata().SessionID)
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
	staleMeta := persistedMetaFromMetadata(sessionStore.Metadata())
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

	record, err := metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Metadata().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Name != "authoritative name" {
		t.Fatalf("persisted name = %q, want authoritative name", record.Meta.Name)
	}
}

func TestEventUseReconciliationUpdatesOnlyEventLogState(t *testing.T) {
	metadataStore, cfg, binding := newMetadataTestStore(t)
	sessionStore := createMetadataTestSession(t, metadataStore, cfg, binding)
	if err := sessionStore.SetListingMetadata("authoritative name", "authoritative preview"); err != nil {
		t.Fatalf("SetListingMetadata: %v", err)
	}
	staleMeta := persistedMetaFromMetadata(sessionStore.Metadata())
	staleRevision := eventLogRevision(t, sessionStore)
	appendMetadataMessage(t, sessionStore, "step-1", session.MessageRoleUser, "authoritative event")
	if err := metadataStore.ImportSessionSnapshot(t.Context(), session.PersistedStoreSnapshot{
		SessionDir: sessionStore.Dir(),
		Meta:       staleMeta,
	}); err != nil {
		t.Fatalf("restore stale event-log metadata: %v", err)
	}
	reopened, err := session.Open(sessionStore.Dir(), metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	record, err := metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Metadata().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession before event use: %v", err)
	}
	if record.Meta.LastSequence != staleRevision {
		t.Fatalf("metadata-only open reconciled last sequence = %d, want stale %d", record.Meta.LastSequence, staleRevision)
	}
	eventLog, err := reopened.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	if _, err := eventLog.ReadRecentRecords(1); err != nil {
		t.Fatalf("read materialized event log: %v", err)
	}

	record, err = metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Metadata().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.Name != "authoritative name" || record.Meta.FirstPromptPreview != "authoritative preview" {
		t.Fatalf("persisted listing metadata = %+v, want authoritative values", record.Meta)
	}
	if mustEventLogRevision(eventLog) != 1 {
		t.Fatalf("persisted last sequence = %d, want reconciled 1", record.Meta.LastSequence)
	}
}

func TestEventUseReconciliationAppliesHistoryReplacementUsageSemantics(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "compaction invalidates usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadataStore, cfg, binding := newMetadataTestStore(t)
			sessionStore := createMetadataTestSession(t, metadataStore, cfg, binding)
			usage := &session.UsageState{
				InputTokens:          190_000,
				WindowTokens:         200_000,
				CachedInputTokens:    190_000,
				HasCachedInputTokens: true,
			}
			if receipt, err := sessionStore.SetUsageState(usage); err != nil || !receipt.Committed {
				t.Fatalf("SetUsageState receipt=%+v error=%v", receipt, err)
			}
			staleMeta := persistedMetaFromMetadata(sessionStore.Metadata())

			eventLog, err := sessionStore.MaterializeEventLog()
			if err != nil {
				t.Fatalf("materialize event log: %v", err)
			}
			staleRevision := mustEventLogRevision(eventLog)
			_, receipt, err := eventLog.AppendRecord(
				metadataStringPointer("step-compact"),
				session.HistoryReplacementRecord{
					Engine: "local",
					Mode:   session.CompactionModeManual,
				},
			)
			if err != nil || !receipt.Committed {
				t.Fatalf("append history replacement: receipt=%+v error=%v", receipt, err)
			}
			if err := metadataStore.ImportSessionSnapshot(t.Context(), session.PersistedStoreSnapshot{
				SessionDir: sessionStore.Dir(),
				Meta:       staleMeta,
			}); err != nil {
				t.Fatalf("restore stale metadata snapshot: %v", err)
			}

			reopened, err := session.Open(sessionStore.Dir(), metadataStore.AuthoritativeSessionStoreOptions()...)
			if err != nil {
				t.Fatalf("session.Open: %v", err)
			}
			reopenedEventLog, err := reopened.MaterializeEventLog()
			if err != nil {
				t.Fatalf("materialize reopened event log: %v", err)
			}
			if _, err := reopenedEventLog.ReadRecentRecords(1); err != nil {
				t.Fatalf("read materialized event log: %v", err)
			}
			if got := mustEventLogRevision(reopenedEventLog); got != staleRevision+1 {
				t.Fatalf("reconciled last sequence = %d, want %d", got, staleRevision+1)
			}
			if gotUsage := reopened.Metadata().UsageState; gotUsage != nil {
				t.Fatalf("compaction replacement retained stale usage: %+v", gotUsage)
			}
			record, err := metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Metadata().SessionID)
			if err != nil {
				t.Fatalf("ResolvePersistedSession: %v", err)
			}
			if record.Meta.UsageState != nil {
				t.Fatalf("persisted reconciled usage = %+v, want nil", record.Meta.UsageState)
			}
		})
	}
}

func TestEventUseReconciliationDoesNotEraseConcurrentlyPersistedCompactedUsage(t *testing.T) {
	metadataStore, cfg, binding := newMetadataTestStore(t)
	sessionStore := createMetadataTestSession(t, metadataStore, cfg, binding)
	oldUsage := &session.UsageState{
		InputTokens:          190_000,
		WindowTokens:         200_000,
		CachedInputTokens:    190_000,
		HasCachedInputTokens: true,
	}
	if receipt, err := sessionStore.SetUsageState(oldUsage); err != nil || !receipt.Committed {
		t.Fatalf("SetUsageState receipt=%+v error=%v", receipt, err)
	}
	staleMeta := persistedMetaFromMetadata(sessionStore.Metadata())
	eventLog, err := sessionStore.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	staleRevision := mustEventLogRevision(eventLog)
	if _, receipt, err := eventLog.AppendCompactionHistoryReplacement(
		metadataStringPointer("step-compact"),
		session.HistoryReplacementRecord{
			Engine: "local",
			Mode:   session.CompactionModeManual,
		},
	); err != nil || !receipt.Committed {
		t.Fatalf("append history replacement: receipt=%+v error=%v", receipt, err)
	}
	if err := metadataStore.ImportSessionSnapshot(t.Context(), session.PersistedStoreSnapshot{
		SessionDir: sessionStore.Dir(),
		Meta:       staleMeta,
	}); err != nil {
		t.Fatalf("restore stale metadata snapshot: %v", err)
	}

	compactedUsage := &session.UsageState{InputTokens: 2_000, WindowTokens: oldUsage.WindowTokens}
	authoritativeObserver := sessionObserver{store: metadataStore}
	observer := &reconciliationInterleavingObserver{
		delegate:   authoritativeObserver,
		reconciler: authoritativeObserver,
		before: func() error {
			receipt, err := sessionStore.SetUsageState(compactedUsage)
			if err != nil {
				return err
			}
			if !receipt.Committed {
				return errors.New("compacted usage persistence returned an uncommitted success")
			}
			return nil
		},
	}
	options := append(metadataStore.AuthoritativeSessionStoreOptions(), session.WithPersistenceObserver(observer))
	reconciledStore, err := session.Open(sessionStore.Dir(), options...)
	if err != nil {
		t.Fatalf("session.Open: %v", err)
	}
	eventLog, err = reconciledStore.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	if _, err := eventLog.ReadRecentRecords(1); err != nil {
		t.Fatalf("read materialized event log: %v", err)
	}
	if err := reconciledStore.SetName("post-reconciliation metadata write"); err != nil {
		t.Fatalf("SetName through reconciled store: %v", err)
	}

	record, err := metadataStore.ResolvePersistedSession(t.Context(), sessionStore.Metadata().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if record.Meta.UsageState == nil || record.Meta.UsageState.InputTokens != compactedUsage.InputTokens {
		t.Fatalf("stale reconciliation erased newer compacted usage: %+v", record.Meta.UsageState)
	}
	if mustEventLogRevision(eventLog) != staleRevision+1 {
		t.Fatalf("persisted last sequence = %d, want %d", record.Meta.LastSequence, staleRevision+1)
	}
	reopened, err := session.Open(sessionStore.Dir(), metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("reopen authoritative session: %v", err)
	}
	if usage := reopened.Metadata().UsageState; usage == nil || usage.InputTokens != compactedUsage.InputTokens {
		t.Fatalf("authoritative reopen usage = %+v, want %+v", usage, compactedUsage)
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
		sessionStore.Metadata().SessionID,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	if reopened.Metadata().Name != "second update" {
		t.Fatalf("reopened name = %q, want latest mutation", reopened.Metadata().Name)
	}
}
