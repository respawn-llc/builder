package sessionservice

import (
	"context"
	"path/filepath"
	"testing"

	"core/server/auth"
	"core/server/metadata"
	"core/server/session"
	"core/server/session/sessiontest"
	sessionruntime "core/server/sessionruntime"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func appendSessionMessage(t *testing.T, store *session.Store, stepID string, role session.MessageRole, content string) session.EventRecord {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	step := stepID
	message := content
	record, receipt, err := eventLog.AppendRecord(&step, session.MessageRecord{
		Role:    role,
		Content: &message,
	})
	if err != nil || !receipt.Committed {
		t.Fatalf("append typed message: receipt=%+v error=%v", receipt, err)
	}
	return record
}

func userMessageSeqAt(t *testing.T, store *session.Store, n int) int64 {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	window, err := eventLog.ReadRecentRecords(10_000)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	visible := 0
	for _, record := range window.Records {
		payload, err := record.Payload()
		if err != nil {
			t.Fatalf("read persisted event payload: %v", err)
		}
		message, ok := payload.(session.MessageRecord)
		if !ok {
			continue
		}
		if message.Role == session.MessageRoleUser {
			visible++
			if visible == n {
				return record.Seq()
			}
		}
	}
	t.Fatalf("user message %d not found among %d events", n, len(window.Records))
	return 0
}

func newTestSessionLifecycleService(containerDir string, authManager *auth.Manager, options ...[]session.StoreOption) *SessionLifecycleService {
	storeOptions := sessionServiceTestPersistence.Options()
	if len(options) == 0 {
		return newSessionLifecycleServiceWithOptions(containerDir, authManager, storeOptions)
	}
	return newSessionLifecycleServiceWithOptions(containerDir, authManager, options[0])
}

func newSessionLifecycleServiceWithOptions(root string, authManager *auth.Manager, storeOptions []session.StoreOption) *SessionLifecycleService {
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: root,
		StoreOptions:    storeOptions,
	})
	return NewSessionLifecycleService(root, authority, authManager)
}

func newGlobalSessionLifecycleServiceWithOptions(root string, authManager *auth.Manager, storeOptions []session.StoreOption) *SessionLifecycleService {
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: root,
		StoreOptions:    storeOptions,
	})
	return NewGlobalSessionLifecycleService(root, authority, authManager)
}

var sessionServiceTestPersistence = sessiontest.NewPersistence()

type sessionLifecycleRetargeterStub struct {
	result metadata.SessionWorkspaceRetargetResult
	err    error
	req    metadata.SessionWorkspaceRetargetRequest
}

type sessionNavigationTargetResolverStub struct {
	target serverapi.SessionNavigationBinding
	err    error
	calls  []string
}

func (s *sessionNavigationTargetResolverStub) ResolveSessionNavigationBinding(_ context.Context, sessionID string) (serverapi.SessionNavigationBinding, error) {
	s.calls = append(s.calls, sessionID)
	return s.target, s.err
}

func (s *sessionLifecycleRetargeterStub) RetargetWorkspace(_ context.Context, req metadata.SessionWorkspaceRetargetRequest) (metadata.SessionWorkspaceRetargetResult, error) {
	s.req = req
	return s.result, s.err
}

func createPersistedSession(t *testing.T) (string, string, *session.Store) {
	t.Helper()
	persistenceRoot := t.TempDir()
	containerDir := filepath.Join(persistenceRoot, "projects", "project-x", "sessions")
	store, err := session.Create(containerDir, "workspace-x", "/tmp/work", sessioncontract.SessionCategoryMain, sessionServiceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	return persistenceRoot, containerDir, store
}

func createAuthoritativeSessionLifecycleSession(t *testing.T, workspaceRoot string) (config.App, *metadata.Store, metadata.Binding, *session.Store) {
	t.Helper()
	cfg := config.App{PersistenceRoot: t.TempDir(), WorkspaceRoot: workspaceRoot}
	store, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	binding, err := store.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	sess, err := session.Create(
		filepath.Join(filepath.Join(cfg.PersistenceRoot, "projects"), binding.ProjectID, "sessions"),
		filepath.Base(cfg.WorkspaceRoot),
		cfg.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		store.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		_ = store.Close()
		t.Fatalf("session.Create: %v", err)
	}
	if err := sess.SetName("incident triage"); err != nil {
		_ = store.Close()
		t.Fatalf("SetName: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return cfg, store, binding, sess
}
