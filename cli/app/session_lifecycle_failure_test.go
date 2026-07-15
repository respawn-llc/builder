package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"core/cli/app/commands"
	"core/shared/config"
	"core/shared/serverapi"
)

type clientPromptRootsFailureServer struct {
	*testEmbeddedServer
	err error
}

func (s clientPromptRootsFailureServer) ClientPromptRoots() (commands.ClientPromptRoots, error) {
	return commands.ClientPromptRoots{}, s.err
}

func TestResolveAndReleaseSessionHandoffTransitionFailureLeavesChildReopenableWithoutDestination(t *testing.T) {
	child, persistence := createAuthoritativeTestSession(t, t.TempDir(), "ws", t.TempDir())
	resolveErr := errors.New("transition resolution failed")
	releaseCalls := 0
	server := narrowSessionLifecycleServer{
		lifecycle: &recordingSessionLifecycleClient{
			resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
				return serverapi.SessionResolveTransitionResponse{}, resolveErr
			},
		},
	}

	handoff, err := resolveAndReleaseSessionHandoff(
		context.Background(),
		server,
		nil,
		child.Meta().SessionID,
		UITransition{Action: UIActionOpenSession, TargetSessionID: "parent-session"},
		&runtimeLaunchPlan{close: func() error {
			releaseCalls++
			return nil
		}},
	)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("handoff error = %v, want transition failure", err)
	}
	if handoff != nil {
		t.Fatalf("transition failure returned destination handoff %+v", handoff)
	}
	if releaseCalls != 1 {
		t.Fatalf("origin release calls = %d, want 1 after transition failure", releaseCalls)
	}

	reopened, err := persistence.Open(child.Dir())
	if err != nil {
		t.Fatalf("reopen child after transition failure: %v", err)
	}
	if reopened.Meta().SessionID != child.Meta().SessionID {
		t.Fatalf("reopened child id = %q, want %q", reopened.Meta().SessionID, child.Meta().SessionID)
	}
}

func TestResolveAndReleaseSessionHandoffRejectsOpenSessionWithoutDestination(t *testing.T) {
	child, persistence := createAuthoritativeTestSession(t, t.TempDir(), "ws", t.TempDir())
	releaseCalls := 0
	server := narrowSessionLifecycleServer{
		lifecycle: &recordingSessionLifecycleClient{
			resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
				return serverapi.SessionResolveTransitionResponse{ShouldContinue: true}, nil
			},
		},
	}

	handoff, err := resolveAndReleaseSessionHandoff(
		context.Background(),
		server,
		nil,
		child.Meta().SessionID,
		UITransition{Action: UIActionOpenSession},
		&runtimeLaunchPlan{close: func() error {
			releaseCalls++
			return nil
		}},
	)
	if err == nil {
		t.Fatal("open-session response without destination was accepted")
	}
	if handoff != nil {
		t.Fatalf("open-session response without destination returned handoff %+v", handoff)
	}
	if releaseCalls != 1 {
		t.Fatalf("origin cleanup calls = %d, want 1", releaseCalls)
	}

	reopened, err := persistence.Open(child.Dir())
	if err != nil {
		t.Fatalf("reopen child after invalid open-session response: %v", err)
	}
	if reopened.Meta().SessionID != child.Meta().SessionID {
		t.Fatalf("reopened child id = %q, want %q", reopened.Meta().SessionID, child.Meta().SessionID)
	}
}

func TestResolveAndReleaseSessionHandoffReturnsReleaseFailureBeforeDestinationCanPlan(t *testing.T) {
	child, persistence := createAuthoritativeTestSession(t, t.TempDir(), "ws", t.TempDir())
	releaseErr := errors.New("release failed")
	events := make([]string, 0, 2)
	server := narrowSessionLifecycleServer{
		lifecycle: &recordingSessionLifecycleClient{
			resolveTransition: func(context.Context, serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
				events = append(events, "resolve")
				return serverapi.SessionResolveTransitionResponse{ShouldContinue: true, NextSessionID: "destination"}, nil
			},
		},
	}
	plan := &runtimeLaunchPlan{close: func() error {
		events = append(events, "release")
		return releaseErr
	}}

	resolved, err := resolveAndReleaseSessionHandoff(context.Background(), server, nil, child.Meta().SessionID, UITransition{Action: UIActionResume}, plan)
	if !errors.Is(err, releaseErr) {
		t.Fatalf("error = %v, want release failure", err)
	}
	if resolved != nil {
		t.Fatalf("release failure returned destination %+v", resolved)
	}
	if got := strings.Join(events, ","); got != "resolve,release" {
		t.Fatalf("event order = %q, want resolve,release", got)
	}
	reopened, err := persistence.Open(child.Dir())
	if err != nil {
		t.Fatalf("reopen child after release failure: %v", err)
	}
	if reopened.Meta().SessionID != child.Meta().SessionID {
		t.Fatalf("reopened child id = %q, want %q", reopened.Meta().SessionID, child.Meta().SessionID)
	}
}

