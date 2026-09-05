package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/session"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func sessionLifecycleStringPtr(value string) *string { return &value }

func sessionLifecycleSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return sessionID
}

func createAttachedAuthoritativeAppSession(t *testing.T, persistenceRoot, projectID, workspaceRoot string) *session.Store {
	t.Helper()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	if _, err := metadataStore.AttachWorkspaceToProject(context.Background(), projectID, workspaceRoot); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	store, err := session.Create(
		filepath.Join(persistenceRoot, "projects", projectID, "sessions"),
		filepath.Base(filepath.Clean(workspaceRoot)),
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		_ = metadataStore.Close()
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		_ = metadataStore.Close()
		t.Fatalf("EnsureDurable: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	return store
}

func TestMaybeHandlePickedSessionWorkspaceChangeCanonicalizesAliases(t *testing.T) {
	realRoot := t.TempDir()
	aliasRoot := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	promptCalls := 0
	runWorkspaceChangePromptFlow = func(string, string, string) (workspaceChangePromptResult, error) {
		promptCalls++
		return workspaceChangePromptResult{Rebind: true}, nil
	}

	action, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		&remoteAppServer{
			cfg:      config.App{WorkspaceRoot: aliasRoot, Settings: config.Settings{Theme: "dark"}},
			retarget: &sessionWorkspaceRetargetContext{workspaceRoot: aliasRoot, theme: "dark"},
		},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         realRoot,
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err != nil {
		t.Fatalf("maybeHandlePickedSessionWorkspaceChange: %v", err)
	}
	if action != sessionWorkspaceChangeProceed || promptCalls != 0 {
		t.Fatalf("action/prompt calls = %v/%d, want proceed/0", action, promptCalls)
	}
}

func TestMaybeHandlePickedSessionWorkspaceChangeUsesRemoteServerBindingRoot(t *testing.T) {
	originalPrompt := runWorkspaceChangePromptFlow
	defer func() { runWorkspaceChangePromptFlow = originalPrompt }()
	var promptedCurrentRoot string
	runWorkspaceChangePromptFlow = func(_ string, currentRoot string, _ string) (workspaceChangePromptResult, error) {
		promptedCurrentRoot = currentRoot
		return workspaceChangePromptResult{}, nil
	}

	action, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		&remoteAppServer{
			cfg:      config.App{WorkspaceRoot: "/source-client-workspace", Settings: config.Settings{Theme: "dark"}},
			retarget: &sessionWorkspaceRetargetContext{workspaceRoot: "/active-server-workspace", theme: "dark"},
		},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/target-server-workspace",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err != nil {
		t.Fatalf("maybeHandlePickedSessionWorkspaceChange: %v", err)
	}
	if action != sessionWorkspaceChangePickAgain || promptedCurrentRoot != "/active-server-workspace" {
		t.Fatalf("action/current root = %v/%q", action, promptedCurrentRoot)
	}
}

func TestMaybeHandlePickedSessionWorkspaceChangeRejectsMissingBindingContext(t *testing.T) {
	_, err := maybeHandlePickedSessionWorkspaceChange(
		context.Background(),
		narrowSessionLifecycleServer{},
		"session-1",
		clientui.SessionExecutionTarget{
			WorkspaceRoot:         "/target-server-workspace",
			WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
		},
	)
	if err == nil {
		t.Fatal("expected missing workspace retarget context error")
	}
}

func TestResolveSessionActionPreservesInitialPromptHistoryRecorded(t *testing.T) {
	client := &recordingSessionLifecycleClient{
		resolveTransition: func(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
			if !req.Transition.InitialPromptHistoryRecorded {
				t.Fatal("expected transition request to preserve initial prompt-history flag")
			}
			prompt := serverapi.SessionInitialPromptMetadata{
				Text:            req.Transition.InitialPrompt,
				HistoryRecorded: req.Transition.InitialPromptHistoryRecorded,
			}
			return serverapi.LaunchSessionDirective(
				serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
				serverapi.NewSessionLaunchPreparation(
					&prompt,
					serverapi.RestoreStoredDraftSessionDraftDisposition(),
					serverapi.SessionAuthPreparationKeepCurrent,
				),
			), nil
		},
	}

	resolved, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{lifecycle: client},
		nil,
		"session-1",
		UITransition{Action: UIActionNewSession, InitialPrompt: "expanded prompt", InitialPromptHistoryRecorded: true},
	)
	if err != nil {
		t.Fatalf("resolve session action: %v", err)
	}
	preparation, present := resolved.LaunchPreparation()
	if !present {
		t.Fatal("resolved transition omitted launch preparation")
	}
	prompt, present := preparation.InitialPrompt()
	if !present || !prompt.HistoryRecorded {
		t.Fatal("expected resolved transition to preserve initial prompt-history flag")
	}
}

