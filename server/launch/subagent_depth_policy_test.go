package launch

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestPlannerReadsOlderAncestryAsMetadataWithoutOpeningTranscript(t *testing.T) {
	planner, containerDir, persistence := newMetadataOnlyAncestryPlanner(t)
	caller := createMetadataOnlyAncestryCaller(t, containerDir, persistence)
	syntheticAncestorID := mustMetadataOnlyAncestrySessionID(t, "metadata-only-ancestor")
	callerRecord, err := persistence.ResolvePersistedSession(context.Background(), caller.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolve caller persisted session: %v", err)
	}
	callerRecord.Meta = cloneMetadataOnlyAncestryMeta(callerRecord.Meta)
	callerRecord.Meta.ParentAgentSessionID = &syntheticAncestorID
	resolver := &metadataOnlyAncestryResolver{
		base: persistence,
		records: map[string]session.PersistedSessionRecord{
			caller.Meta().SessionID: callerRecord,
			syntheticAncestorID.String(): {
				SessionDir: filepath.Join(t.TempDir(), "must-not-open"),
				Meta: &session.Meta{
					SessionID: syntheticAncestorID.String(),
					Category:  metadataOnlyAncestryCategory(sessioncontract.SessionCategorySubagent),
				},
			},
		},
	}
	planner.PersistedSessions = resolver

	plan, err := planner.PlanSession(context.Background(), SessionRequest{
		Mode:   ModeHeadless,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(mustMetadataOnlyAncestrySessionID(t, caller.Meta().SessionID))),
	})
	if err != nil {
		t.Fatalf("PlanSession metadata-only ancestry: %v", err)
	}
	if plan.Descriptor.SessionID().IsZero() {
		t.Fatal("metadata-only ancestry did not create a child")
	}
	if want := []string{caller.Meta().SessionID, syntheticAncestorID.String()}; !reflect.DeepEqual(resolver.calls, want) {
		t.Fatalf("resolver calls = %v, want %v", resolver.calls, want)
	}
}

func newMetadataOnlyAncestryPlanner(t *testing.T) (Planner, string, *sessiontest.Persistence) {
	t.Helper()
	root := t.TempDir()
	containerDir := filepath.Join(root, "projects", "project-a", "sessions")
	persistence := sessiontest.NewPersistence()
	return Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: root,
			Settings: config.Settings{
				Model:            "gpt-5",
				MaxSubagentDepth: 2,
			},
		},
		ContainerDir:             containerDir,
		StoreOptions:             persistence.Options(),
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: testProjectBoundaryResolver{},
	}, containerDir, persistence
}

func createMetadataOnlyAncestryCaller(t *testing.T, containerDir string, persistence *sessiontest.Persistence) *session.Store {
	t.Helper()
	store, err := session.NewLazy(containerDir, filepath.Base(containerDir), "/tmp/workspace-a", sessioncontract.SessionCategoryMain, persistence.Options()...)
	if err != nil {
		t.Fatalf("create caller session: %v", err)
	}
	if err := session.InitializeCreationContext(store, nil, session.SessionCreationSourceIndependent, session.ChildContextOptions{}); err != nil {
		t.Fatalf("initialize caller creation context: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist caller session: %v", err)
	}
	return store
}

type metadataOnlyAncestryResolver struct {
	base    session.PersistedSessionResolver
	records map[string]session.PersistedSessionRecord
	calls   []string
}

func (r *metadataOnlyAncestryResolver) ResolvePersistedSession(ctx context.Context, sessionID string) (session.PersistedSessionRecord, error) {
	r.calls = append(r.calls, sessionID)
	if record, ok := r.records[sessionID]; ok {
		record.Meta = cloneMetadataOnlyAncestryMeta(record.Meta)
		return record, nil
	}
	return r.base.ResolvePersistedSession(ctx, sessionID)
}

func cloneMetadataOnlyAncestryMeta(meta *session.Meta) *session.Meta {
	if meta == nil {
		return nil
	}
	cloned := *meta
	if meta.ParentAgentSessionID != nil {
		id := *meta.ParentAgentSessionID
		cloned.ParentAgentSessionID = &id
	}
	return &cloned
}

func metadataOnlyAncestryCategory(category sessioncontract.SessionCategory) *sessioncontract.SessionCategory {
	copied := category
	return &copied
}

func mustMetadataOnlyAncestrySessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("parse session id %q: %v", raw, err)
	}
	return id
}
