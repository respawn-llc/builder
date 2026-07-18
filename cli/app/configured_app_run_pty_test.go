package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/internal/testharness/pty"
	checkpoint "core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/appfixture"
	"core/internal/testharness/testsetup"
	"core/server/core"
	"core/server/serverstatus"
	"core/server/session"
	serverstartup "core/server/startup"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type configuredPTYReleaseSource struct {
	calls     atomic.Int32
	startOnce sync.Once
	onStart   func()
	release   chan struct{}
}

func newConfiguredPTYReleaseSource() *configuredPTYReleaseSource {
	return &configuredPTYReleaseSource{release: make(chan struct{})}
}

func (s *configuredPTYReleaseSource) LatestRelease(ctx context.Context) (serverstatus.ReleaseMetadata, error) {
	s.calls.Add(1)
	s.startOnce.Do(func() {
		if s.onStart != nil {
			s.onStart()
		}
	})
	select {
	case <-ctx.Done():
		return serverstatus.ReleaseMetadata{}, ctx.Err()
	case <-s.release:
		return serverstatus.ReleaseMetadata{Version: "999.0.0"}, nil
	}
}

func (s *configuredPTYReleaseSource) Release() {
	s.startOnce.Do(func() {
		if s.onStart != nil {
			s.onStart()
		}
	})
	select {
	case <-s.release:
	default:
		close(s.release)
	}
}

func TestAppRunConfiguredUpdateStatusCancellationAndTranscriptLossContinueThroughMainUI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, workspace := newRegisteredAppWorkspace(t)
	releasePort := reserveAppDirectServePort(t)
	releaseSource := newConfiguredPTYReleaseSource()
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
	seedConfiguredPickerSession(t, server, workspace)
	cfg := server.Config()
	waitForConfiguredStartupValidation(t, cfg)
	proxy := startConfiguredPTYRouteProxy(t, config.ServerRPCURL(cfg))
	releaseSource.onStart = func() {
		proxy.publish(configuredPTYRouteEventUpdateRequestAdmitted)
	}
	t.Cleanup(releaseSource.Release)
	processConfigPath := filepath.Join(t.TempDir(), "configured-client.json")
	if err := appfixture.WriteConfiguredClientProcessConfig(processConfigPath, appfixture.ConfiguredClientProcessConfig{
		WorkspaceRoot:            workspace,
		PersistenceRoot:          cfg.PersistenceRoot,
		ConfiguredServerEndpoint: proxy.Endpoint(),
	}); err != nil {
		t.Fatalf("write configured client process config: %v", err)
	}

	capture, err := pty.RunCommand(ctx, pty.CommandSpec{
		Path: os.Args[0],
		Env: []string{
			appfixture.ConfiguredClientProcessConfigEnvName + "=" + processConfigPath,
		},
		Dimensions: pty.MustDimensions(24, 80),
		PhaseInputs: []pty.PhaseInputEvent{
			{
				Phase: pty.PhaseSessionPickerReady,
				Bytes: []byte{'\r'},
			},
			{
				Phase: pty.PhaseMainRequestReachable,
				Bytes: []byte{0x03},
			},
		},
		Timeout: 20 * time.Second,
	})
	if err != nil {
		analysis, analysisErr := pty.Analyze(capture)
		t.Fatalf(
			"run configured pure-client PTY child: %v analysis_err=%v phases=%#v raw=%q",
			err,
			analysisErr,
			analysis.PhaseEvents,
			string(capture.Raw),
		)
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
	required := []pty.PhaseKind{
		pty.PhaseAppRunStarted,
		pty.PhaseSessionPickerReady,
		pty.PhasePickerRequestCanceled,
		pty.PhaseTranscriptSubscriptionEstablished,
		pty.PhaseInitialInputLoaded,
		pty.PhaseTranscriptTransportLost,
		pty.PhaseMainUIReady,
		pty.PhaseMainRequestReachable,
		pty.PhaseAppRunExited,
	}
	for _, phase := range required {
		if !slices.Contains(phases, phase) {
			t.Fatalf("configured pure-client phases = %#v, missing %v", phases, phase)
		}
	}
	mainReadyEvent := analysis.PhaseEvents[slices.Index(phases, pty.PhaseMainUIReady)]
	mainReachableEvent := analysis.PhaseEvents[slices.Index(phases, pty.PhaseMainRequestReachable)]
	screens, err := checkpoint.ReplayCheckpointScreens(capture, []checkpoint.ReplayCheckpoint{
		{ByteOffset: mainReadyEvent.ByteRange.Start},
		{ByteOffset: mainReachableEvent.ByteRange.Start},
	})
	if err != nil {
		t.Fatalf("replay configured main-UI checkpoint screens: %v", err)
	}
	if len(screens) != 2 {
		t.Fatalf("main-UI checkpoint screens = %d, want two", len(screens))
	}
	mainReadyStatusRow := screens[0].TextInRegion(checkpoint.Region{
		Top:    screens[0].Dimensions.Rows - 1,
		Bottom: screens[0].Dimensions.Rows,
		Left:   0,
		Right:  screens[0].Dimensions.Cols,
	})
	if !strings.Contains(mainReadyStatusRow, runtimeDisconnectedStatusMessage) {
		t.Fatalf(
			"main-UI-ready status row = %q, want shared disconnected projection",
			mainReadyStatusRow,
		)
	}
	mainReachableStatusRow := screens[1].TextInRegion(checkpoint.Region{
		Top:    screens[1].Dimensions.Rows - 1,
		Bottom: screens[1].Dimensions.Rows,
		Left:   0,
		Right:  screens[1].Dimensions.Cols,
	})
	if strings.Contains(mainReachableStatusRow, runtimeDisconnectedStatusMessage) {
		t.Fatalf(
			"main-request-reachable status row = %q, want cleared disconnected projection",
			mainReachableStatusRow,
		)
	}
	requirePhaseBefore(t, phases, pty.PhaseAppRunStarted, pty.PhaseSessionPickerReady)
	requirePhaseBefore(t, phases, pty.PhaseSessionPickerReady, pty.PhasePickerRequestCanceled)
	requirePhaseBefore(t, phases, pty.PhaseTranscriptSubscriptionEstablished, pty.PhaseTranscriptTransportLost)
	requirePhaseBefore(t, phases, pty.PhaseInitialInputLoaded, pty.PhaseTranscriptTransportLost)
	requirePhaseBefore(t, phases, pty.PhaseTranscriptTransportLost, pty.PhaseMainUIReady)
	requirePhaseBefore(t, phases, pty.PhaseMainUIReady, pty.PhaseMainRequestReachable)
	requirePhaseBefore(t, phases, pty.PhaseMainRequestReachable, pty.PhaseAppRunExited)
	if calls := releaseSource.calls.Load(); calls != 1 {
		t.Fatalf("configured pure-client update source calls = %d, want exactly one admitted server request", calls)
	}
	releaseSource.Release()
}

