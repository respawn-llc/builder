package sessionservice

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/server/auth"
	"core/server/metadata"
	"core/server/runlog"
	"core/server/session"
	"core/server/session/sessiontest"
	sessionruntime "core/server/sessionruntime"
	"core/shared/config"
	"core/shared/rollbacktarget"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
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

func TestServiceGetInitialInputOverrideReturnsOnlyExactTransitionInput(t *testing.T) {
	tests := []struct {
		name            string
		transitionInput string
	}{
		{
			name:            "byte-sensitive input",
			transitionInput: " \nExact café 👩🏽‍💻\n尾  ",
		},
		{
			name:            "intentional empty input",
			transitionInput: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, containerDir, store := createPersistedSession(t)
			if err := store.SetName("parent session"); err != nil {
				t.Fatalf("persist parent session: %v", err)
			}
			service := newTestSessionLifecycleService(containerDir, nil)
			_, err := service.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{

				SessionID: store.Meta().SessionID,
				Input:     "conflicting parent draft",
				RecoveryBuffers: []serverapi.SessionDraftRecoveryBuffer{
					{
						Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
						Text: "conflicting pending input",
					},
					{
						Kind: serverapi.SessionDraftRecoveryBufferQueuedInput,
						Text: "conflicting queued input",
					},
				},
			})
			if err != nil {
				t.Fatalf("persist parent draft: %v", err)
			}

			resp, err := service.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{
				SessionID:           store.Meta().SessionID,
				TransitionInput:     tt.transitionInput,
				OverrideStoredDraft: true,
			})
			if err != nil {
				t.Fatalf("GetInitialInput: %v", err)
			}
			if resp.Input != tt.transitionInput {
				t.Fatalf("input = %q, want exact transition input %q", resp.Input, tt.transitionInput)
			}
			if len(resp.RecoveryBuffers) != 0 {
				t.Fatalf("recovery buffers = %+v, want none from overridden parent draft", resp.RecoveryBuffers)
			}
		})
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

		SessionID: store.Meta().SessionID,
		Input:     "saved by service",
	}); err != nil {
		t.Fatalf("PersistInputDraft: %v", err)
	}

	reopened, err := session.Open(store.Dir(), sessionServiceTestPersistence.Options()...)
	if err != nil {
		t.Fatalf("reopen session store: %v", err)
	}
	if reopened.Meta().InputDraft != "saved by service" {
		t.Fatalf("input draft = %q, want %q", reopened.Meta().InputDraft, "saved by service")
	}
}

func TestServicePersistInputDraftRoundTripsStructuredRecoveryBuffers(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	service := newTestSessionLifecycleService(containerDir, nil)
	recovery := []serverapi.SessionDraftRecoveryBuffer{{
		Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
		Text: "queued steering before forced exit",
	}}
	if _, err := service.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{

		SessionID:       store.Meta().SessionID,
		Input:           "visible draft",
		RecoveryBuffers: recovery,
	}); err != nil {
		t.Fatalf("PersistInputDraft: %v", err)
	}

	resp, err := service.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("GetInitialInput: %v", err)
	}
	if resp.Input != "visible draft" || len(resp.RecoveryBuffers) != 1 {
		t.Fatalf("initial input response = %+v, want visible draft and one recovery buffer", resp)
	}
	got := resp.RecoveryBuffers[0]
	if got != recovery[0] {
		t.Fatalf("recovery buffer = %+v, want %+v", got, recovery[0])
	}
}

