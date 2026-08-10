package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty/appfixture"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	creackpty "github.com/creack/pty"
)

type runtimeAttachmentTestServer struct {
	runtime           apicontract.SessionRuntimeService
	sessionTranscript apicontract.SessionTranscriptService
	sessionViews      apicontract.SessionViewService
	runtimeControl    apicontract.RuntimeControlService
}

type recordingSessionRuntimeClient struct {
	activate func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error)
	release  func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error)
}

func sessionRuntimeActivateResponse(sessionID string, generation uint64) serverapi.SessionRuntimeActivateResponse {
	return serverapi.SessionRuntimeActivateResponse{Attachment: serverapi.SessionRuntimeAttachment{
		SessionID:  sessionID,
		Generation: generation,
	}}
}

func (c *recordingSessionRuntimeClient) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	if c.activate != nil {
		return c.activate(ctx, req)
	}
	return sessionRuntimeActivateResponse(req.SessionID, 1), nil
}

func (c *recordingSessionRuntimeClient) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	if c.release != nil {
		return c.release(ctx, req)
	}
	return serverapi.SessionRuntimeReleaseResponse{}, nil
}

func (s runtimeAttachmentTestServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	return runtimeAttachmentClients{
		RuntimeControls:   s.runtimeControl,
		SessionRuntime:    s.runtime,
		SessionTranscript: s.sessionTranscript,
		SessionViews:      s.sessionViews,
	}
}

func TestRuntimeAttachmentRequiresTranscriptServiceAndReleasesRuntime(t *testing.T) {
	releaseCount := 0
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return sessionRuntimeActivateResponse(req.SessionID, 1), nil
			},
			release: func(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				releaseCount++
				if req.Attachment.SessionID != "session-1" || req.Attachment.Generation != 1 {
					t.Fatalf("unexpected release request: %+v", req)
				}
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("expected bounded release context")
				}
				if remaining := time.Until(deadline); remaining <= 0 || remaining > runtimeReleaseTimeout {
					t.Fatalf("release deadline remaining = %v, want within %v", remaining, runtimeReleaseTimeout)
				}
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
	}

	_, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1"}, io.Discard, "test")
	if err == nil {
		t.Fatal("expected missing transcript service error")
	}
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", releaseCount)
	}
}

func TestRuntimeAttachmentUsesTranscriptBellHook(t *testing.T) {
	releaseCount := 0
	sessionViews := &countingSessionViewClient{}
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return sessionRuntimeActivateResponse(req.SessionID, 1), nil
			},
			release: func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				releaseCount++
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
		sessionTranscript: &recordingTranscriptSubscriber{subs: []*scriptedTranscriptSubscription{{}}},
		sessionViews:      sessionViews,
		runtimeControl:    &reconnectRetryRuntimeControlClient{},
	}

	plan, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1"}, io.Discard, "test")
	if err != nil {
		t.Fatalf("prepareSharedRuntime: %v", err)
	}
	if plan.Wiring.eventDispatcher == nil || plan.Wiring.eventDispatcher.transcriptEvents == nil {
		t.Fatal("runtime wiring omitted the transcript event dispatcher")
	}
	promptHook, promptOK := plan.Wiring.promptAttention.(*bellHooks)
	turnHook, turnOK := plan.Wiring.turnQueueHook.(*bellHooks)
	if !promptOK || !turnOK || promptHook != turnHook {
		t.Fatal("runtime wiring did not make the bell hook authoritative for prompt activation")
	}
	plan.Close()
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", releaseCount)
	}
	if got := sessionViews.mainViewCount.Load(); got != 0 {
		t.Fatalf("startup main-view reads = %d, want 0 before feed hydration", got)
	}
}

func TestRuntimeAttachmentKeepsPromptActivationIndependentFromLifecycleHooks(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "lifecycle.jsonl")
	command, err := lifecycleHookProductRecorderCommand(
		recordPath,
		appfixture.LifecycleHookBehaviorSuccess,
		nil,
	)
	if err != nil {
		t.Fatalf("lifecycle recorder command: %v", err)
	}
	server := runtimeAttachmentTestServer{
		sessionTranscript: &recordingTranscriptSubscriber{subs: []*scriptedTranscriptSubscription{{}}},
		sessionViews:      &countingSessionViewClient{},
		runtimeControl:    &reconnectRetryRuntimeControlClient{},
	}
	sessionID := ongoingTestSessionID().String()
	wiring, stop, err := prepareSharedRuntimeWiring(t.Context(), server.RuntimeAttachmentClients(), sessionLaunchPlan{
		SessionID:                  sessionID,
		ClientLifecycleCommand:     command,
		ClientLifecycleOpeningKind: lifecyclecontract.OpeningKindResumed,
	}, nil)
	if err != nil {
		t.Fatalf("prepareSharedRuntimeWiring: %v", err)
	}
	t.Cleanup(stop)

	promptHook, promptOK := wiring.promptAttention.(*bellHooks)
	turnHooks, turnOK := wiring.turnQueueHook.(*turnQueueHooks)
	if !promptOK || !turnOK || turnHooks.notifications != promptHook {
		t.Fatal("lifecycle hooks changed native prompt-activation ownership")
	}
}