func requirePhaseBefore(t *testing.T, phases []pty.PhaseKind, before, after pty.PhaseKind) {
	t.Helper()
	beforeIndex := slices.Index(phases, before)
	afterIndex := slices.Index(phases, after)
	if beforeIndex < 0 || afterIndex < 0 || beforeIndex >= afterIndex {
		t.Fatalf("configured pure-client phases = %#v, want %v before %v", phases, before, after)
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

func seedConfiguredPickerSession(t *testing.T, appServer *serverstartup.ServeServer, workspace string) {
	t.Helper()
	cfg := appServer.Config()
	projectID := appServer.ProjectID()
	containerDir := filepath.Dir(config.ProjectSessionDir(cfg, projectID, "seed-session"))
	store, err := session.Create(
		containerDir,
		filepath.Base(workspace),
		workspace,
		sessioncontract.SessionCategoryMain,
		appServer.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("create selectable configured session: %v", err)
	}
	if err := store.SetName("Configured PTY session"); err != nil {
		t.Fatalf("make configured session launch-visible: %v", err)
	}
	sessionID := store.Meta().SessionID
	var lastPageErr error
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		page, err := appServer.ProjectViewClient().ListSessionPage(context.Background(), serverapi.SessionPageRequest{
			ProjectID: projectID,
			Category:  sessioncontract.SessionCategoryMain,
			PageSize:  sessionPickerPageSize,
			Position:  serverapi.NewestSessionPagePosition(),
		})
		if err != nil {
			lastPageErr = err
			return false
		}
		for _, summary := range page.Sessions {
			if summary.SessionID.String() == sessionID {
				return true
			}
		}
		return false
	}, "configured picker seed did not reach the typed session page: %v", lastPageErr)
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