func TestServiceRestoresLegacyDraftRecoveryAndOrdinaryWriteDropsIdentity(t *testing.T) {
	cfg, metadataStore, _, sess := createAuthoritativeSessionLifecycleSession(t, t.TempDir())
	legacyBuffers := []map[string]any{
		{
			"kind":                        "pending_injected_input",
			"id":                          "legacy-local-1",
			"server_id":                   "legacy-server-1",
			"client_request_id":           "legacy-request-1",
			"text":                        "first recovered draft",
			"operation_client_request_id": "not-a-runtime-request-id",
			"operation_queue_item_id":     "not-a-queue-item-id",
			"operation_kind":              "queued_message",
		},
		{
			"kind":                        "queued_input",
			"id":                          "legacy-local-2",
			"server_id":                   "legacy-server-2",
			"client_request_id":           "legacy-request-2",
			"text":                        "second recovered draft",
			"operation_client_request_id": "also-invalid",
			"operation_queue_item_id":     "also-invalid",
			"operation_kind":              "submit",
		},
	}
	legacyMetadata, err := json.Marshal(map[string]any{
		"workspace_root":               cfg.WorkspaceRoot,
		"workspace_container":          filepath.Base(cfg.WorkspaceRoot),
		"input_draft_recovery_buffers": legacyBuffers,
	})
	if err != nil {
		t.Fatalf("marshal legacy metadata: %v", err)
	}
	if _, err := metadataStore.DB().ExecContext(
		t.Context(),
		"UPDATE sessions SET input_draft = ?, metadata_json = ? WHERE id = ?",
		"visible draft",
		string(legacyMetadata),
		sess.Meta().SessionID,
	); err != nil {
		t.Fatalf("seed legacy metadata: %v", err)
	}

	service := newGlobalSessionLifecycleServiceWithOptions(
		cfg.PersistenceRoot,
		nil,
		metadataStore.AuthoritativeSessionStoreOptions(),
	)
	resp, err := service.GetInitialInput(t.Context(), serverapi.SessionInitialInputRequest{
		SessionID: sess.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("GetInitialInput: %v", err)
	}
	wantRecovery := []serverapi.SessionDraftRecoveryBuffer{
		{Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput, Text: "first recovered draft"},
		{Kind: serverapi.SessionDraftRecoveryBufferQueuedInput, Text: "second recovered draft"},
	}
	if resp.Input != "visible draft" || !reflect.DeepEqual(resp.RecoveryBuffers, wantRecovery) {
		t.Fatalf("initial input response = %+v, want visible draft and ordered category/text %+v", resp, wantRecovery)
	}

	reopened, err := session.OpenByID(
		cfg.PersistenceRoot,
		sess.Meta().SessionID,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("open legacy session: %v", err)
	}
	if err := reopened.SetName("rewritten legacy recovery"); err != nil {
		t.Fatalf("rewrite metadata through ordinary mutation: %v", err)
	}
	var rewrittenMetadata string
	if err := metadataStore.DB().QueryRowContext(
		t.Context(),
		"SELECT metadata_json FROM sessions WHERE id = ?",
		sess.Meta().SessionID,
	).Scan(&rewrittenMetadata); err != nil {
		t.Fatalf("read rewritten metadata: %v", err)
	}
	var rewritten struct {
		RecoveryBuffers []map[string]any `json:"input_draft_recovery_buffers"`
	}
	if err := json.Unmarshal([]byte(rewrittenMetadata), &rewritten); err != nil {
		t.Fatalf("decode rewritten metadata: %v", err)
	}
	wantBuffers := []map[string]any{
		{"kind": "pending_injected_input", "text": "first recovered draft"},
		{"kind": "queued_input", "text": "second recovered draft"},
	}
	if !reflect.DeepEqual(rewritten.RecoveryBuffers, wantBuffers) {
		t.Fatalf("rewritten recovery buffers = %#v, want category/text only %#v", rewritten.RecoveryBuffers, wantBuffers)
	}
}

func TestServiceGetInitialInputLegacyDraftHasNoRecoveryBuffers(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	if err := store.SetInputDraft("legacy visible draft"); err != nil {
		t.Fatalf("set input draft: %v", err)
	}
	service := newTestSessionLifecycleService(containerDir, nil)
	resp, err := service.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("GetInitialInput: %v", err)
	}
	if resp.Input != "legacy visible draft" || len(resp.RecoveryBuffers) != 0 {
		t.Fatalf("initial input response = %+v, want legacy draft only", resp)
	}
}

func TestServiceRetargetSessionWorkspaceDelegatesAndMapsBinding(t *testing.T) {
	projectID := "project-target"
	retargeter := &sessionLifecycleRetargeterStub{result: metadata.SessionWorkspaceRetargetResult{
		Binding: metadata.Binding{
			ProjectID:       projectID,
			ProjectKey:      "TAR",
			ProjectName:     "Target",
			WorkspaceID:     "workspace-target",
			CanonicalRoot:   t.TempDir(),
			WorkspaceName:   "target",
			WorkspaceStatus: "available",
		},
		WorkspaceBindingCreated: true,
	}}
	service := NewGlobalSessionLifecycleService(t.TempDir(), nil, nil).WithWorkspaceRetargeter(retargeter)
	resp, err := service.RetargetSessionWorkspace(context.Background(), serverapi.SessionRetargetWorkspaceRequest{

		SessionID:     "session-1",
		WorkspaceRoot: retargeter.result.Binding.CanonicalRoot,
		ProjectID:     &projectID,
	})
	if err != nil {
		t.Fatalf("RetargetSessionWorkspace: %v", err)
	}
	if retargeter.req.ProjectID == nil || *retargeter.req.ProjectID != projectID {
		t.Fatalf("retarget request = %+v, want target project %q", retargeter.req, projectID)
	}
	if resp.Binding.ProjectID != projectID || resp.Binding.ProjectKey != "TAR" {
		t.Fatalf("binding = %+v, want mapped retarget result", resp.Binding)
	}
	if !resp.WorkspaceBindingCreated {
		t.Fatal("WorkspaceBindingCreated = false, want true")
	}
}

