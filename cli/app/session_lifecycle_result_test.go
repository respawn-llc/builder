package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"core/server/metadata"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func requireSessionLifecycleResult(t *testing.T, result serverapi.SessionLifecycleResult) serverapi.SessionLifecycleResult {
	t.Helper()
	if result.IsZero() {
		t.Fatal("expected session lifecycle to continue")
	}
	return result
}

func requireSessionPickerDestination(t *testing.T, result serverapi.SessionLifecycleResult) {
	t.Helper()
	result = requireSessionLifecycleResult(t, result)
	if result.Kind() != serverapi.SessionLifecycleResultSelectSession {
		t.Fatalf("result kind = %q, want session picker", result.Kind())
	}
}

func requireSessionOpenDestination(t *testing.T, result serverapi.SessionLifecycleResult) string {
	t.Helper()
	result = requireSessionLifecycleResult(t, result)
	if result.Kind() != serverapi.SessionLifecycleResultLaunch {
		t.Fatalf("result kind = %q, want existing session launch", result.Kind())
	}
	intent, present := result.LaunchIntent()
	if !present || intent.Kind() != serverapi.SessionLaunchIntentOpenExisting {
		t.Fatalf("launch intent = %+v, want existing session", intent)
	}
	sessionID, present := intent.SessionID()
	if !present {
		t.Fatal("existing-session launch omitted session id")
	}
	return sessionID.String()
}

func requireSessionCreateDestination(t *testing.T, result serverapi.SessionLifecycleResult) *sessionParentReference {
	t.Helper()
	result = requireSessionLifecycleResult(t, result)
	if result.Kind() != serverapi.SessionLifecycleResultLaunch {
		t.Fatalf("result kind = %q, want new session launch", result.Kind())
	}
	intent, present := result.LaunchIntent()
	if !present || intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
		t.Fatalf("launch intent = %+v, want new session", intent)
	}
	parentID, present := intent.ParentID()
	if !present {
		return nil
	}
	return optionalSessionParentReference(parentID.String())
}

func sessionLaunchRequestFromLifecycleResult(t *testing.T, result serverapi.SessionLifecycleResult, overrides serverapi.RunPromptOverrides) sessionLaunchRequest {
	t.Helper()
	result = requireSessionLifecycleResult(t, result)
	intent, present := result.LaunchIntent()
	if !present {
		t.Fatal("lifecycle result omitted launch intent")
	}
	request, err := sessionLaunchRequestFromIntent(intent, overrides)
	if err != nil {
		t.Fatalf("build launch request: %v", err)
	}
	return request
}

func sessionLifecycleSessionIDForTest(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("parse session id %q: %v", raw, err)
	}
	return id
}

func TestSessionLifecycleResultExitIsClientLocalStop(t *testing.T) {
	result, err := resolveSessionAction(
		context.Background(),
		nil,
		nil,
		"current-session",
		UITransition{Exit: true},
	)
	if err != nil {
		t.Fatalf("resolveSessionAction: %v", err)
	}
	if result.Kind() != serverapi.SessionLifecycleResultStop {
		t.Fatalf("result kind = %q, want stop", result.Kind())
	}
}

func TestSessionLifecycleResultResumeReleasesThenUsesSamePickerWithoutPlan(t *testing.T) {
	releaseCalls := 0
	planCalls := 0
	binding := metadataBindingForLifecycleResult()
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   binding.workspaceRoot,
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		projectID: binding.projectID,
		projectViewClient: sessionLifecycleProjectViewClient(
			binding.binding,
			binding.workspaceRoot,
			[]clientui.SessionSummary{sessionLifecycleSessionSummary(t, "other", time.Now().UTC())},
		),
		sessionLaunch: stubSessionLaunchClient{planSession: func(context.Context, serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			planCalls++
			return serverapi.SessionPlanResponse{}, nil
		}},
	}
	planner := newSessionLaunchPlanner(server)
	planner.pickSession = func(sessionPageLoader, string, sessionPickerHeaderInfo) (sessionPickerResult, error) {
		if releaseCalls != 1 {
			t.Fatalf("picker opened before runtime release: releases=%d", releaseCalls)
		}
		return newSessionPickerCancelResult(), nil
	}

	result, err := resolveAndReleaseSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{lifecycle: &recordingSessionLifecycleClient{
			resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionLifecycleResult, error) {
				return serverapi.SelectSessionLifecycleResult(serverapi.SessionAuthPreparationKeepCurrent), nil
			},
		}},
		nil,
		"origin",
		UITransition{Action: UIActionResume},
		&runtimeLaunchPlan{close: func() error {
			releaseCalls++
			return nil
		}},
	)
	if err != nil {
		t.Fatalf("resolveAndReleaseSessionAction: %v", err)
	}
	if result.Kind() != serverapi.SessionLifecycleResultSelectSession {
		t.Fatalf("result kind = %q, want select session", result.Kind())
	}
	if _, err := planner.selectSession(context.Background()); err != nil {
		t.Fatalf("selectSession: %v", err)
	}
	if planCalls != 0 {
		t.Fatalf("session plan calls = %d, want zero", planCalls)
	}
	if releaseCalls != 1 {
		t.Fatalf("runtime release calls = %d, want one", releaseCalls)
	}
}

