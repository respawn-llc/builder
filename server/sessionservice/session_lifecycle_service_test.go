package sessionservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/auth"
	"core/server/llm"
	"core/server/metadata"
	"core/server/registry"
	"core/server/requestmemo"
	"core/server/runlog"
	"core/server/session"
	sessionruntime "core/server/sessionruntime"
	"core/shared/config"
	"core/shared/rollbacktarget"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func userMessageSeqAt(t *testing.T, store *session.Store, n int) int64 {
	t.Helper()
	window, err := store.ReadRecentEvents(10_000)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	visible := 0
	for _, evt := range window.Events {
		if evt.Kind != "message" {
			continue
		}
		var msg struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			continue
		}
		if msg.Role == "user" {
			visible++
			if visible == n {
				return evt.Seq
			}
		}
	}
	t.Fatalf("user message %d not found among %d events", n, len(window.Events))
	return 0
}

func newTestSessionLifecycleService(containerDir string, authManager *auth.Manager) *SessionLifecycleService {
	return NewSessionLifecycleService(containerDir, nil, authManager)
}

func createPersistedSession(t *testing.T) (string, string, *session.Store) {
	t.Helper()
	persistenceRoot := t.TempDir()
	containerDir := filepath.Join(persistenceRoot, "projects", "project-x", "sessions")
	store, err := session.Create(containerDir, "workspace-x", "/tmp/work")
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

func TestMetadataBackedLoopbackClientOwnsMetadataStore(t *testing.T) {
	cfg, _, _, sess := createAuthoritativeSessionLifecycleSession(t, t.TempDir())
	lifecycleClient, err := NewMetadataBackedSessionLifecycleClient(cfg.PersistenceRoot, nil)
	if err != nil {
		t.Fatalf("NewMetadataBackedSessionLifecycleClient: %v", err)
	}
	if _, err := lifecycleClient.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{SessionID: sess.Meta().SessionID}); err != nil {
		t.Fatalf("GetInitialInput before Close: %v", err)
	}
	if err := lifecycleClient.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := lifecycleClient.Close(); err != nil {
		t.Fatalf("Close duplicate: %v", err)
	}
	if _, err := lifecycleClient.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{SessionID: sess.Meta().SessionID}); err == nil || !errors.Is(err, errLifecycleClientClosed) {
		t.Fatalf("GetInitialInput after Close error = %v, want closed client", err)
	}
}

func TestServiceGetInitialInputPrefersStoredDraft(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if err := store.SetInputDraft("draft from store"); err != nil {
		t.Fatalf("set input draft: %v", err)
	}

	service := newTestSessionLifecycleService(containerDir, nil)
	resp, err := service.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{
		SessionID:       store.Meta().SessionID,
		TransitionInput: "transition input",
	})
	if err != nil {
		t.Fatalf("GetInitialInput: %v", err)
	}
	if resp.Input != "draft from store" {
		t.Fatalf("input = %q, want %q", resp.Input, "draft from store")
	}
}

func TestServiceGetInitialInputAllowsEmptySessionID(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	resp, err := service.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{
		TransitionInput: "transition input",
	})
	if err != nil {
		t.Fatalf("GetInitialInput: %v", err)
	}
	if resp.Input != "transition input" {
		t.Fatalf("input = %q, want %q", resp.Input, "transition input")
	}
}