func TestServiceRetargetSessionWorkspaceRequiresRetargeter(t *testing.T) {
	service := NewSessionLifecycleService(t.TempDir(), nil, nil)
	_, err := service.RetargetSessionWorkspace(context.Background(), serverapi.SessionRetargetWorkspaceRequest{

		SessionID:     "session-1",
		WorkspaceRoot: t.TempDir(),
	})
	if !errors.Is(err, errSessionWorkspaceRetargeterRequired) {
		t.Fatalf("RetargetSessionWorkspace error = %v, want missing retargeter", err)
	}
}

func TestServicePersistInputDraftRejectsPathLikeSessionID(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	_, err := service.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{

		SessionID: "sessions/workspace-x/session-1",
		Input:     "draft",
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestServiceResolveTransitionRejectsPathLikeSessionID(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	_, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		SessionID: "../session-1",
		Transition: serverapi.SessionTransition{
			Action: "continue",
		},
	})
	if !errors.Is(err, serverapi.ErrSessionIDNotSingle) {
		t.Fatalf("expected path-like session id rejection, got %v", err)
	}
}

func TestServiceResolveTransitionOpenSessionRequiresTarget(t *testing.T) {
	service := newTestSessionLifecycleService(t.TempDir(), nil)
	response, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		Transition: serverapi.SessionTransition{
			Action: serverapi.SessionTransitionActionOpenSession,
		},
	})
	if err == nil {
		t.Fatalf("open-session transition without target resolved as %+v", response)
	}
}

func TestServiceResolveTransitionOpenSessionAuthorizesProvenanceTargetAndReturnsBinding(t *testing.T) {
	_, containerDir, parent := createPersistedSession(t)
	child, err := session.NewLazy(
		containerDir,
		"workspace-x",
		"/tmp/work",
		sessioncontract.SessionCategoryMain,
		sessionServiceTestPersistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := session.InitializeCreationContext(child, parent, session.SessionCreationSourcePreviousSession, session.ChildContextOptions{}); err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}
	if err := child.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable child: %v", err)
	}
	resolver := &sessionNavigationTargetResolverStub{target: serverapi.SessionNavigationBinding{
		ProjectID:   "project-target",
		WorkspaceID: "workspace-target",
	}}
	service := newTestSessionLifecycleService(containerDir, nil).WithNavigationTargetResolver(resolver)

	response, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		SessionID: child.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:          serverapi.SessionTransitionActionOpenSession,
			TargetSessionID: parent.Meta().SessionID,
			InitialInput:    textutil.Value("draft reply"),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	intent, preparation := requireSessionLifecycleLaunch(t, response)
	targetID, present := intent.SessionID()
	if !present || targetID.String() != parent.Meta().SessionID {
		t.Fatalf("navigation target = %q/%t, want %q", targetID.String(), present, parent.Meta().SessionID)
	}
	binding, present := preparation.NavigationBinding()
	if !present || binding.ProjectID != "project-target" || binding.WorkspaceID != "workspace-target" {
		t.Fatalf("navigation binding = %+v/%t", binding, present)
	}
	if len(resolver.calls) != 1 || resolver.calls[0] != parent.Meta().SessionID {
		t.Fatalf("target resolver calls = %#v, want parent once", resolver.calls)
	}
}

func TestServiceResolveTransitionOpenSessionRejectsNonProvenanceTargetBeforeResolution(t *testing.T) {
	_, containerDir, parent := createPersistedSession(t)
	child, err := session.NewLazy(
		containerDir,
		"workspace-x",
		"/tmp/work",
		sessioncontract.SessionCategoryMain,
		sessionServiceTestPersistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	if err := session.InitializeCreationContext(child, parent, session.SessionCreationSourcePreviousSession, session.ChildContextOptions{}); err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}
	if err := child.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable child: %v", err)
	}
	resolver := &sessionNavigationTargetResolverStub{}
	service := newTestSessionLifecycleService(containerDir, nil).WithNavigationTargetResolver(resolver)

	_, err = service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		SessionID: child.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:          serverapi.SessionTransitionActionOpenSession,
			TargetSessionID: "arbitrary-session",
		},
	})
	if err == nil {
		t.Fatal("non-provenance navigation target unexpectedly succeeded")
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("target resolver calls = %#v, want none", resolver.calls)
	}
}