func TestSessionLifecycleResultReauthenticationCompletesBeforeDispatch(t *testing.T) {
	events := make([]string, 0, 3)
	target := sessionLifecycleSessionID(t, "target-session")
	want := serverapi.LaunchSessionLifecycleResult(
		serverapi.OpenExistingSessionLaunchIntent(target),
		serverapi.NewSessionLaunchPreparation(
			nil,
			serverapi.RestoreStoredDraftSessionInitialInputPolicy(),
			serverapi.SessionAuthPreparationReauthenticate,
		),
	)
	result, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{
			lifecycle: &recordingSessionLifecycleClient{
				resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionLifecycleResult, error) {
					events = append(events, "resolve")
					return want, nil
				},
			},
			reauthenticate: func(context.Context, authInteractor) error {
				events = append(events, "reauthenticate")
				return nil
			},
		},
		nil,
		"current-session",
		UITransition{Action: UIActionLogout},
	)
	if err != nil {
		t.Fatalf("resolveSessionAction: %v", err)
	}
	events = append(events, "dispatch")
	if !result.Equal(want) {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	if got := strings.Join(events, ","); got != "resolve,reauthenticate,dispatch" {
		t.Fatalf("event order = %q, want resolve,reauthenticate,dispatch", got)
	}
}

func TestSessionLifecycleResultAuthFailureDoesNotFabricateResult(t *testing.T) {
	authErr := errors.New("authentication canceled")
	target := sessionLifecycleSessionID(t, "target-session")
	result, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{
			lifecycle: &recordingSessionLifecycleClient{
				resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionLifecycleResult, error) {
					return serverapi.LaunchSessionLifecycleResult(
						serverapi.OpenExistingSessionLaunchIntent(target),
						serverapi.NewSessionLaunchPreparation(
							nil,
							serverapi.RestoreStoredDraftSessionInitialInputPolicy(),
							serverapi.SessionAuthPreparationReauthenticate,
						),
					), nil
				},
			},
			reauthenticate: func(context.Context, authInteractor) error {
				return authErr
			},
		},
		nil,
		"current-session",
		UITransition{Action: UIActionLogout},
	)
	if !errors.Is(err, authErr) {
		t.Fatalf("error = %v, want auth cancellation", err)
	}
	if !result.IsZero() {
		t.Fatalf("auth failure fabricated lifecycle result %+v", result)
	}
}

func TestSessionSelectionPickerCreateSendsCreateNewWithoutParent(t *testing.T) {
	originalPicker := runSessionPickerFlow
	defer func() { runSessionPickerFlow = originalPicker }()
	runSessionPickerFlow = func(sessionPageLoader, string, sessionPickerHeaderInfo) (sessionPickerResult, error) {
		return newSessionPickerCreateResult(), nil
	}

	stopErr := errors.New("stop after create-new plan")
	planCalls := 0
	binding := metadataBindingForLifecycleResult()
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   binding.workspaceRoot,
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		projectID: binding.projectID,
		projectViewClient: sessionLifecycleProjectViewClient(
			binding.binding,
			binding.workspaceRoot,
			nil,
		),
		sessionLaunch: stubSessionLaunchClient{planSession: func(_ context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
			planCalls++
			if req.Intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
				t.Fatalf("intent kind = %q, want create new", req.Intent.Kind())
			}
			if parentID, present := req.Intent.ParentID(); present {
				t.Fatalf("picker create carried parent %q", parentID.String())
			}
			return serverapi.SessionPlanResponse{}, stopErr
		}},
	}

	if err := runSessionLifecycle(context.Background(), server, nil, ""); !errors.Is(err, stopErr) {
		t.Fatalf("runSessionLifecycle error = %v, want %v", err, stopErr)
	}
	if planCalls != 1 {
		t.Fatalf("session plan calls = %d, want one", planCalls)
	}
}

