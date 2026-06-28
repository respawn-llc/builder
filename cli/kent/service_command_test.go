package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/shared/config"
	"core/shared/protocol"
	"core/shared/sessionenv"
)

func TestMain(m *testing.M) {
	_ = os.Unsetenv(sessionenv.SessionIDEnv)
	os.Exit(m.Run())
}

func TestServiceServeArgumentsBakesPersistenceRoot(t *testing.T) {
	args := serviceServeArguments("/tmp/isolated-root")
	want := []string{"serve", "--persistence-root", "/tmp/isolated-root"}
	if len(args) != len(want) {
		t.Fatalf("serve arguments = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("serve arguments = %v, want %v", args, want)
		}
	}
	cmd := serviceCommand(serviceSpec{Executable: "/usr/local/bin/kent", Arguments: args})
	wantCmd := []string{"/usr/local/bin/kent", "serve", "--persistence-root", "/tmp/isolated-root"}
	if strings.Join(cmd, " ") != strings.Join(wantCmd, " ") {
		t.Fatalf("service command = %v, want %v", cmd, wantCmd)
	}
}

func TestServiceServeArgumentsOmitsEmptyRoot(t *testing.T) {
	args := serviceServeArguments("")
	if len(args) != 1 || args[0] != "serve" {
		t.Fatalf("serve arguments = %v, want [serve]", args)
	}
}

type stubServiceBackend struct {
	status        serviceStatus
	installStart  bool
	installForce  bool
	uninstallStop bool
	calls         []serviceAction
	err           error
	installErr    error
	restartErr    error
	statusErr     error
}

func (s *stubServiceBackend) Name() string { return "stub" }

func (s *stubServiceBackend) Install(_ context.Context, _ serviceSpec, force bool, start bool) error {
	s.calls = append(s.calls, serviceActionInstall)
	s.installForce = force
	s.installStart = start
	if s.installErr != nil {
		return s.installErr
	}
	return s.err
}

func (s *stubServiceBackend) Uninstall(_ context.Context, _ serviceSpec, stop bool) error {
	s.calls = append(s.calls, serviceActionUninstall)
	s.uninstallStop = stop
	return s.err
}

func (s *stubServiceBackend) Start(context.Context, serviceSpec) error {
	s.calls = append(s.calls, serviceActionStart)
	return s.err
}

func (s *stubServiceBackend) Stop(context.Context, serviceSpec) error {
	s.calls = append(s.calls, serviceActionStop)
	return s.err
}

func (s *stubServiceBackend) Restart(context.Context, serviceSpec) error {
	s.calls = append(s.calls, serviceActionRestart)
	if s.restartErr != nil {
		return s.restartErr
	}
	return s.err
}

func (s *stubServiceBackend) Status(context.Context, serviceSpec) (serviceStatus, error) {
	s.calls = append(s.calls, serviceActionStatus)
	if s.statusErr != nil {
		return s.status, s.statusErr
	}
	return s.status, s.err
}

func withServiceCommandTestBackend(t *testing.T, backend *stubServiceBackend) {
	withServiceCommandTestBackendEndpoint(t, backend, "http://127.0.0.1:1")
}

func withServiceCommandTestBackendEndpoint(t *testing.T, backend *stubServiceBackend, endpoint string) {
	t.Helper()
	originalLoadSpec := loadServiceSpec
	originalBackendFactory := serviceBackendFactory
	t.Cleanup(func() {
		loadServiceSpec = originalLoadSpec
		serviceBackendFactory = originalBackendFactory
	})
	loadServiceSpec = func() (serviceSpec, error) {
		host, portText, _ := net.SplitHostPort(strings.TrimPrefix(endpoint, "http://"))
		port := parsePositiveInt(portText)
		// These cases pass no --persistence-root, so the resolved root is the
		// default; an unpinned `serve` registration is the default-root service and
		// must not trip the cross-root guard. Explicit-root cases use
		// withServiceCommandTestSpecRoot, which bakes a matching --persistence-root.
		return serviceSpec{
			Config:        config.App{PersistenceRoot: config.DefaultPersistence, Settings: config.Settings{ServerHost: host, ServerPort: port}},
			Executable:    "/usr/local/bin/kent",
			Arguments:     []string{"serve"},
			LogDir:        "/tmp/kent/logs",
			StdoutLogPath: "/tmp/kent/logs/server.log",
			StderrLogPath: "/tmp/kent/logs/server.err.log",
			Endpoint:      endpoint,
		}, nil
	}
	serviceBackendFactory = func() serviceBackend {
		return backend
	}
}