func TestRuntimeAttachmentReactivationUsesActivation(t *testing.T) {
	activateCalls := 0
	var released serverapi.SessionRuntimeAttachment
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				activateCalls++
				if req.SessionID != "session-recover" || req.ActiveSettings.Model != "gpt-test" {
					t.Fatalf("unexpected activation request: %+v", req)
				}
				return sessionRuntimeActivateResponse(req.SessionID, uint64(activateCalls)), nil
			},
			release: func(_ context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				released = req.Attachment
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
	}
	reactivator, lease, err := activateSharedRuntime(context.Background(), server.RuntimeAttachmentClients(), sessionLaunchPlan{
		SessionID:      "session-recover",
		ActiveSettings: config.Settings{Model: "gpt-test"},
	})
	if err != nil {
		t.Fatalf("activateSharedRuntime: %v", err)
	}
	if err := reactivator.Reactivate(context.Background()); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if activateCalls != 2 {
		t.Fatalf("activate calls = %d, want 2", activateCalls)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.SessionID != "session-recover" || released.Generation != 2 {
		t.Fatalf("released attachment = %+v, want reactivated generation 2", released)
	}
}

var errLifecycleActivationCaptured = errors.New("lifecycle activation captured")

func TestNewOrdinarySessionLifecycleCarriesUserActivationAuthority(t *testing.T) {
	server := newActivationLifecycleTestServer(t, 1)
	withSessionPickerResult(t, sessionPickerCreateResult{})

	err := runSessionLifecycleWithOptions(context.Background(), server, nil, sessionLifecycleOptions{})

	requireCapturedUserActivation(t, err, server, "created-session")
}

func TestExplicitSessionLifecycleCarriesUserActivationAuthority(t *testing.T) {
	server := newActivationLifecycleTestServer(t, 1)
	sessionID := sessionLifecycleSessionID(t, "explicit-session")
	intent := serverapi.OpenExistingSessionLaunchIntent(sessionID)

	err := runSessionLifecycleWithOptions(context.Background(), server, nil, sessionLifecycleOptions{Intent: &intent})

	requireCapturedUserActivation(t, err, server, sessionID.String())
}

func TestPickerExistingSessionLifecycleCarriesUserActivationAuthority(t *testing.T) {
	server := newActivationLifecycleTestServer(t, 1)
	sessionID := sessionLifecycleSessionID(t, "picker-session")
	withSessionPickerResult(t, newSessionPickerOpenResult(sessionID))

	err := runSessionLifecycleWithOptions(context.Background(), server, nil, sessionLifecycleOptions{})

	requireCapturedUserActivation(t, err, server, sessionID.String())
}

func TestInAppNavigationLifecycleCarriesUserActivationAuthority(t *testing.T) {
	server := newActivationLifecycleTestServer(t, 2)
	sourceID := sessionLifecycleSessionID(t, "navigation-source")
	intent := serverapi.OpenExistingSessionLaunchIntent(sourceID)
	withUIInput(t, "/new\r")

	err := runSessionLifecycleWithOptions(context.Background(), server, nil, sessionLifecycleOptions{Intent: &intent})

	if !errors.Is(err, errLifecycleActivationCaptured) {
		t.Fatalf("runSessionLifecycleWithOptions error = %v, want activation capture", err)
	}
	if got := len(server.activateRequests); got != 2 {
		t.Fatalf("activation requests = %d, want source open and in-app navigation", got)
	}
	for index, wantSessionID := range []string{sourceID.String(), "navigation-target"} {
		requireUserActivationRequest(t, server.activateRequests[index], wantSessionID)
	}
}

type activationLifecycleTestServer struct {
	cfg              config.App
	projectView      *activationLifecycleProjectView
	sessionView      stubSessionViewClient
	activateRequests []serverapi.SessionRuntimeActivateRequest
	stopAfter        int
}