func TestServiceGetInitialInputRejectsPathLikeSessionID(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	_, err := service.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{
		SessionID: "../session-1",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestServicePersistInputDraftWritesBySessionID(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if err := store.SetName("session name"); err != nil {
		t.Fatalf("set session name: %v", err)
	}

	service := newTestSessionLifecycleService(containerDir, nil)
	if _, err := service.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{
		ClientRequestID: "req-1",
		SessionID:       store.Meta().SessionID,
		Input:           "saved by service",
	}); err != nil {
		t.Fatalf("PersistInputDraft: %v", err)
	}

	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	if reopened.Meta().InputDraft != "saved by service" {
		t.Fatalf("input draft = %q, want %q", reopened.Meta().InputDraft, "saved by service")
	}
}

func TestServiceRetargetSessionWorkspaceUpdatesBindingAndSession(t *testing.T) {
	oldWorkspace := t.TempDir()
	newWorkspace := t.TempDir()
	cfg, metadataStore, binding, sess := createAuthoritativeSessionLifecycleSession(t, oldWorkspace)

	service := NewGlobalSessionLifecycleService(cfg.PersistenceRoot, nil, nil)
	resp, err := service.RetargetSessionWorkspace(context.Background(), serverapi.SessionRetargetWorkspaceRequest{
		ClientRequestID: "req-1",
		SessionID:       sess.Meta().SessionID,
		WorkspaceRoot:   newWorkspace,
	})
	if err != nil {
		t.Fatalf("RetargetSessionWorkspace: %v", err)
	}
	if resp.Binding.ProjectID != binding.ProjectID {
		t.Fatalf("binding project id = %q, want %q", resp.Binding.ProjectID, binding.ProjectID)
	}
	target, err := metadataStore.ResolveSessionExecutionTarget(context.Background(), sess.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceRoot != resp.Binding.CanonicalRoot {
		t.Fatalf("target workspace root = %q, want %q", target.WorkspaceRoot, resp.Binding.CanonicalRoot)
	}
	reopened, err := session.OpenByID(cfg.PersistenceRoot, sess.Meta().SessionID, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if reopened.Meta().WorkspaceRoot != resp.Binding.CanonicalRoot {
		t.Fatalf("session workspace root = %q, want %q", reopened.Meta().WorkspaceRoot, resp.Binding.CanonicalRoot)
	}
}

func TestServiceRetargetSessionWorkspaceRequiresPersistenceRoot(t *testing.T) {
	service := NewSessionLifecycleService(t.TempDir(), nil, nil)
	_, err := service.RetargetSessionWorkspace(context.Background(), serverapi.SessionRetargetWorkspaceRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		WorkspaceRoot:   t.TempDir(),
	})
	if err == nil || !errors.Is(err, errPersistenceRootRequired) {
		t.Fatalf("RetargetSessionWorkspace error = %v, want persistence root is required", err)
	}
}

func TestServicePersistInputDraftPersistsAndDedupes(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if err := store.SetName("session name"); err != nil {
		t.Fatalf("set session name: %v", err)
	}
	service := NewSessionLifecycleService(containerDir, nil, nil)
	req := serverapi.SessionPersistInputDraftRequest{
		ClientRequestID: "req-1",
		SessionID:       store.Meta().SessionID,
		Input:           "saved by service",
	}

	if _, err := service.PersistInputDraft(context.Background(), req); err != nil {
		t.Fatalf("PersistInputDraft first: %v", err)
	}
	if _, err := service.PersistInputDraft(context.Background(), req); err != nil {
		t.Fatalf("PersistInputDraft replay: %v", err)
	}
	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	if reopened.Meta().InputDraft != "saved by service" {
		t.Fatalf("input draft = %q, want %q", reopened.Meta().InputDraft, "saved by service")
	}
}

func TestServicePersistInputDraftRejectsClientRequestIDPayloadMismatch(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if err := store.SetName("session name"); err != nil {
		t.Fatalf("set session name: %v", err)
	}
	service := newTestSessionLifecycleService(containerDir, nil)
	first := serverapi.SessionPersistInputDraftRequest{
		ClientRequestID: "req-1",
		SessionID:       store.Meta().SessionID,
		Input:           "saved by service",
	}

	if _, err := service.PersistInputDraft(context.Background(), first); err != nil {
		t.Fatalf("PersistInputDraft first: %v", err)
	}
	second := first
	second.Input = "different draft"
	if _, err := service.PersistInputDraft(context.Background(), second); err == nil || !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("PersistInputDraft mismatch error = %v, want request id payload mismatch", err)
	}
	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	if reopened.Meta().InputDraft != "saved by service" {
		t.Fatalf("input draft = %q, want %q", reopened.Meta().InputDraft, "saved by service")
	}
}

func TestServicePersistInputDraftRejectsPathLikeSessionID(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	_, err := service.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{
		ClientRequestID: "req-1",
		SessionID:       "sessions/workspace-x/session-1",
		Input:           "draft",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestServiceResolveTransitionRejectsPathLikeSessionID(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	_, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "req-1",
		SessionID:       "../session-1",
		Transition: serverapi.SessionTransition{
			Action: "continue",
		},
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestServiceResolveTransitionForkRollbackCreatesFork(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-2", "message", llm.Message{Role: llm.RoleUser, Content: "u2"}); err != nil {
		t.Fatalf("append second user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-2", "message", llm.Message{Role: llm.RoleAssistant, Content: "a2"}); err != nil {
		t.Fatalf("append second assistant message: %v", err)
	}

	service := newTestSessionLifecycleService(containerDir, nil)
	resp, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "req-1",
		SessionID:       store.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			InitialPrompt:        "edited prompt",
			ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, store, 2)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	if !resp.ShouldContinue {
		t.Fatal("expected lifecycle continuation")
	}
	if resp.NextSessionID == "" || resp.NextSessionID == store.Meta().SessionID {
		t.Fatalf("unexpected fork session id %q", resp.NextSessionID)
	}
	if resp.InitialPrompt != "edited prompt" {
		t.Fatalf("initial prompt = %q, want %q", resp.InitialPrompt, "edited prompt")
	}
	if _, err := session.Open(filepath.Join(containerDir, resp.NextSessionID)); err != nil {
		t.Fatalf("open forked session store: %v", err)
	}
}

func TestServiceResolveTransitionForkRollbackUsesTargetToken(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-2", "message", llm.Message{Role: llm.RoleUser, Content: "u2"}); err != nil {
		t.Fatalf("append second user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-2", "message", llm.Message{Role: llm.RoleAssistant, Content: "a2"}); err != nil {
		t.Fatalf("append second assistant message: %v", err)
	}

	service := newTestSessionLifecycleService(containerDir, nil)
	resp, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "req-1",
		SessionID:       store.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			InitialPrompt:        "edited prompt",
			ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, store, 2)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	if _, err := session.Open(filepath.Join(containerDir, resp.NextSessionID)); err != nil {
		t.Fatalf("open forked session store: %v", err)
	}
	if resp.InitialPrompt != "edited prompt" {
		t.Fatalf("initial prompt = %q, want %q", resp.InitialPrompt, "edited prompt")
	}
}

func TestServiceResolveTransitionForkRollbackPreservesExecutionTarget(t *testing.T) {
	workspaceRoot := t.TempDir()
	cfg, metadataStore, binding, sess := createAuthoritativeSessionLifecycleSession(t, workspaceRoot)
	if _, _, err := sess.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := sess.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if _, _, err := sess.AppendEvent("step-2", "message", llm.Message{Role: llm.RoleUser, Content: "u2"}); err != nil {
		t.Fatalf("append second user message: %v", err)
	}
	if _, _, err := sess.AppendEvent("step-2", "message", llm.Message{Role: llm.RoleAssistant, Content: "a2"}); err != nil {
		t.Fatalf("append second assistant message: %v", err)
	}

	worktreeRoot := filepath.Join(t.TempDir(), "feature-a")
	if err := os.MkdirAll(filepath.Join(worktreeRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir worktree pkg: %v", err)
	}
	if err := metadataStore.UpsertWorktreeRecord(context.Background(), metadata.WorktreeRecord{
		ID:            "wt-1",
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktreeRoot,
		DisplayName:   "feature-a",
		Availability:  "available",
		IsMain:        false,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := metadataStore.UpdateSessionExecutionTargetByID(context.Background(), sess.Meta().SessionID, binding.WorkspaceID, "wt-1", "pkg"); err != nil {
		t.Fatalf("UpdateSessionExecutionTargetByID: %v", err)
	}

	service := NewGlobalSessionLifecycleService(cfg.PersistenceRoot, nil, nil, metadataStore.AuthoritativeSessionStoreOptions()...)
	resp, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "req-1",
		SessionID:       sess.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			InitialPrompt:        "edited prompt",
			ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, sess, 2)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}

	target, err := metadataStore.ResolveSessionExecutionTarget(context.Background(), resp.NextSessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if target.WorktreeID != "wt-1" {
		t.Fatalf("fork target worktree id = %q, want wt-1", target.WorktreeID)
	}
	if target.WorktreeRoot != canonicalWorktreeRoot {
		t.Fatalf("fork target worktree root = %q, want %q", target.WorktreeRoot, canonicalWorktreeRoot)
	}
	if target.CwdRelpath != "pkg" {
		t.Fatalf("fork target cwd_relpath = %q, want pkg", target.CwdRelpath)
	}
	if target.EffectiveWorkdir != filepath.Join(canonicalWorktreeRoot, "pkg") {
		t.Fatalf("fork effective workdir = %q, want %q", target.EffectiveWorkdir, filepath.Join(canonicalWorktreeRoot, "pkg"))
	}
}

func TestServiceResolveTransitionForkRollbackActivatesChildInPreservedWorktree(t *testing.T) {
	workspaceRoot := t.TempDir()
	cfg, metadataStore, binding, sess := createAuthoritativeSessionLifecycleSession(t, workspaceRoot)
	if _, _, err := sess.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := sess.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	if _, _, err := sess.AppendEvent("step-2", "message", llm.Message{Role: llm.RoleUser, Content: "u2"}); err != nil {
		t.Fatalf("append second user message: %v", err)
	}
	if _, _, err := sess.AppendEvent("step-2", "message", llm.Message{Role: llm.RoleAssistant, Content: "a2"}); err != nil {
		t.Fatalf("append second assistant message: %v", err)
	}

	worktreeRoot := filepath.Join(t.TempDir(), "feature-a")
	if err := os.MkdirAll(filepath.Join(worktreeRoot, "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir worktree pkg: %v", err)
	}
	if err := metadataStore.UpsertWorktreeRecord(context.Background(), metadata.WorktreeRecord{
		ID:            "wt-1",
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktreeRoot,
		DisplayName:   "feature-a",
		Availability:  "available",
		IsMain:        false,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if err := metadataStore.UpdateSessionExecutionTargetByID(context.Background(), sess.Meta().SessionID, binding.WorkspaceID, "wt-1", "pkg"); err != nil {
		t.Fatalf("UpdateSessionExecutionTargetByID: %v", err)
	}

	lifecycle := NewGlobalSessionLifecycleService(cfg.PersistenceRoot, nil, nil, metadataStore.AuthoritativeSessionStoreOptions()...)
	resolved, err := lifecycle.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "req-1",
		SessionID:       sess.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			InitialPrompt:        "edited prompt",
			ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, sess, 2)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}

	runtimeService := sessionruntime.NewService(cfg.PersistenceRoot, metadataStore, nil, nil, nil, nil, nil, registry.NewSessionStoreRegistry(), metadataStore.AuthoritativeSessionStoreOptions()...)
	activateSettings := cfg.Settings
	activateSettings.Model = "gpt-5.4"
	activateSettings.OpenAIBaseURL = "http://127.0.0.1:1/v1"
	activateReq := sessionruntime.NewActivateRequest("activate-1", resolved.NextSessionID, activateSettings, nil, config.SourceReport{})
	if _, err := runtimeService.ActivateSessionRuntime(context.Background(), activateReq); err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	if _, err := runtimeService.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-1",
		SessionID:       resolved.NextSessionID,
	}); err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}

	childStore, err := session.OpenByID(cfg.PersistenceRoot, resolved.NextSessionID, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("OpenByID child: %v", err)
	}
	logBody, err := os.ReadFile(filepath.Join(childStore.Dir(), runlog.RunLogFileName))
	if err != nil {
		t.Fatalf("ReadFile steps.log: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	wantWorkdir := filepath.Join(canonicalWorktreeRoot, "pkg")
	if !strings.Contains(string(logBody), "app.interactive.start") {
		t.Fatalf("expected activation log entry, got %q", string(logBody))
	}
	if !strings.Contains(string(logBody), "workdir="+wantWorkdir) {
		t.Fatalf("expected activation workdir %q in log, got %q", wantWorkdir, string(logBody))
	}
}

func TestServiceResolveTransitionForkRollbackRejectsInvalidTargetToken(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "u1"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "a1"}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}

	service := newTestSessionLifecycleService(containerDir, nil)
	_, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "req-1",
		SessionID:       store.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			ForkRollbackTargetID: "not-valid",
		},
	})
	if !errors.Is(err, rollbacktarget.ErrInvalidRollbackTargetID) {
		t.Fatalf("expected invalid target token rejection, got %v", err)
	}
}