func newServiceHealthTestServer(t *testing.T, body string, statusCode ...int) *httptest.Server {
	t.Helper()
	code := http.StatusOK
	if len(statusCode) > 0 {
		code = statusCode[0]
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		if code != http.StatusOK {
			http.Error(w, body, code)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestServiceInstallNoStartAndForce(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"install", "--force", "--no-start"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !backend.installForce || backend.installStart {
		t.Fatalf("install flags force=%v start=%v, want force true start false", backend.installForce, backend.installStart)
	}
	if !strings.Contains(stdout.String(), "Started: no") {
		t.Fatalf("stdout = %q, want Started: no", stdout.String())
	}
}

func TestServiceInstallRejectsKentShellSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"install"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestServiceUninstallKeepRunning(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"uninstall", "--keep-running"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	// uninstall first reads status to verify the registration targets this root.
	want := []serviceAction{serviceActionStatus, serviceActionUninstall}
	if strings.Join(actionsToStrings(backend.calls), ",") != strings.Join(actionsToStrings(want), ",") {
		t.Fatalf("calls = %+v, want %+v", backend.calls, want)
	}
	if backend.uninstallStop {
		t.Fatal("expected --keep-running to skip stop")
	}
}

func TestServiceUninstallRejectsKentShellSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"uninstall"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func withServiceCommandTestSpecRoot(t *testing.T, root string) {
	t.Helper()
	// `serviceSubcommand(... --persistence-root ...)` publishes KENT_PERSISTENCE_ROOT
	// process-wide; register it so it is restored and does not leak into later tests.
	t.Setenv(config.PersistenceRootEnvName, "")
	original := loadServiceSpec
	t.Cleanup(func() { loadServiceSpec = original })
	loadServiceSpec = func() (serviceSpec, error) {
		return serviceSpec{
			Config:        config.App{PersistenceRoot: root, Settings: config.Settings{ServerHost: "127.0.0.1", ServerPort: 1}},
			Executable:    "/usr/local/bin/kent",
			Arguments:     serviceServeArguments(root),
			LogDir:        "/tmp/kent/logs",
			StdoutLogPath: "/tmp/kent/logs/server.log",
			StderrLogPath: "/tmp/kent/logs/server.err.log",
			Endpoint:      "http://127.0.0.1:1",
		}, nil
	}
}

func TestServiceStopRejectsRootMismatch(t *testing.T) {
	requestedRoot := filepath.Join(t.TempDir(), "requested")
	installedRoot := filepath.Join(t.TempDir(), "installed")
	backend := &stubServiceBackend{status: serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   true,
		Command:   []string{"/usr/local/bin/kent", "serve", "--persistence-root", installedRoot},
	}}
	withServiceCommandTestBackend(t, backend)
	withServiceCommandTestSpecRoot(t, requestedRoot)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"stop", "--persistence-root", requestedRoot}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr=%q", code, stderr.String())
	}
	for _, call := range backend.calls {
		if call == serviceActionStop {
			t.Fatalf("stop must not run against a registration for a different root; calls=%+v", backend.calls)
		}
	}
	if !strings.Contains(stderr.String(), requestedRoot) || !strings.Contains(stderr.String(), installedRoot) {
		t.Fatalf("stderr = %q, want it to name both the requested and installed roots", stderr.String())
	}
}

func TestServiceStopAllowsMatchingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	backend := &stubServiceBackend{status: serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   true,
		Command:   []string{"/usr/local/bin/kent", "serve", "--persistence-root", root},
	}}
	withServiceCommandTestBackend(t, backend)
	withServiceCommandTestSpecRoot(t, root)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"stop", "--persistence-root", root}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	sawStop := false
	for _, call := range backend.calls {
		if call == serviceActionStop {
			sawStop = true
		}
	}
	if !sawStop {
		t.Fatalf("stop must run when the installed root matches; calls=%+v", backend.calls)
	}
}