func TestDestinationPlanningFailureLeavesChildReopenableWithoutPreparation(t *testing.T) {
	child, persistence := createAuthoritativeTestSession(t, t.TempDir(), "ws", t.TempDir())
	planErr := errors.New("destination planning failed")
	prepareCalls := 0
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   t.TempDir(),
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		sessionLifecycle: &recordingSessionLifecycleClient{
			resolveTransition: func(_ context.Context, req serverapi.SessionResolveTransitionRequest) (serverapi.SessionResolveTransitionResponse, error) {
				return serverapi.SessionResolveTransitionResponse{
					NextSessionID:  req.Transition.TargetSessionID,
					InitialInput:   req.Transition.InitialInput,
					ShouldContinue: true,
				}, nil
			},
		},
		sessionLaunch: stubSessionLaunchClient{
			planSession: func(context.Context, serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
				return serverapi.SessionPlanResponse{}, planErr
			},
		},
		prepareRuntime: func(context.Context, sessionLaunchPlan, io.Writer, string) (*runtimeLaunchPlan, error) {
			prepareCalls++
			return nil, errors.New("destination preparation must not start")
		},
	}

	handoff, err := resolveAndReleaseSessionHandoff(
		context.Background(),
		server,
		nil,
		child.Meta().SessionID,
		UITransition{Action: UIActionOpenSession, TargetSessionID: "parent-session", InitialInput: "child final"},
		&runtimeLaunchPlan{close: func() error { return nil }},
	)
	if err != nil {
		t.Fatalf("resolve destination handoff: %v", err)
	}
	parentSessionID := requireSessionOpenDestination(t, handoff)
	planner := newSessionLaunchPlanner(server)
	_, err = planner.PlanSession(context.Background(), sessionLaunchRequest{
		Mode:        launchModeInteractive,
		Destination: sessionOpenDestinationForTest(t, parentSessionID),
	})
	if !errors.Is(err, planErr) {
		t.Fatalf("destination planning error = %v, want %v", err, planErr)
	}
	if prepareCalls != 0 {
		t.Fatalf("destination preparation calls = %d, want none after planning failure", prepareCalls)
	}

	reopened, err := persistence.Open(child.Dir())
	if err != nil {
		t.Fatalf("reopen child after destination planning failure: %v", err)
	}
	if reopened.Meta().SessionID != child.Meta().SessionID {
		t.Fatalf("reopened child id = %q, want %q", reopened.Meta().SessionID, child.Meta().SessionID)
	}
}

func TestDestinationPreparationFailureLeavesChildReopenableWithoutComposition(t *testing.T) {
	child, persistence := createAuthoritativeTestSession(t, t.TempDir(), "ws", t.TempDir())
	prepareErr := errors.New("destination runtime preparation failed")
	initialInputCalls := 0
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   t.TempDir(),
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		sessionLifecycle: &recordingSessionLifecycleClient{
			getInitialInput: func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
				initialInputCalls++
				return serverapi.SessionInitialInputResponse{}, errors.New("initial input must not load before runtime preparation")
			},
		},
		prepareRuntime: func(context.Context, sessionLaunchPlan, io.Writer, string) (*runtimeLaunchPlan, error) {
			return nil, prepareErr
		},
	}
	planner := newSessionLaunchPlanner(server)

	runtimePlan, request, err := prepareSessionUIRun(
		context.Background(),
		server,
		planner,
		sessionLaunchPlan{
			SessionID:      "parent-session",
			WorkspaceRoot:  server.cfg.WorkspaceRoot,
			ActiveSettings: config.Settings{Model: "gpt-5", Theme: "dark"},
		},
		resolvedSessionHandoff{
			Destination: sessionOpenDestinationForTest(t, "parent-session"),
			InitialInput: sessionInitialInputDirective{
				TransitionInput: "child final",
				Precedence:      sessionInitialInputPreferTransition,
			},
		},
		false,
	)
	if !errors.Is(err, prepareErr) {
		t.Fatalf("destination preparation error = %v, want %v", err, prepareErr)
	}
	if runtimePlan != nil || request.wiring != nil {
		t.Fatalf("failed destination preparation returned composable state plan=%+v request=%+v", runtimePlan, request)
	}
	if initialInputCalls != 0 {
		t.Fatalf("initial input calls = %d, want none before successful runtime preparation", initialInputCalls)
	}

	reopened, err := persistence.Open(child.Dir())
	if err != nil {
		t.Fatalf("reopen child after destination preparation failure: %v", err)
	}
	if reopened.Meta().SessionID != child.Meta().SessionID {
		t.Fatalf("reopened child id = %q, want %q", reopened.Meta().SessionID, child.Meta().SessionID)
	}
}