func TestServiceGetInitialInputRejectsSessionOutsideContainer(t *testing.T) {
	root := t.TempDir()
	containerA := filepath.Join(root, "projects", "project-a", "sessions")
	containerB := filepath.Join(root, "projects", "project-b", "sessions")
	if err := os.MkdirAll(containerA, 0o755); err != nil {
		t.Fatalf("mkdir container A: %v", err)
	}
	store, err := session.Create(containerB, "workspace-b", "/tmp/workspace-b")
	if err != nil {
		t.Fatalf("create foreign session store: %v", err)
	}
	if err := store.SetInputDraft("foreign draft"); err != nil {
		t.Fatalf("set foreign input draft: %v", err)
	}

	service := newTestSessionLifecycleService(containerA, nil)
	_, err = service.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{SessionID: store.Meta().SessionID})
	if err == nil {
		t.Fatal("expected foreign session lookup rejection")
	}
	if !errors.Is(err, sessioncontract.ErrSessionNotFound) && !errors.Is(err, session.ErrOutsideWorkspaceContainer) {
		t.Fatalf("expected scoped lookup rejection, got %v", err)
	}
}

func TestServicePersistInputDraftRejectsSessionOutsideContainer(t *testing.T) {
	root := t.TempDir()
	containerA := filepath.Join(root, "projects", "project-a", "sessions")
	containerB := filepath.Join(root, "projects", "project-b", "sessions")
	if err := os.MkdirAll(containerA, 0o755); err != nil {
		t.Fatalf("mkdir container A: %v", err)
	}
	store, err := session.Create(containerB, "workspace-b", "/tmp/workspace-b")
	if err != nil {
		t.Fatalf("create foreign session store: %v", err)
	}
	if err := store.SetName("foreign session"); err != nil {
		t.Fatalf("persist foreign session meta: %v", err)
	}

	service := newTestSessionLifecycleService(containerA, nil)
	_, err = service.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{
		ClientRequestID: "req-1",
		SessionID:       store.Meta().SessionID,
		Input:           "should fail",
	})
	if err == nil {
		t.Fatal("expected foreign session mutation rejection")
	}
	if !errors.Is(err, sessioncontract.ErrSessionNotFound) && !errors.Is(err, session.ErrOutsideWorkspaceContainer) {
		t.Fatalf("expected scoped lookup rejection, got %v", err)
	}
}