func TestPersistSessionDraftIncludesOnlyComposerInput(t *testing.T) {
	var captured serverapi.SessionPersistInputDraftRequest
	client := &recordingSessionLifecycleClient{
		persistInputDraft: func(_ context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
			captured = req
			return serverapi.SessionPersistInputDraftResponse{}, nil
		},
	}
	model := newUIModelDefaults(nil)
	testSetMainInput(model, "visible draft")
	model.injectedQueue = []injectedRuntimeQueueItem{{
		LocalID:         "pending-injected",
		Text:            "  pending injected\n",
		State:           injectedRuntimeQueuePendingCreate,
		submissionOrder: inputSubmissionOrder{sequence: 1},
	}}
	model.queued = queuedInputsForTest("\tqueued later  ")

	if err := persistSessionDraftToServer(context.Background(), narrowSessionLifecycleServer{lifecycle: client}, " session-1 ", model); err != nil {
		t.Fatalf("persistSessionDraftToServer: %v", err)
	}
	if captured.Input != "visible draft" || captured.SessionID != "session-1" {
		t.Fatalf("captured draft request = %+v, want composer input only", captured)
	}
}

func TestRuntimeReleaseUsesFinalModelPolicyAndPreservesErrors(t *testing.T) {
	t.Run("normal release after UI loop", func(t *testing.T) {
		releaseErr := errors.New("release failed")
		uiLoopReturned := false
		plan := &runtimeLaunchPlan{close: func() error {
			if !uiLoopReturned {
				t.Fatal("runtime release ran before UI loop returned")
			}
			return releaseErr
		}}
		uiLoopReturned = true
		if err := closeRuntimePlanAfterUIExit(plan, &uiModel{}); !errors.Is(err, releaseErr) {
			t.Fatalf("close error = %v, want release failure", err)
		}
	})

	t.Run("forced exit detaches and joins draft error", func(t *testing.T) {
		persistErr := errors.New("persist draft failed")
		detachErr := errors.New("detach failed")
		plan := &runtimeLaunchPlan{
			close:       func() error { t.Fatal("normal close must not run"); return nil },
			detachClose: func() error { return detachErr },
		}
		err := releaseRuntimePlanAfterUIResult(plan, projectionFailureFinalModel(t), persistErr)
		if !errors.Is(err, persistErr) || !errors.Is(err, detachErr) {
			t.Fatalf("release error = %v, want persistence and detach failures", err)
		}
	})
}

func TestReopenRetargetedSessionPersistsDraftBeforeReleasingSourceRuntime(t *testing.T) {
	sourceRemoteOpen := true
	released := false
	var persistedDraft string
	sourceLifecycle := &recordingSessionLifecycleClient{
		persistInputDraft: func(_ context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
			if released {
				return serverapi.SessionPersistInputDraftResponse{}, errors.New("source runtime was released before draft persistence")
			}
			persistedDraft = req.Input
			return serverapi.SessionPersistInputDraftResponse{}, nil
		},
	}
	var server *reattachSessionLifecycleServer
	server = &reattachSessionLifecycleServer{
		lifecycle: sourceLifecycle,
		reattach: func(context.Context, string) error {
			if !released {
				return errors.New("source runtime owner is still attached")
			}
			sourceRemoteOpen = false
			return nil
		},
	}
	runtimePlan := &runtimeLaunchPlan{close: func() error {
		if !sourceRemoteOpen {
			return errors.New("source remote is closed")
		}
		released = true
		return nil
	}}
	sessionID := "11111111-1111-4111-8111-111111111111"
	model := newUIModelDefaults(nil)
	testSetMainInput(model, "draft after rebind")

	next, err := reopenRetargetedSession(
		context.Background(),
		server,
		runtimePlan,
		sessionID,
		model,
	)
	if err != nil {
		t.Fatalf("reopen retargeted Session: %v", err)
	}
	if !released || sourceRemoteOpen {
		t.Fatalf("handoff state released=%t source_remote_open=%t", released, sourceRemoteOpen)
	}
	if persistedDraft != "draft after rebind" {
		t.Fatalf("persisted draft = %q, want destination draft", persistedDraft)
	}
	if target := requireSessionOpenDestination(t, next); target != sessionID {
		t.Fatalf("reopen destination = %q, want %q", target, sessionID)
	}
}

