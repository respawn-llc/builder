package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/pty"
	serverstartup "core/server/startup"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protoapi"
	onboardingpb "core/shared/protoapi/gen/kent/api/onboarding"
	"core/shared/rpcwire"
	"core/shared/theme"
)

const onboardingRemoteLifecycleConfigEnv = "KENT_ONBOARDING_REMOTE_LIFECYCLE_CONFIG"

type onboardingRemoteLifecycleProcessConfig struct {
	Endpoint   string  `json:"endpoint"`
	CancelPath *string `json:"cancel_path,omitempty"`
	ResultPath string  `json:"result_path"`
}

type onboardingRemoteLifecycleProcessResult struct {
	Completed bool `json:"completed"`
	Canceled  bool `json:"canceled"`
}

func runOnboardingRemoteLifecycleHelper(configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read onboarding lifecycle process config: %w", err)
	}
	var processConfig onboardingRemoteLifecycleProcessConfig
	if err := json.Unmarshal(data, &processConfig); err != nil {
		return fmt.Errorf("decode onboarding lifecycle process config: %w", err)
	}
	if strings.TrimSpace(processConfig.Endpoint) == "" {
		return errors.New("onboarding lifecycle endpoint is required")
	}
	if strings.TrimSpace(processConfig.ResultPath) == "" {
		return errors.New("onboarding lifecycle result path is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if processConfig.CancelPath != nil {
		if strings.TrimSpace(*processConfig.CancelPath) == "" {
			return errors.New("onboarding lifecycle cancellation path must not be empty")
		}
		go cancelOnboardingLifecycleWhenFileExists(ctx, cancel, *processConfig.CancelPath)
	}
	remote, err := client.DialRemoteURL(ctx, processConfig.Endpoint)
	if err != nil {
		return fmt.Errorf("dial onboarding lifecycle remote: %w", err)
	}
	defer remote.Close()
	settings := config.DefaultOnboardingSettings()
	settings.Theme = theme.Dark
	settings.Reviewer.Model = settings.Model
	settings.Reviewer.ThinkingLevel = settings.ThinkingLevel
	result, err := runOnboardingFlow(ctx, config.App{
		Settings: settings,
		Source: config.SourceReport{Sources: map[string]string{
			"thinking_level":          "default",
			"reviewer.model":          "default",
			"reviewer.thinking_level": "default",
		}},
	}, remote, remote)
	output := onboardingRemoteLifecycleProcessResult{
		Completed: result.Completed,
		Canceled:  errors.Is(err, context.Canceled) || errors.Is(err, ErrOnboardingCanceled),
	}
	encoded, encodeErr := json.Marshal(output)
	if encodeErr != nil {
		return fmt.Errorf("encode onboarding lifecycle result: %w", encodeErr)
	}
	if writeErr := os.WriteFile(processConfig.ResultPath, encoded, 0o600); writeErr != nil {
		return fmt.Errorf("write onboarding lifecycle result: %w", writeErr)
	}
	if err != nil && !output.Canceled {
		return err
	}
	return nil
}

func cancelOnboardingLifecycleWhenFileExists(ctx context.Context, cancel context.CancelFunc, path string) {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type onboardingRPCGate struct {
	backendURL              string
	started                 chan struct{}
	release                 chan struct{}
	finalized               chan struct{}
	closed                  chan struct{}
	finalizationMu          sync.Mutex
	finalizationCorrelation string
	startOnce               sync.Once
	releaseOnce             sync.Once
	finalizedOnce           sync.Once
	closeOnce               sync.Once
}

func newOnboardingRPCGate(backendURL string) *onboardingRPCGate {
	return &onboardingRPCGate{
		backendURL: backendURL,
		started:    make(chan struct{}),
		release:    make(chan struct{}),
		finalized:  make(chan struct{}),
		closed:     make(chan struct{}),
	}
}

func (g *onboardingRPCGate) Release() {
	g.releaseOnce.Do(func() { close(g.release) })
}

func (g *onboardingRPCGate) Handler(ctx context.Context, conn rpcwire.Conn) {
	defer g.closeOnce.Do(func() { close(g.closed) })
	endpoint, err := rpcwire.ParseWebSocketEndpoint(g.backendURL)
	if err != nil {
		return
	}
	backend, err := rpcwire.NewWebSocketTransport().Dial(ctx, endpoint)
	if err != nil {
		return
	}
	defer backend.Close()
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		for event := range backend.Events() {
			if event.Err != nil {
				return
			}
			if g.isFinalizationResponse(event.Frame) {
				g.finalizedOnce.Do(func() { close(g.finalized) })
			}
			if err := conn.Send(ctx, event.Frame); err != nil {
				return
			}
		}
	}()
	for event := range conn.Events() {
		if event.Err != nil {
			return
		}
		if correlation, finalization := onboardingFinalizationCorrelation(event.Frame); finalization {
			g.finalizationMu.Lock()
			g.finalizationCorrelation = correlation
			g.finalizationMu.Unlock()
			g.startOnce.Do(func() { close(g.started) })
			<-g.release
		}
		if err := backend.Send(ctx, event.Frame); err != nil {
			return
		}
	}
}