func TestServiceRestartIfInstalledTreatsRootMismatchAsNoOp(t *testing.T) {
	requestedRoot := filepath.Join(t.TempDir(), "requested")
	installedRoot := filepath.Join(t.TempDir(), "installed")
	backend := &stubServiceBackend{status: serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   true,
		Command:   []string{"/usr/local/bin/kent", "serve", "--persistence-root", installedRoot},
	}}
	withServiceCommandTestBackend(t, backend)
	withServiceCommandTestSpecRoot(t, requestedRoot)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart", "--if-installed", "--persistence-root", requestedRoot}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	for _, call := range backend.calls {
		if call == serviceActionRestart {
			t.Fatalf("restart --if-installed must no-op when no service exists for the root; calls=%+v", backend.calls)
		}
	}
}

func TestRootMismatchErrorRejectsUnpinnedRegistrationForExplicitRoot(t *testing.T) {
	// A registration with no --persistence-root (a pre-isolation or hand-installed
	// service) is the single global default-root service; targeting it with an
	// explicit non-default root must be refused rather than acting on the wrong one.
	explicitRoot := filepath.Join(t.TempDir(), "iso")
	status := serviceStatus{Installed: true, Command: []string{"/usr/local/bin/kent", "serve"}}
	spec := serviceSpec{Config: config.App{PersistenceRoot: explicitRoot}}
	if err := rootMismatchError(status, spec); err == nil {
		t.Fatal("expected mismatch for an unpinned registration targeted with an explicit non-default root")
	}
}

func TestRootMismatchErrorAllowsUnpinnedRegistrationForDefaultRoot(t *testing.T) {
	defaultRoot, err := config.NormalizePersistenceRoot(config.DefaultPersistence)
	if err != nil {
		t.Fatalf("normalize default root: %v", err)
	}
	status := serviceStatus{Installed: true, Command: []string{"/usr/local/bin/kent", "serve"}}
	spec := serviceSpec{Config: config.App{PersistenceRoot: defaultRoot}}
	if err := rootMismatchError(status, spec); err != nil {
		t.Fatalf("unexpected mismatch for a default-root unpinned registration: %v", err)
	}
}

func TestRootMismatchErrorRejectsUnreadableRegistrationForExplicitRoot(t *testing.T) {
	// An installed service whose registered command the backend could not
	// read/parse (empty command — e.g. a malformed plist or unit) is a root that
	// cannot be confirmed; targeting it with an explicit non-default root must be
	// refused rather than acting on the single global registration.
	explicitRoot := filepath.Join(t.TempDir(), "iso")
	status := serviceStatus{Installed: true, Command: nil}
	spec := serviceSpec{Config: config.App{PersistenceRoot: explicitRoot}}
	if err := rootMismatchError(status, spec); err == nil {
		t.Fatal("expected mismatch for an unreadable registration targeted with an explicit non-default root")
	}
}

func TestRootMismatchErrorAllowsUnreadableRegistrationForDefaultRoot(t *testing.T) {
	defaultRoot, err := config.NormalizePersistenceRoot(config.DefaultPersistence)
	if err != nil {
		t.Fatalf("normalize default root: %v", err)
	}
	status := serviceStatus{Installed: true, Command: nil}
	spec := serviceSpec{Config: config.App{PersistenceRoot: defaultRoot}}
	if err := rootMismatchError(status, spec); err != nil {
		t.Fatalf("unexpected mismatch for a default-root unreadable registration: %v", err)
	}
}

func TestServiceStatusReportsNotInstalledForForeignRoot(t *testing.T) {
	// A registration whose command targets a different root must be reported as
	// not installed for the requested root, so `service status --persistence-root`
	// never presents another root's service as installed/running for this one.
	requestedRoot := filepath.Join(t.TempDir(), "requested")
	installedRoot := filepath.Join(t.TempDir(), "installed")
	backend := &stubServiceBackend{status: serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   true,
		PID:       4242,
		Command:   []string{"/usr/local/bin/kent", "serve", "--persistence-root", installedRoot},
	}}
	withServiceCommandTestBackend(t, backend)
	withServiceCommandTestSpecRoot(t, requestedRoot)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"status", "--json", "--persistence-root", requestedRoot}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var status serviceStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode status json: %v; stdout=%q", err, stdout.String())
	}
	if status.Installed || status.Running || status.Loaded || status.PID != 0 {
		t.Fatalf("status = %+v, want not installed/running/loaded for a foreign root", status)
	}
}

func TestServiceRestartIfInstalledSkipsMissingService(t *testing.T) {
	backend := &stubServiceBackend{status: serviceStatus{Installed: false}}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart", "--if-installed"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if len(backend.calls) != 1 || backend.calls[0] != serviceActionStatus {
		t.Fatalf("calls = %+v, want status only", backend.calls)
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("stdout = %q, want quiet no-op", stdout.String())
	}
}