func TestDestinationInitialInputFailureClosesRuntimeAndLeavesChildReopenableWithoutComposition(t *testing.T) {
	child, persistence := createAuthoritativeTestSession(t, t.TempDir(), "ws", t.TempDir())
	lookupErr := errors.New("destination initial input failed")
	closeErr := errors.New("destination runtime cleanup failed")
	closeCalls := 0
	server := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   t.TempDir(),
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		sessionLifecycle: &recordingSessionLifecycleClient{
			getInitialInput: func(context.Context, serverapi.SessionInitialInputRequest) (serverapi.SessionInitialInputResponse, error) {
				return serverapi.SessionInitialInputResponse{}, lookupErr
			},
		},
		prepareRuntime: func(context.Context, sessionLaunchPlan, io.Writer, string) (*runtimeLaunchPlan, error) {
			return &runtimeLaunchPlan{close: func() error {
				closeCalls++
				return closeErr
			}}, nil
		},
	}

	runtimePlan, request, err := prepareSessionUIRun(
		context.Background(),
		server,
		newSessionLaunchPlanner(server),
		sessionLaunchPlan{
			SessionID:      "parent-session",
			WorkspaceRoot:  server.cfg.WorkspaceRoot,
			ActiveSettings: config.Settings{Model: "gpt-5", Theme: "dark"},
		},
		resolvedSessionHandoff{
			Destination: sessionOpenDestinationForTest(t, "parent-session"),
			InitialInput: sessionInitialInputDirective{
				TransitionInput: "child final",
				Precedence:      sessionInitialInputPreferTransition,
			},
		},
		false,
	)
	if !errors.Is(err, lookupErr) || !errors.Is(err, closeErr) {
		t.Fatalf("destination initial-input error = %v, want lookup and cleanup failures", err)
	}
	if runtimePlan != nil || request.wiring != nil {
		t.Fatalf("failed initial-input preparation returned composable state plan=%+v request=%+v", runtimePlan, request)
	}
	if closeCalls != 1 {
		t.Fatalf("runtime cleanup calls = %d, want 1", closeCalls)
	}

	reopened, err := persistence.Open(child.Dir())
	if err != nil {
		t.Fatalf("reopen child after destination initial-input failure: %v", err)
	}
	if reopened.Meta().SessionID != child.Meta().SessionID {
		t.Fatalf("reopened child id = %q, want %q", reopened.Meta().SessionID, child.Meta().SessionID)
	}
}

func TestDestinationClientPromptRootsFailureJoinsRuntimeCleanupFailure(t *testing.T) {
	child, persistence := createAuthoritativeTestSession(t, t.TempDir(), "ws", t.TempDir())
	promptRootsErr := errors.New("client prompt roots unavailable")
	closeErr := errors.New("destination runtime cleanup failed")
	closeCalls := 0
	baseServer := &testEmbeddedServer{
		cfg: config.App{
			WorkspaceRoot:   t.TempDir(),
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Theme: "dark"},
		},
		prepareRuntime: func(context.Context, sessionLaunchPlan, io.Writer, string) (*runtimeLaunchPlan, error) {
			return &runtimeLaunchPlan{close: func() error {
				closeCalls++
				return closeErr
			}}, nil
		},
	}
	server := clientPromptRootsFailureServer{testEmbeddedServer: baseServer, err: promptRootsErr}

	runtimePlan, request, err := prepareSessionUIRun(
		context.Background(),
		server,
		newSessionLaunchPlanner(server),
		sessionLaunchPlan{
			SessionID:      "parent-session",
			WorkspaceRoot:  baseServer.cfg.WorkspaceRoot,
			ActiveSettings: config.Settings{Model: "gpt-5", Theme: "dark"},
		},
		resolvedSessionHandoff{
			Destination:  sessionOpenDestinationForTest(t, "parent-session"),
			InitialInput: sessionInitialInputDirective{Precedence: sessionInitialInputPreferTransition},
		},
		false,
	)
	if !errors.Is(err, promptRootsErr) || !errors.Is(err, closeErr) {
		t.Fatalf("client-prompt-root preparation error = %v, want prompt-root and cleanup failures", err)
	}
	if runtimePlan != nil || request.wiring != nil {
		t.Fatalf("failed client-prompt-root preparation returned composable state plan=%+v request=%+v", runtimePlan, request)
	}
	if closeCalls != 1 {
		t.Fatalf("runtime cleanup calls = %d, want 1", closeCalls)
	}

	reopened, err := persistence.Open(child.Dir())
	if err != nil {
		t.Fatalf("reopen child after client-prompt-root failure: %v", err)
	}
	if reopened.Meta().SessionID != child.Meta().SessionID {
		t.Fatalf("reopened child id = %q, want %q", reopened.Meta().SessionID, child.Meta().SessionID)
	}
}