func onboardingFinalizationCorrelation(frame rpcwire.Frame) (string, bool) {
	if frame.Kind != rpcwire.FrameBinary {
		return "", false
	}
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil || envelope.GetCall() == nil || envelope.GetCall().Correlation == nil {
		return "", false
	}
	method := onboardingpb.File_kent_api_onboarding_onboarding_proto.Services().
		ByName("OnboardingService").Methods().ByName("Finalize")
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil || envelope.GetCall().Operation != operation.Name {
		return "", false
	}
	return envelope.GetCall().GetCorrelation(), true
}

func (g *onboardingRPCGate) isFinalizationResponse(frame rpcwire.Frame) bool {
	if frame.Kind != rpcwire.FrameBinary {
		return false
	}
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil || envelope.GetResult() == nil {
		return false
	}
	g.finalizationMu.Lock()
	defer g.finalizationMu.Unlock()
	return g.finalizationCorrelation != "" &&
		g.finalizationCorrelation == envelope.GetResult().GetCorrelation()
}

type gatedOnboardingServer struct {
	gate   *onboardingRPCGate
	server *httptest.Server
}

func newGatedOnboardingServer(t *testing.T) *gatedOnboardingServer {
	t.Helper()
	_, workspace := newRegisteredAppWorkspaceWithoutSettings(t)
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		AllowUnauthenticated:  true,
	}, memoryAuthHandler{}, nil)
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	stopServing := serveAppServer(t, srv)
	t.Cleanup(stopServing)

	deadline := time.Now().Add(5 * time.Second)
	for {
		remote, attachErr := attachConfiguredStartupRemote(context.Background(), cfg)
		if attachErr == nil {
			if err := remote.Close(); err != nil {
				t.Fatalf("close readiness remote: %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("wait for onboarding server: %v", attachErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	gate := newOnboardingRPCGate(config.ServerRPCURL(cfg))
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(gate.Handler))
	t.Cleanup(server.Close)
	return &gatedOnboardingServer{
		gate:   gate,
		server: server,
	}
}

func (s *gatedOnboardingServer) endpoint() string {
	return "ws" + s.server.URL[len("http"):]
}

func TestOnboardingRemoteLifecycleKeepsSubmittedRPCAliveAfterParentCancellation(t *testing.T) {
	binary := onboardingRemoteLifecycleTestExecutable(t)
	server := newGatedOnboardingServer(t)
	root := t.TempDir()
	cancelPath := filepath.Join(root, "cancel")
	resultPath := filepath.Join(root, "result.json")
	processConfigPath := writeOnboardingRemoteLifecycleProcessConfig(t, root, onboardingRemoteLifecycleProcessConfig{
		Endpoint:   server.endpoint(),
		CancelPath: &cancelPath,
		ResultPath: resultPath,
	})
	parentResult := make(chan error, 1)
	go func() {
		select {
		case <-server.gate.started:
			if err := os.WriteFile(cancelPath, nil, 0o600); err != nil {
				parentResult <- err
				return
			}
			select {
			case <-server.gate.closed:
				parentResult <- errors.New("remote closed before the submitted RPC reached a terminal result")
				return
			case <-time.After(100 * time.Millisecond):
			}
			server.gate.Release()
			parentResult <- nil
		case <-time.After(5 * time.Second):
			parentResult <- errors.New("onboarding finalization request did not reach the server gate")
		}
	}()

	capture, err := pty.RunCommand(context.Background(), pty.CommandSpec{
		Path:       binary,
		Args:       []string{onboardingRemoteLifecycleTestRunArgument},
		Env:        []string{onboardingRemoteLifecycleConfigEnv + "=" + processConfigPath},
		Dimensions: pty.MustDimensions(24, 80),
		ParseableInputs: []pty.ParseableInputEvent{
			{Bytes: []byte("\r\x1b[B\r")},
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("run onboarding lifecycle helper: %v raw=%q", err, string(capture.Raw))
	}
	if err := <-parentResult; err != nil {
		t.Fatalf("coordinate submitted finalization: %v", err)
	}
	result := readOnboardingRemoteLifecycleProcessResult(t, resultPath)
	if !result.Completed || result.Canceled {
		t.Fatalf("onboarding result = %+v, want completed terminal server result", result)
	}
	waitForOnboardingGateClose(t, server.gate.closed)
}

func TestOnboardingRemoteLifecycleEscapeCancelsBeforeFinalization(t *testing.T) {
	binary := onboardingRemoteLifecycleTestExecutable(t)
	server := newGatedOnboardingServer(t)
	root := t.TempDir()
	resultPath := filepath.Join(root, "result.json")
	processConfigPath := writeOnboardingRemoteLifecycleProcessConfig(t, root, onboardingRemoteLifecycleProcessConfig{
		Endpoint:   server.endpoint(),
		ResultPath: resultPath,
	})
	capture, err := pty.RunCommand(context.Background(), pty.CommandSpec{
		Path:       binary,
		Args:       []string{onboardingRemoteLifecycleTestRunArgument},
		Env:        []string{onboardingRemoteLifecycleConfigEnv + "=" + processConfigPath},
		Dimensions: pty.MustDimensions(24, 80),
		ParseableInputs: []pty.ParseableInputEvent{
			{Bytes: []byte{0x1b}},
		},
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("run onboarding escape helper: %v raw=%q", err, string(capture.Raw))
	}
	result := readOnboardingRemoteLifecycleProcessResult(t, resultPath)
	if !result.Canceled || result.Completed {
		t.Fatalf("onboarding result = %+v, want pre-submission cancellation", result)
	}
	select {
	case <-server.gate.started:
		t.Fatal("escape before submission must not invoke onboarding finalization")
	default:
	}
	waitForOnboardingGateClose(t, server.gate.closed)
}

func TestOnboardingFinalizationRemoteDeadlineIsIndeterminateUntilCallerClosesRemote(t *testing.T) {
	server := newGatedOnboardingServer(t)
	remote, err := client.DialRemoteURL(context.Background(), server.endpoint())
	if err != nil {
		t.Fatalf("dial gated remote: %v", err)
	}
	finalization := newOnboardingFinalization(remote, context.Background())
	finalization.timeout = time.Second
	if err := finalization.start(&onboardingpb.FinalizeRequest{
		Theme:          ptrOnboardingTheme(theme.Dark),
		CommandsImport: &onboardingpb.ImportSelection{Mode: onboardingpb.ImportMode_IMPORT_MODE_NONE},
	}, true, theme.Dark); err != nil {
		t.Fatalf("start finalization: %v", err)
	}
	select {
	case <-server.gate.started:
	case <-time.After(5 * time.Second):
		t.Fatal("onboarding finalization request did not reach the server gate")
	}
	outcome, submitted := finalization.waitIfSubmitted()
	if !submitted || outcome.err == nil || !onboardingFinalizationIndeterminate(outcome.err) {
		t.Fatalf("deadline outcome = %+v submitted=%v, want indeterminate submitted result", outcome, submitted)
	}
	select {
	case <-server.gate.closed:
		t.Fatal("finalization lifecycle must not close the caller-owned remote")
	default:
	}
	server.gate.Release()
	waitForOnboardingFinalizationCompletion(t, server.gate.finalized)
	if err := remote.Close(); err != nil {
		t.Fatalf("close caller-owned remote: %v", err)
	}
	waitForOnboardingGateClose(t, server.gate.closed)
}

func ptrOnboardingTheme(value string) *onboardingpb.Theme {
	themeValue := onboardingpb.Theme_THEME_UNSPECIFIED
	switch value {
	case theme.Dark:
		themeValue = onboardingpb.Theme_THEME_DARK
	case theme.Light:
		themeValue = onboardingpb.Theme_THEME_LIGHT
	default:
		panic("unsupported onboarding theme fixture")
	}
	return &themeValue
}

func writeOnboardingRemoteLifecycleProcessConfig(t *testing.T, root string, processConfig onboardingRemoteLifecycleProcessConfig) string {
	t.Helper()
	data, err := json.Marshal(processConfig)
	if err != nil {
		t.Fatalf("encode onboarding lifecycle process config: %v", err)
	}
	path := filepath.Join(root, "process.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write onboarding lifecycle process config: %v", err)
	}
	return path
}

func readOnboardingRemoteLifecycleProcessResult(t *testing.T, path string) onboardingRemoteLifecycleProcessResult {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read onboarding lifecycle process result: %v", err)
	}
	var result onboardingRemoteLifecycleProcessResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("decode onboarding lifecycle process result: %v", err)
	}
	return result
}

func waitForOnboardingFinalizationCompletion(t *testing.T, finalized <-chan struct{}) {
	t.Helper()
	select {
	case <-finalized:
	case <-time.After(5 * time.Second):
		t.Fatal("onboarding finalization did not complete after release")
	}
}

func waitForOnboardingGateClose(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("caller-owned onboarding remote did not close")
	}
}

func onboardingRemoteLifecycleTestExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve onboarding lifecycle test executable: %v", err)
	}
	return executable
}
