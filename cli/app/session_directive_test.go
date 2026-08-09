package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func requireSessionLifecycleResult(t *testing.T, result serverapi.SessionDirective) serverapi.SessionDirective {
	t.Helper()
	if err := result.Validate(); err != nil {
		t.Fatalf("expected valid session directive: %v", err)
	}
	return result
}

func requireSessionPickerDestination(t *testing.T, result serverapi.SessionDirective) {
	t.Helper()
	result = requireSessionLifecycleResult(t, result)
	if result.Kind() != serverapi.SessionDirectiveSelectSession {
		t.Fatalf("result kind = %q, want session picker", result.Kind())
	}
}

func requireSessionOpenDestination(t *testing.T, result serverapi.SessionDirective) string {
	t.Helper()
	result = requireSessionLifecycleResult(t, result)
	if result.Kind() != serverapi.SessionDirectiveLaunch {
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

func requireSessionCreateDestination(t *testing.T, result serverapi.SessionDirective) *string {
	t.Helper()
	result = requireSessionLifecycleResult(t, result)
	if result.Kind() != serverapi.SessionDirectiveLaunch {
		t.Fatalf("result kind = %q, want new session launch", result.Kind())
	}
	intent, present := result.LaunchIntent()
	if !present || intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
		t.Fatalf("launch intent = %+v, want new session", intent)
	}
	origin, present := intent.CreateOrigin()
	if !present || origin.Kind() == serverapi.SessionCreateOriginIndependent {
		return nil
	}
	parentID, present := origin.SessionID()
	if !present {
		t.Fatalf("creation origin = %+v, want source session", origin)
	}
	value := parentID.String()
	return &value
}

func sessionLaunchRequestFromLifecycleResult(t *testing.T, result serverapi.SessionDirective, overrides serverapi.RunPromptOverrides) sessionLaunchRequest {
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
	if result.Kind() != serverapi.SessionDirectiveStop {
		t.Fatalf("result kind = %q, want stop", result.Kind())
	}
}

func TestSessionLifecycleResultReauthenticationCompletesBeforeDispatch(t *testing.T) {
	events := make([]string, 0, 3)
	target := sessionLifecycleSessionID(t, "target-session")
	want := serverapi.LaunchSessionDirective(
		serverapi.OpenExistingSessionLaunchIntent(target),
		serverapi.NewSessionLaunchPreparation(
			nil,
			serverapi.RestoreStoredDraftSessionDraftDisposition(),
			serverapi.SessionAuthPreparationReauthenticate,
		),
	)
	result, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{
			lifecycle: &recordingSessionLifecycleClient{
				resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionDirective, error) {
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
	requireSessionDirectiveWireEqual(t, result, want)
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
				resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionDirective, error) {
					return serverapi.LaunchSessionDirective(
						serverapi.OpenExistingSessionLaunchIntent(target),
						serverapi.NewSessionLaunchPreparation(
							nil,
							serverapi.RestoreStoredDraftSessionDraftDisposition(),
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
	if err := result.Validate(); err == nil {
		t.Fatalf("auth failure fabricated lifecycle result %+v", result)
	}
}

func requireSessionDirectiveWireEqual(t *testing.T, got serverapi.SessionDirective, want serverapi.SessionDirective) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal directive: %v", err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal expected directive: %v", err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("directive JSON = %s, want %s", gotJSON, wantJSON)
	}
}

func TestInitialInputPolicyComesFromLifecycleResultNotTransitionAction(t *testing.T) {
	target := sessionLifecycleSessionID(t, "target-session")
	want := serverapi.LaunchSessionDirective(
		serverapi.OpenExistingSessionLaunchIntent(target),
		serverapi.NewSessionLaunchPreparation(
			nil,
			serverapi.RestoreStoredDraftSessionDraftDisposition(),
			serverapi.SessionAuthPreparationKeepCurrent,
		),
	)
	result, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{lifecycle: &recordingSessionLifecycleClient{
			resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionDirective, error) {
				return want, nil
			},
		}},
		nil,
		"current-session",
		UITransition{
			Action:          UIActionOpenSession,
			TargetSessionID: target.String(),
			InitialInput:    textutil.Value("transition input must not choose policy"),
		},
	)
	if err != nil {
		t.Fatalf("resolveSessionAction: %v", err)
	}
	preparation, ok := result.LaunchPreparation()
	if !ok {
		t.Fatal("launch result omitted preparation")
	}
	if preparation.DraftDisposition().Kind() != serverapi.SessionDraftDispositionRestoreStoredDraft {
		t.Fatalf("input policy = %q, want restore stored draft", preparation.DraftDisposition().Kind())
	}
}

func TestSessionTransitionInitialInputPreservesOpenSessionOmission(t *testing.T) {
	target := sessionLifecycleSessionID(t, "target-session")
	var recorded serverapi.SessionResolveTransitionRequest
	_, err := resolveSessionAction(
		context.Background(),
		narrowSessionLifecycleServer{lifecycle: &recordingSessionLifecycleClient{
			resolveTransition: func(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionDirective, error) {
				recorded = req
				return serverapi.StopSessionDirective(), nil
			},
		}},
		nil,
		"current-session",
		UITransition{
			Action:          UIActionOpenSession,
			TargetSessionID: target.String(),
		},
	)
	if err != nil {
		t.Fatalf("resolveSessionAction: %v", err)
	}
	if recorded.Transition.InitialInput != nil {
		t.Fatalf("open Session emitted initial input = %q, want absent", *recorded.Transition.InitialInput)
	}
}

func requireAppLifecycleLaunch(t *testing.T, result serverapi.SessionDirective) (serverapi.SessionLaunchIntent, serverapi.SessionLaunchPreparation) {
	t.Helper()
	if result.Kind() != serverapi.SessionDirectiveLaunch {
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