func TestReopenRetargetedSessionPreservesDraftWhenDestinationReattachmentFails(t *testing.T) {
	reattachErr := errors.New("destination unavailable")
	var persistedDraft string
	server := &reattachSessionLifecycleServer{
		lifecycle: &recordingSessionLifecycleClient{
			persistInputDraft: func(_ context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
				persistedDraft = req.Input
				return serverapi.SessionPersistInputDraftResponse{}, nil
			},
		},
		reattach: func(context.Context, string) error {
			return reattachErr
		},
	}
	released := false
	runtimePlan := &runtimeLaunchPlan{close: func() error {
		released = true
		return nil
	}}
	model := newUIModelDefaults(nil)
	testSetMainInput(model, "draft survives failed reattachment")

	_, err := reopenRetargetedSession(
		context.Background(),
		server,
		runtimePlan,
		"11111111-1111-4111-8111-111111111111",
		model,
	)
	if !errors.Is(err, reattachErr) {
		t.Fatalf("reopen error = %v, want %v", err, reattachErr)
	}
	if !released {
		t.Fatal("source runtime was not released")
	}
	if persistedDraft != "draft survives failed reattachment" {
		t.Fatalf("persisted draft = %q, want preserved composer input", persistedDraft)
	}
}

type narrowSessionLifecycleServer struct {
	lifecycle      apicontract.SessionLifecycleService
	cfg            config.App
	reauthenticate func(context.Context, authInteractor) error
}

func (s narrowSessionLifecycleServer) SessionLifecycleClient() apicontract.SessionLifecycleService {
	return s.lifecycle
}

func (s narrowSessionLifecycleServer) Config() config.App { return s.cfg }

func (s narrowSessionLifecycleServer) Reauthenticate(ctx context.Context, interactor authInteractor, _ bool) error {
	if s.reauthenticate == nil {
		return nil
	}
	return s.reauthenticate(ctx, interactor)
}

type reattachSessionLifecycleServer struct {
	lifecycle           apicontract.SessionLifecycleService
	reattachedLifecycle apicontract.SessionLifecycleService
	reattach            func(context.Context, string) error
}

func (s *reattachSessionLifecycleServer) SessionLifecycleClient() apicontract.SessionLifecycleService {
	return s.lifecycle
}

func (s *reattachSessionLifecycleServer) ReattachSession(ctx context.Context, sessionID string) error {
	if err := s.reattach(ctx, sessionID); err != nil {
		return err
	}
	if s.reattachedLifecycle != nil {
		s.lifecycle = s.reattachedLifecycle
	}
	return nil
}

type recordingSessionLifecycleClient struct {
	getInitialInput          func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error)
	persistInputDraft        func(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error)
	retargetSessionWorkspace func(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error)
	resolveTransition        func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error)
}

func (c *recordingSessionLifecycleClient) Close() error { return nil }

func (c *recordingSessionLifecycleClient) GetInitialInput(ctx context.Context, req serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	if c.getInitialInput == nil {
		return serverapi.SessionInitialInputResponse{}, errors.New("unexpected GetInitialInput call")
	}
	return c.getInitialInput(ctx, req)
}

func (c *recordingSessionLifecycleClient) PersistInputDraft(ctx context.Context, req serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	if c.persistInputDraft == nil {
		return serverapi.SessionPersistInputDraftResponse{}, errors.New("unexpected PersistInputDraft call")
	}
	return c.persistInputDraft(ctx, req)
}

func (c *recordingSessionLifecycleClient) RetargetSessionWorkspace(ctx context.Context, req serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	if c.retargetSessionWorkspace == nil {
		return serverapi.SessionRetargetWorkspaceResponse{}, errors.New("unexpected RetargetSessionWorkspace call")
	}
	return c.retargetSessionWorkspace(ctx, req)
}

func (c *recordingSessionLifecycleClient) ResolveTransition(ctx context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	if c.resolveTransition == nil {
		return serverapi.SessionResolveTransitionResponse{}, errors.New("unexpected ResolveTransition call")
	}
	return c.resolveTransition(ctx, req)
}

func (*recordingSessionLifecycleClient) ArchiveSession(context.Context, *sessionlaunchpb.SessionArchiveRequest) (*sessionlaunchpb.SessionArchiveSuccess, error) {
	return nil, errors.New("unexpected ArchiveSession call")
}

func (*recordingSessionLifecycleClient) DeleteSession(context.Context, *sessionlaunchpb.SessionDeleteRequest) (*sessionlaunchpb.SessionDeleteSuccess, error) {
	return nil, errors.New("unexpected DeleteSession call")
}