func TestServiceResolveTransitionForkRollbackCreatesFork(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	appendSessionMessage(t, store, "step-1", session.MessageRoleUser, "u1")
	appendSessionMessage(t, store, "step-1", session.MessageRoleAssistant, "a1")
	appendSessionMessage(t, store, "step-2", session.MessageRoleUser, "u2")
	appendSessionMessage(t, store, "step-2", session.MessageRoleAssistant, "a2")

	service := newTestSessionLifecycleService(containerDir, nil)
	resp, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		SessionID: store.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			InitialPrompt:        "edited prompt",
			ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, store, 2)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	intent, preparation := requireSessionLifecycleLaunch(t, resp)
	forkID, ok := intent.SessionID()
	if !ok || forkID.String() == store.Meta().SessionID {
		t.Fatalf("unexpected fork session id %q/%v", forkID.String(), ok)
	}
	prompt, ok := preparation.InitialPrompt()
	if !ok || prompt.Text != "edited prompt" {
		t.Fatalf("initial prompt = %+v/%v, want edited prompt", prompt, ok)
	}
	if _, err := session.Open(filepath.Join(containerDir, forkID.String()), sessionServiceTestPersistence.Options()...); err != nil {
		t.Fatalf("open forked session store: %v", err)
	}
}

func TestServiceResolveTransitionForkRollbackUsesTargetToken(t *testing.T) {
	_, containerDir, store := createPersistedSession(t)
	appendSessionMessage(t, store, "step-1", session.MessageRoleUser, "u1")
	appendSessionMessage(t, store, "step-1", session.MessageRoleAssistant, "a1")
	appendSessionMessage(t, store, "step-2", session.MessageRoleUser, "u2")
	appendSessionMessage(t, store, "step-2", session.MessageRoleAssistant, "a2")

	service := newTestSessionLifecycleService(containerDir, nil)
	resp, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		SessionID: store.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			InitialPrompt:        "edited prompt",
			ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, store, 2)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	intent, preparation := requireSessionLifecycleLaunch(t, resp)
	forkID, ok := intent.SessionID()
	if !ok {
		t.Fatal("rollback result omitted fork session ID")
	}
	if _, err := session.Open(filepath.Join(containerDir, forkID.String()), sessionServiceTestPersistence.Options()...); err != nil {
		t.Fatalf("open forked session store: %v", err)
	}
	prompt, ok := preparation.InitialPrompt()
	if !ok || prompt.Text != "edited prompt" {
		t.Fatalf("initial prompt = %+v/%v, want edited prompt", prompt, ok)
	}
}

