package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/appfixture"
	"core/internal/testharness/testsetup"
	"core/server/core"
	"core/server/serverstatus"
	serverstartup "core/server/startup"
	"core/shared/config"
	"core/shared/serverapi"
)

type configuredPTYReleaseSource struct {
	calls atomic.Int32
}

func (s *configuredPTYReleaseSource) LatestRelease(context.Context) (serverstatus.ReleaseMetadata, error) {
	s.calls.Add(1)
	return serverstatus.ReleaseMetadata{Version: "999.0.0"}, nil
}

func TestAppRunConfiguredUpdateStatusReachesSessionPickerAndExitsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, workspace := newRegisteredAppWorkspace(t)
	releasePort := reserveAppDirectServePort(t)
	releaseSource := &configuredPTYReleaseSource{}
	server, err := serverstartup.StartServeServerWithOptions(
		ctx,
		serverstartup.Request{
			WorkspaceRoot:         workspace,
			WorkspaceRootExplicit: true,
			Model:                 "gpt-5",
		},
		apiKeyMemoryAuthHandler("test-key"),
		autoOnboarding,
		serverstartup.Options{Core: core.Options{
			ServerStatus: serverstatus.Dependencies{ReleaseSource: releaseSource},
		}},
	)
	if err != nil {
		t.Fatalf("StartServeServerWithOptions: %v", err)
	}
	stopServing := serveAppServerAfter(t, server, releasePort)
	t.Cleanup(func() {
		stopServing()
		if closeErr := server.Close(); closeErr != nil {
			t.Errorf("ServeServer.Close: %v", closeErr)
		}
	})
	seedConfiguredPickerSession(t, workspace)
	cfg := server.Config()
	waitForConfiguredStartupValidation(t, cfg)
	processConfigPath := filepath.Join(t.TempDir(), "configured-client.json")
	if err := appfixture.WriteConfiguredClientProcessConfig(processConfigPath, appfixture.ConfiguredClientProcessConfig{
		WorkspaceRoot:            workspace,
		PersistenceRoot:          cfg.PersistenceRoot,
		ConfiguredServerEndpoint: config.ServerRPCURL(cfg),
	}); err != nil {
		t.Fatalf("write configured client process config: %v", err)
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path: os.Args[0],
		Env: []string{
			appfixture.ConfiguredClientProcessConfigEnvName + "=" + processConfigPath,
		},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{{
			Phase: pty.PhaseSessionPickerReady,
			Bytes: []byte{0x03},
		}},
		Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("run configured pure-client PTY child: %v", err)
	}
	if capture.ProcessExit == nil || capture.ProcessExit.Code != 0 {
		t.Fatalf("configured pure-client process exit = %#v, want clean exit", capture.ProcessExit)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("analyze configured pure-client PTY capture: %v", err)
	}
	phases := make([]pty.PhaseKind, 0, len(analysis.PhaseEvents))
	for _, event := range analysis.PhaseEvents {
		phases = append(phases, event.Phase)
	}
	pickerIndex := slices.Index(phases, pty.PhaseSessionPickerReady)
	exitIndex := slices.Index(phases, pty.PhaseAppRunExited)
	if !slices.Contains(phases, pty.PhaseAppRunStarted) || pickerIndex < 0 || exitIndex < 0 || pickerIndex >= exitIndex {
		t.Fatalf("configured pure-client phases = %#v, want app start, picker ready, then app exit", phases)
	}
	if calls := releaseSource.calls.Load(); calls != 1 {
		testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
			return releaseSource.calls.Load() == 1
		}, "configured pure-client picker did not issue update status through the configured Remote: calls=%d", calls)
	}
}

func waitForConfiguredStartupValidation(t *testing.T, cfg config.App) {
	t.Helper()
	var lastErr error
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		remote, err := attachConfiguredStartupRemote(context.Background(), cfg)
		if err != nil {
			lastErr = err
			return false
		}
		if closeErr := remote.Close(); closeErr != nil {
			lastErr = closeErr
			return false
		}
		return true
	}, "configured remote did not complete startup validation: %v", lastErr)
}

func seedConfiguredPickerSession(t *testing.T, workspace string) {
	t.Helper()
	server, err := startSessionServer(context.Background(), Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
	}, newHeadlessAuthInteractor(), false)
	if err != nil {
		t.Fatalf("attach configured server to seed picker session: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := server.Close(); closeErr != nil {
			t.Errorf("close configured seeding client: %v", closeErr)
		}
	})
	planner := newSessionLaunchPlanner(server)
	intent := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
	if _, err := planner.PlanSession(context.Background(), sessionLaunchRequest{
		Mode:   launchModeInteractive,
		Intent: intent,
	}); err != nil {
		t.Fatalf("create selectable session through configured server: %v", err)
	}
}

func TestAppRunConfiguredUnavailableEndpointDoesNotStartServer(t *testing.T) {
	cfgRoot := t.TempDir()
	t.Setenv(config.PersistenceRootEnvName, cfgRoot)
	t.Setenv("KENT_SERVER_HOST", "127.0.0.1")
	t.Setenv("KENT_SERVER_PORT", "1")

	err := Run(context.Background(), Options{
		WorkspaceRoot:         t.TempDir(),
		WorkspaceRootExplicit: true,
		ConfigRoot:            cfgRoot,
	})
	var preflight *configuredServerPreflightError
	if !errors.As(err, &preflight) {
		t.Fatalf("Run error = %v, want configuredServerPreflightError", err)
	}
	if preflight.operation != "attach" || preflight.endpoint == "" {
		t.Fatalf("preflight = %+v, want endpoint-scoped attach failure", preflight)
	}
}