func TestInitialInputPolicyComesFromLifecycleResultNotTransitionAction(t *testing.T) {
	target := sessionLifecycleSessionID(t, "target-session")
	want := serverapi.LaunchSessionLifecycleResult(
		serverapi.OpenExistingSessionLaunchIntent(target),
		serverapi.NewSessionLaunchPreparation(
			nil,
			serverapi.RestoreStoredDraftSessionInitialInputPolicy(),
			serverapi.SessionAuthPreparationKeepCurrent,
		),
	)
	result, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{lifecycle: &recordingSessionLifecycleClient{
			resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionLifecycleResult, error) {
				return want, nil
			},
		}},
		nil,
		"current-session",
		UITransition{
			Action:          UIActionOpenSession,
			TargetSessionID: target.String(),
			InitialInput:    "transition input must not choose policy",
		},
	)
	if err != nil {
		t.Fatalf("resolveSessionAction: %v", err)
	}
	preparation, ok := result.LaunchPreparation()
	if !ok {
		t.Fatal("launch result omitted preparation")
	}
	if preparation.InitialInputPolicy().Kind() != serverapi.SessionInitialInputPolicyRestoreStoredDraft {
		t.Fatalf("input policy = %q, want restore stored draft", preparation.InitialInputPolicy().Kind())
	}
}

type lifecycleResultBinding struct {
	projectID     string
	workspaceRoot string
	binding       metadata.Binding
}

func metadataBindingForLifecycleResult() lifecycleResultBinding {
	const projectID = "project-1"
	const workspaceID = "workspace-1"
	workspaceRoot := "/tmp/lifecycle-result-workspace"
	return lifecycleResultBinding{
		projectID:     projectID,
		workspaceRoot: workspaceRoot,
		binding: metadata.Binding{
			ProjectID:     projectID,
			WorkspaceID:   workspaceID,
			CanonicalRoot: workspaceRoot,
		},
	}
}

func requireAppLifecycleLaunch(t *testing.T, result serverapi.SessionLifecycleResult) (serverapi.SessionLaunchIntent, serverapi.SessionLaunchPreparation) {
	t.Helper()
	if result.Kind() != serverapi.SessionLifecycleResultLaunch {
		t.Fatalf("result kind = %q, want launch", result.Kind())
	}
	intent, ok := result.LaunchIntent()
	if !ok {
		t.Fatal("launch result omitted intent")
	}
	preparation, ok := result.LaunchPreparation()
	if !ok {
		t.Fatal("launch result omitted preparation")
	}
	return intent, preparation
}

func assertAppLifecycleSelect(t *testing.T, result serverapi.SessionLifecycleResult, wantAuth serverapi.SessionAuthPreparation) {
	t.Helper()
	if result.Kind() != serverapi.SessionLifecycleResultSelectSession {
		t.Fatalf("result kind = %q, want select session", result.Kind())
	}
	authPreparation, ok := result.AuthPreparation()
	if !ok || authPreparation != wantAuth {
		t.Fatalf("auth preparation = %q/%v, want %q", authPreparation, ok, wantAuth)
	}
}

func testCreateSessionLaunchRequest(mode launchMode) sessionLaunchRequest {
	return sessionLaunchRequest{
		Mode:   mode,
		Intent: serverapi.CreateNewSessionLaunchIntent(nil),
	}
}

func testOpenSessionLaunchRequest(t *testing.T, mode launchMode, rawSessionID string) sessionLaunchRequest {
	t.Helper()
	return sessionLaunchRequest{
		Mode:   mode,
		Intent: serverapi.OpenExistingSessionLaunchIntent(sessionLifecycleSessionID(t, rawSessionID)),
	}
}