func newActivationLifecycleTestServer(t *testing.T, stopAfter int) *activationLifecycleTestServer {
	t.Helper()
	workspaceRoot := t.TempDir()
	server := &activationLifecycleTestServer{
		cfg: config.App{
			WorkspaceRoot:   workspaceRoot,
			PersistenceRoot: t.TempDir(),
			Settings: config.Settings{
				Model: "gpt-test",
				Theme: "dark",
			},
		},
		stopAfter: stopAfter,
	}
	server.projectView = &activationLifecycleProjectView{server: server}
	server.sessionView = stubSessionViewClient{
		getSessionMainView: func(_ context.Context, req serverapi.SessionMainViewRequest) (serverapi.SessionMainViewResponse, error) {
			return serverapi.SessionMainViewResponse{MainView: clientui.RuntimeMainView{
				Session: clientui.RuntimeSessionView{
					SessionID: req.SessionID,
					ExecutionTarget: clientui.SessionExecutionTarget{
						WorkspaceID:           "workspace-test",
						WorkspaceRoot:         workspaceRoot,
						WorkspaceAvailability: clientui.ProjectAvailabilityAvailable,
						EffectiveWorkdir:      workspaceRoot,
					},
				},
			}}, nil
		},
	}
	return server
}

func (s *activationLifecycleTestServer) Close() error { return nil }

func (s *activationLifecycleTestServer) Config() config.App { return s.cfg }

func (s *activationLifecycleTestServer) PresentationTheme() string { return "dark" }

func (s *activationLifecycleTestServer) ProjectID() string { return "project-test" }

func (s *activationLifecycleTestServer) AuthStatusClient() apicontract.AuthStatusService {
	return nil
}

func (s *activationLifecycleTestServer) ProjectViewClient() apicontract.ProjectViewService {
	return s.projectView
}

func (s *activationLifecycleTestServer) ServerStatusClient() apicontract.ServerStatusService {
	return nil
}

func (s *activationLifecycleTestServer) SessionLaunchClient() apicontract.SessionLaunchService {
	return activationLifecycleSessionLaunchService{s: s}
}

func (s *activationLifecycleTestServer) SessionViewClient() apicontract.SessionViewService {
	return s.sessionView
}

func (s *activationLifecycleTestServer) SessionLifecycleClient() apicontract.SessionLifecycleService {
	return activationLifecycleSessionService{}
}

func (s *activationLifecycleTestServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	return runtimeAttachmentClients{
		RuntimeControls: &reconnectRetryRuntimeControlClient{},
		SessionRuntime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				s.activateRequests = append(s.activateRequests, req)
				if len(s.activateRequests) == s.stopAfter {
					return serverapi.SessionRuntimeActivateResponse{}, errLifecycleActivationCaptured
				}
				return sessionRuntimeActivateResponse(req.SessionID, uint64(len(s.activateRequests))), nil
			},
		},
		SessionTranscript: &recordingTranscriptSubscriber{
			subs: []*scriptedTranscriptSubscription{{}},
		},
		SessionViews: s.sessionView,
	}
}

func (s *activationLifecycleTestServer) Reauthenticate(context.Context, authInteractor, bool) error {
	return nil
}

func (s *activationLifecycleTestServer) BindProjectWorkspace(context.Context, string, string) (interactiveSessionServer, error) {
	return s, nil
}

func (s *activationLifecycleTestServer) EnsureAuthReady(context.Context, authInteractor, bool) error {
	return nil
}

func (s *activationLifecycleTestServer) workspaceRetargetContext() *sessionWorkspaceRetargetContext {
	return &sessionWorkspaceRetargetContext{
		workspaceRoot: s.cfg.WorkspaceRoot,
		theme:         s.PresentationTheme(),
	}
}

type activationLifecycleProjectView struct {
	apicontract.ProjectViewService
	server *activationLifecycleTestServer
}

func (v *activationLifecycleProjectView) PlanWorkspaceBinding(context.Context, serverapi.ProjectBindingPlanRequest) (serverapi.ProjectBindingPlanResponse, error) {
	return serverapi.ProjectBindingPlanResponse{
		Kind:          serverapi.ProjectBindingPlanKindBound,
		CanonicalRoot: v.server.cfg.WorkspaceRoot,
		Binding: &serverapi.ProjectBinding{
			ProjectID:     v.server.ProjectID(),
			WorkspaceID:   "workspace-test",
			CanonicalRoot: v.server.cfg.WorkspaceRoot,
		},
	}, nil
}

type activationLifecycleSessionLaunchService struct {
	s *activationLifecycleTestServer
}

