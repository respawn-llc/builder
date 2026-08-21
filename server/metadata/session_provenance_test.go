package metadata

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

func TestTypedSessionProvenanceRoundTripsAndMakesSessionVisible(t *testing.T) {
	t.Parallel()
	store, cfg, binding := newMetadataTestStore(t)
	sessionDir := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	root, err := session.Create(
		sessionDir,
		binding.WorkspaceName,
		cfg.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	child, err := session.NewLazy(
		sessionDir,
		binding.WorkspaceName,
		cfg.WorkspaceRoot,
		sessioncontract.SessionCategorySubagent,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := session.InitializeCreationContext(child, root, session.SessionCreationSourceParentAgent, session.ChildContextOptions{}); err != nil {
		t.Fatalf("initialize child provenance: %v", err)
	}
	if err := child.EnsureDurable(); err != nil {
		t.Fatalf("persist child: %v", err)
	}

	record, err := store.ResolvePersistedSession(context.Background(), child.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	wantParent, err := runtimeids.ParseSessionID(root.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID root: %v", err)
	}
	if record.Meta.PreviousSessionID != nil ||
		record.Meta.ParentAgentSessionID == nil ||
		*record.Meta.ParentAgentSessionID != wantParent {
		t.Fatalf("resolved provenance = previous:%v parent-agent:%v", record.Meta.PreviousSessionID, record.Meta.ParentAgentSessionID)
	}

	page, err := store.ListSessionPage(
		context.Background(),
		binding.ProjectID,
		sessioncontract.SessionCategorySubagent,
		0,
		10,
	)
	if err != nil {
		t.Fatalf("ListSessionPage: %v", err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID.String() != child.Meta().SessionID {
		t.Fatalf("visible sessions = %+v, want child only", page.Sessions)
	}
}

func TestResolvePersistedSessionRejectsMalformedPresentProvenanceID(t *testing.T) {
	t.Parallel()
	store, cfg, binding := newMetadataTestStore(t)
	sess := createMetadataTestSession(t, store, cfg, binding)
	if _, err := store.db.ExecContext(
		context.Background(),
		"UPDATE sessions SET parent_agent_session_id = ? WHERE id = ?",
		"../escape",
		sess.Meta().SessionID,
	); err != nil {
		t.Fatalf("persist malformed provenance: %v", err)
	}
	_, err := store.ResolvePersistedSession(context.Background(), sess.Meta().SessionID)
	if err == nil {
		t.Fatal("ResolvePersistedSession accepted malformed parent-agent session ID")
	}
	if errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("ResolvePersistedSession error = %v, want malformed provenance failure", err)
	}
}