func TestServiceResolveTransitionForkRollbackPreservesExecutionTarget(t *testing.T) {
	workspaceRoot := t.TempDir()
	cfg, metadataStore, binding, sess := createAuthoritativeSessionLifecycleSession(t, workspaceRoot)
	appendSessionMessage(t, sess, "step-1", session.MessageRoleUser, "u1")
	appendSessionMessage(t, sess, "step-1", session.MessageRoleAssistant, "a1")
	appendSessionMessage(t, sess, "step-2", session.MessageRoleUser, "u2")
	appendSessionMessage(t, sess, "step-2", session.MessageRoleAssistant, "a2")

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
	if err := metadataStore.UpdateSessionExecutionTarget(context.Background(), metadata.SessionExecutionTargetUpdate{SessionID: sess.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID}, Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: "wt-1"}, CwdRelpath: "pkg"}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget: %v", err)
	}

	service := newGlobalSessionLifecycleServiceWithOptions(cfg.PersistenceRoot, nil, metadataStore.AuthoritativeSessionStoreOptions())
	resp, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		SessionID: sess.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			InitialPrompt:        "edited prompt",
			ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, sess, 2)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}

	intent, _ := requireSessionLifecycleLaunch(t, resp)
	forkID, ok := intent.SessionID()
	if !ok {
		t.Fatal("rollback result omitted fork session ID")
	}
	target, err := metadataStore.ResolveSessionExecutionTarget(context.Background(), forkID.String())
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if target.Worktree == nil || target.Worktree.ID != "wt-1" {
		t.Fatalf("fork target worktree = %+v, want wt-1", target.Worktree)
	}
	if target.Worktree == nil || target.Worktree.Root != canonicalWorktreeRoot {
		t.Fatalf("fork target worktree root = %+v, want %q", target.Worktree, canonicalWorktreeRoot)
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
	appendSessionMessage(t, sess, "step-1", session.MessageRoleUser, "u1")
	appendSessionMessage(t, sess, "step-1", session.MessageRoleAssistant, "a1")
	appendSessionMessage(t, sess, "step-2", session.MessageRoleUser, "u2")
	appendSessionMessage(t, sess, "step-2", session.MessageRoleAssistant, "a2")

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
	if err := metadataStore.UpdateSessionExecutionTarget(context.Background(), metadata.SessionExecutionTargetUpdate{SessionID: sess.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: binding.WorkspaceID}, Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: "wt-1"}, CwdRelpath: "pkg"}); err != nil {
		t.Fatalf("UpdateSessionExecutionTarget: %v", err)
	}

	lifecycle := newGlobalSessionLifecycleServiceWithOptions(cfg.PersistenceRoot, nil, metadataStore.AuthoritativeSessionStoreOptions())
	resolved, err := lifecycle.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		SessionID: sess.Meta().SessionID,
		Transition: serverapi.SessionTransition{
			Action:               "fork_rollback",
			InitialPrompt:        "edited prompt",
			ForkRollbackTargetID: rollbacktarget.EncodeUserMessageSeq(userMessageSeqAt(t, sess, 2)),
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	intent, _ := requireSessionLifecycleLaunch(t, resolved)
	forkID, ok := intent.SessionID()
	if !ok {
		t.Fatal("rollback result omitted fork session ID")
	}

	runtimeAuthority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := runtimeAuthority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	runtimeService := sessionruntime.NewAPI(metadataStore, nil, runtimeAuthority, sessionruntime.APIOptions{})
	activateSettings := cfg.Settings
	activateSettings.Model = "gpt-5.4"
	activateSettings.OpenAIBaseURL = "http://127.0.0.1:1/v1"
	activateSettings.Shell.PostprocessingMode = config.ShellPostprocessingModeBuiltin
	activation, err := runtimeService.ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{

		SessionID:      forkID.String(),
		OwnerID:        "test-owner",
		ActiveSettings: activateSettings,
		Source:         config.SourceReport{},
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	if _, err := runtimeService.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{

		Attachment: activation.Attachment,
		OwnerID:    "test-owner",
	}); err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}

	childStore, err := session.OpenByID(cfg.PersistenceRoot, forkID.String(), metadataStore.AuthoritativeSessionStoreOptions()...)
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
	appendSessionMessage(t, store, "step-1", session.MessageRoleUser, "u1")
	appendSessionMessage(t, store, "step-1", session.MessageRoleAssistant, "a1")

	service := newTestSessionLifecycleService(containerDir, nil)
	_, err := service.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{

		SessionID: store.Meta().SessionID,
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
	store, err := session.Create(
		containerB,
		"workspace-b",
		"/tmp/workspace-b",
		sessioncontract.SessionCategoryMain,
		sessionServiceTestPersistence.Options()...,
	)
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
	store, err := session.Create(
		containerB,
		"workspace-b",
		"/tmp/workspace-b",
		sessioncontract.SessionCategoryMain,
		sessionServiceTestPersistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create foreign session store: %v", err)
	}
	if err := store.SetName("foreign session"); err != nil {
		t.Fatalf("persist foreign session meta: %v", err)
	}

	service := newTestSessionLifecycleService(containerA, nil)
	_, err = service.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{

		SessionID: store.Meta().SessionID,
		Input:     "should fail",
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

		SessionID: "session-42",
		Transition: serverapi.SessionTransition{
			Action: "logout",
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition logout: %v", err)
	}
	intent, preparation := requireSessionLifecycleLaunch(t, resp)
	sessionID, ok := intent.SessionID()
	if !ok || sessionID.String() != "session-42" {
		t.Fatalf("next session id = %q/%v, want session-42", sessionID.String(), ok)
	}
	if preparation.AuthPreparation() != serverapi.SessionAuthPreparationReauthenticate {
		t.Fatalf("auth preparation = %q, want reauthenticate", preparation.AuthPreparation())
	}
	state, err := mgr.Load(context.Background())
	if err != nil {
		t.Fatalf("load auth state: %v", err)
	}
	if state.Method.Type != auth.MethodAPIKey || state.Method.APIKey == nil || state.Method.APIKey.Key != "sk-before" {
		t.Fatalf("expected auth method to be preserved until reauth choice, got %+v", state.Method)
	}
}