func (s activationLifecycleSessionLaunchService) PlanSession(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	sessionID := "created-session"
	if existing, present := req.Intent.SessionID(); present {
		sessionID = existing.String()
	}
	return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
		SessionID:      sessionID,
		ActiveSettings: s.s.cfg.Settings,
	}}, nil
}

type activationLifecycleSessionService struct{}

func (activationLifecycleSessionService) GetInitialInput(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
	return serverapi.SessionInitialInputResponse{}, nil
}

func (activationLifecycleSessionService) PersistInputDraft(context.Context, serverapi.SessionPersistInputDraftRequest) (serverapi.SessionPersistInputDraftResponse, error) {
	return serverapi.SessionPersistInputDraftResponse{}, nil
}

func (activationLifecycleSessionService) RetargetSessionWorkspace(context.Context, serverapi.SessionRetargetWorkspaceRequest) (serverapi.SessionRetargetWorkspaceResponse, error) {
	return serverapi.SessionRetargetWorkspaceResponse{}, nil
}

func (activationLifecycleSessionService) ResolveTransition(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
	if req.Transition.Action != UIActionNewSession {
		return serverapi.SessionDirective{}, errors.New("expected in-app new-session navigation")
	}
	target := sessionLifecycleSessionIDFromString("navigation-target")
	return serverapi.LaunchSessionDirective(
		serverapi.OpenExistingSessionLaunchIntent(target),
		serverapi.NewSessionNavigationLaunchPreparation(
			nil,
			serverapi.RestoreStoredDraftSessionDraftDisposition(),
			serverapi.SessionAuthPreparationKeepCurrent,
			serverapi.SessionNavigationBinding{
				ProjectID:   "project-test",
				WorkspaceID: "workspace-test",
			},
		),
	), nil
}

func sessionLifecycleSessionIDFromString(raw string) runtimeids.SessionID {
	sessionID, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		panic(err)
	}
	return sessionID
}

func withSessionPickerResult(t *testing.T, result sessionPickerResult) {
	t.Helper()
	original := runSessionPickerFlow
	runSessionPickerFlow = func(context.Context, sessionPageLoader, string, sessionPickerHeaderInfo) (sessionPickerResult, error) {
		return result, nil
	}
	t.Cleanup(func() { runSessionPickerFlow = original })
}

func withUIInput(t *testing.T, input string) {
	t.Helper()
	master, terminal, err := creackpty.Open()
	if err != nil {
		t.Fatalf("open UI terminal: %v", err)
	}
	if err := creackpty.Setsize(master, &creackpty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatalf("set UI terminal size: %v", err)
	}
	originalInput := os.Stdin
	originalOutput := os.Stdout
	os.Stdin = terminal
	os.Stdout = terminal
	inputErr := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1024)
		inputSent := false
		for {
			if _, err := master.Read(buffer); err != nil {
				inputErr <- nil
				return
			}
			if inputSent {
				continue
			}
			if _, err := master.WriteString(input); err != nil {
				inputErr <- err
				return
			}
			inputSent = true
		}
	}()
	t.Cleanup(func() {
		os.Stdin = originalInput
		os.Stdout = originalOutput
		_ = terminal.Close()
		_ = master.Close()
		if err := <-inputErr; err != nil {
			t.Errorf("write UI input: %v", err)
		}
	})
}

func requireCapturedUserActivation(t *testing.T, err error, server *activationLifecycleTestServer, wantSessionID string) {
	t.Helper()
	if !errors.Is(err, errLifecycleActivationCaptured) {
		t.Fatalf("runSessionLifecycleWithOptions error = %v, want activation capture", err)
	}
	if len(server.activateRequests) == 0 {
		t.Fatal("lifecycle emitted no SessionRuntimeActivateRequest")
	}
	requireUserActivationRequest(t, server.activateRequests[len(server.activateRequests)-1], wantSessionID)
}

func requireUserActivationRequest(t *testing.T, request serverapi.SessionRuntimeActivateRequest, wantSessionID string) {
	t.Helper()
	if request.SessionID != wantSessionID {
		t.Fatalf("activation session = %q, want %q", request.SessionID, wantSessionID)
	}
	if got := appRuntimeActivationOperation(t, request); got != "user_activation" {
		t.Fatalf("activation operation = %q, want user_activation", got)
	}
}

func appRuntimeActivationOperation(t *testing.T, request serverapi.SessionRuntimeActivateRequest) string {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal activation request: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode activation request: %v", err)
	}
	operation, _ := payload["operation"].(string)
	return operation
}