func TestServiceResolveTransitionLogoutUsesSessionIDWithoutStoreLookup(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "sk-before"},
		},
	}), nil, time.Now)
	service := newTestSessionLifecycleService(t.TempDir(), mgr)

	resp, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-42",
		Transition: serverapi.SessionTransition{
			Action: "logout",
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition logout: %v", err)
	}
	if !resp.ShouldContinue || !resp.RequiresReauth {
		t.Fatalf("unexpected logout response: %+v", resp)
	}
	if resp.NextSessionID != "session-42" {
		t.Fatalf("next session id = %q, want %q", resp.NextSessionID, "session-42")
	}
	state, err := mgr.Load(context.Background())
	if err != nil {
		t.Fatalf("load auth state: %v", err)
	}
	if state.Method.Type != auth.MethodAPIKey || state.Method.APIKey == nil || state.Method.APIKey.Key != "sk-before" {
		t.Fatalf("expected auth method to be preserved until reauth choice, got %+v", state.Method)
	}
}

func TestServiceResolveTransitionRequiresClientRequestID(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	_, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		Transition: serverapi.SessionTransition{Action: "continue"},
	})
	if !errors.Is(err, serverapi.ErrClientRequestIDRequired) {
		t.Fatalf("expected missing client_request_id error, got %v", err)
	}
}

func TestServiceResolveTransitionLogoutDedupesSuccessfulRetry(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "sk-before"},
		},
	}), nil, time.Now)
	service := newTestSessionLifecycleService(t.TempDir(), mgr)
	req := serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "dup-lease",
		SessionID:       "session-42",
		Transition:      serverapi.SessionTransition{Action: "logout"},
	}

	firstResp, err := service.ResolveTransition(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveTransition first: %v", err)
	}
	secondResp, err := service.ResolveTransition(context.Background(), req)
	if err != nil {
		t.Fatalf("ResolveTransition second replay: %v", err)
	}
	if !firstResp.ShouldContinue || !firstResp.RequiresReauth {
		t.Fatalf("unexpected first logout response: %+v", firstResp)
	}
	if secondResp != firstResp {
		t.Fatalf("expected duplicate transition replay response %+v, got %+v", firstResp, secondResp)
	}
}