func TestServiceRestartIfInstalledRefreshesRegistrationBeforeRestart(t *testing.T) {
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart", "--if-installed"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := []serviceAction{serviceActionStatus, serviceActionStatus, serviceActionInstall}
	if strings.Join(actionsToStrings(backend.calls), ",") != strings.Join(actionsToStrings(want), ",") {
		t.Fatalf("calls = %+v, want %+v", backend.calls, want)
	}
	if !backend.installForce || !backend.installStart {
		t.Fatalf("refresh flags force=%v start=%v, want force true start true", backend.installForce, backend.installStart)
	}
	if !strings.Contains(stdout.String(), "sessions may fail briefly") {
		t.Fatalf("stdout = %q, want restart warning", stdout.String())
	}
}

func TestServiceRestartIfInstalledStopsWhenRefreshFails(t *testing.T) {
	backend := &stubServiceBackend{status: serviceStatus{Installed: true}, installErr: errors.New("install failed")}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart", "--if-installed"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := []serviceAction{serviceActionStatus, serviceActionStatus, serviceActionInstall}
	if strings.Join(actionsToStrings(backend.calls), ",") != strings.Join(actionsToStrings(want), ",") {
		t.Fatalf("calls = %+v, want %+v", backend.calls, want)
	}
	if strings.Contains(strings.Join(actionsToStrings(backend.calls), ","), string(serviceActionRestart)) {
		t.Fatalf("restart should not be called after install failure: %+v", backend.calls)
	}
	if !strings.Contains(stderr.String(), "install failed") {
		t.Fatalf("stderr = %q, want install error", stderr.String())
	}
}

func TestServiceStatusJSON(t *testing.T) {
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 123}}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"status", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded serviceStatus
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode status json: %v; raw=%q", err, stdout.String())
	}
	if !decoded.Installed || !decoded.Running || decoded.PID != 123 || decoded.Backend != "stub" {
		t.Fatalf("decoded status = %+v", decoded)
	}
}

func TestServiceStatusKeepsManualHealthSeparateFromServiceRunningState(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"status", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	var decoded serviceStatus
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("decode status json: %v; raw=%q", err, stdout.String())
	}
	if decoded.Running {
		t.Fatalf("running = true, want false for backend stopped status: %+v", decoded)
	}
	if decoded.HealthStatus != protocol.HealthStatusOK || decoded.HealthPID != 123 {
		t.Fatalf("health status = %q pid=%d, want ok/123", decoded.HealthStatus, decoded.HealthPID)
	}
}

func actionsToStrings(actions []serviceAction) []string {
	out := make([]string, 0, len(actions))
	for _, action := range actions {
		out = append(out, string(action))
	}
	return out
}

func TestServiceInstallRejectsUnmanagedRunningServer(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: false, Loaded: false, Running: false}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"install"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 1 || backend.calls[0] != serviceActionStatus {
		t.Fatalf("calls = %+v, want status only", backend.calls)
	}
	if !strings.Contains(stderr.String(), "outside the background service") {
		t.Fatalf("stderr = %q, want unmanaged conflict", stderr.String())
	}
}

func TestServiceInstallAllowsHealthyServerOwnedByLoadedService(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 123}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"install", "--force"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	if len(backend.calls) != 2 || backend.calls[0] != serviceActionStatus || backend.calls[1] != serviceActionInstall {
		t.Fatalf("calls = %+v, want status then install", backend.calls)
	}
	if !backend.installForce || !backend.installStart {
		t.Fatalf("install flags force=%v start=%v, want force true start true", backend.installForce, backend.installStart)
	}
}

func TestServiceRestartAllowsHealthyServerOwnedByLoadedServiceBeforePIDIsVisible(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   true,
		Command:   []string{"/usr/local/bin/kent", "serve"},
	}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := []serviceAction{serviceActionStatus, serviceActionRestart}
	if strings.Join(actionsToStrings(backend.calls), ",") != strings.Join(actionsToStrings(want), ",") {
		t.Fatalf("calls = %+v, want %+v", backend.calls, want)
	}
}

func TestServiceRestartRejectsKentShellSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 123}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestServiceLifecycleGuardRejectsBeforeSpecLoad(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{status: serviceStatus{Installed: true}}
	originalLoadSpec := loadServiceSpec
	originalBackendFactory := serviceBackendFactory
	t.Cleanup(func() {
		loadServiceSpec = originalLoadSpec
		serviceBackendFactory = originalBackendFactory
	})
	loadServiceSpec = func() (serviceSpec, error) {
		return serviceSpec{}, errors.New("spec load should not run")
	}
	serviceBackendFactory = func() serviceBackend {
		return backend
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestServiceRestartIfInstalledRejectsKentShellSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 123}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart", "--if-installed"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestServiceRestartIfInstalledRejectsKentShellSessionBeforeMissingServiceBypass(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{status: serviceStatus{Installed: false}}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart", "--if-installed"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestServiceRestartHelpWritesToStderr(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart", "--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Fatal("stderr is empty, want help output")
	}
}

func TestServiceRestartRejectsCurrentShellSessionBeforeHealthProbe(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 123}}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestServiceRestartAllowsHealthyKentServerRecoveryWhenLoadedServiceIsNotRunning(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   false,
		Command:   []string{"/usr/local/bin/kent", "serve"},
	}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := []serviceAction{serviceActionStatus, serviceActionRestart}
	if strings.Join(actionsToStrings(backend.calls), ",") != strings.Join(actionsToStrings(want), ",") {
		t.Fatalf("calls = %+v, want %+v", backend.calls, want)
	}
}

func TestServiceRestartAllowsUnloadedInstalledServiceToRecoverHealthyKentServer(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := []serviceAction{serviceActionStatus, serviceActionRestart}
	if strings.Join(actionsToStrings(backend.calls), ",") != strings.Join(actionsToStrings(want), ",") {
		t.Fatalf("calls = %+v, want %+v", backend.calls, want)
	}
}

func TestServiceStartRejectsUnmanagedRunningServer(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"start"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 1 || backend.calls[0] != serviceActionStatus {
		t.Fatalf("calls = %+v, want status only", backend.calls)
	}
}

func TestServiceStartRejectsKentShellSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: false}}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"start"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestServiceStartCallsBackendOutsideKentShell(t *testing.T) {
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: false}}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"start"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := []serviceAction{serviceActionStatus, serviceActionStart}
	if strings.Join(actionsToStrings(backend.calls), ",") != strings.Join(actionsToStrings(want), ",") {
		t.Fatalf("calls = %+v, want %+v", backend.calls, want)
	}
	if !strings.Contains(stdout.String(), "Started") {
		t.Fatalf("stdout = %q, want start confirmation", stdout.String())
	}
}

func TestServiceStopRejectsKentShellSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true}}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"stop"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 0 {
		t.Fatalf("calls = %+v, want no service backend calls", backend.calls)
	}
	if got := strings.TrimSpace(stderr.String()); got != serviceLifecycleCurrentSessionError {
		t.Fatalf("stderr = %q, want current session lifecycle guard", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestServiceRestartRejectsRunningServerWhenServicePIDMismatches(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   true,
		PID:       456,
		Command:   []string{"/other/kent", "serve"},
	}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 1 || backend.calls[0] != serviceActionStatus {
		t.Fatalf("calls = %+v, want status only", backend.calls)
	}
	if !strings.Contains(stderr.String(), "outside the background service") {
		t.Fatalf("stderr = %q, want unmanaged conflict", stderr.String())
	}
}

func TestServiceRestartRejectsRunningServerWhenOwnershipPIDMissingAndCommandDiffers(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok"}`)
	backend := &stubServiceBackend{status: serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   true,
		Command:   []string{"/other/kent", "serve"},
	}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if len(backend.calls) != 1 || backend.calls[0] != serviceActionStatus {
		t.Fatalf("calls = %+v, want status only", backend.calls)
	}
}

func TestServiceRestartAllowsUnhealthyListenerWhenServiceRunning(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"starting","pid":123}`, http.StatusServiceUnavailable)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 123}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"restart"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	want := []serviceAction{serviceActionStatus, serviceActionRestart}
	if strings.Join(actionsToStrings(backend.calls), ",") != strings.Join(actionsToStrings(want), ",") {
		t.Fatalf("calls = %+v, want %+v", backend.calls, want)
	}
}

func TestServiceActionErrorReturnsOne(t *testing.T) {
	backend := &stubServiceBackend{err: errors.New("boom")}
	withServiceCommandTestBackend(t, backend)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand([]string{"start"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Fatalf("stderr = %q, want boom", stderr.String())
	}
}
