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
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("serve arguments = %v, want %v", args, want)
	}

	cmd := serviceCommand(serviceSpec{Executable: "/usr/local/bin/kent", Arguments: args})
	wantCmd := []string{"/usr/local/bin/kent", "serve", "--persistence-root", "/tmp/isolated-root"}
	if strings.Join(cmd, "\x00") != strings.Join(wantCmd, "\x00") {
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

func withServiceCommandTestSpecRoot(t *testing.T, root string) {
	t.Helper()
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

func runServiceCommandForTest(args ...string) (string, string, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := serviceSubcommand(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
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

func TestServiceStatusJSONReportsBackendSpecAndHealth(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 123}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	stdout, stderr, code := runServiceCommandForTest("status", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	var decoded serviceStatus
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode status json: %v; raw=%q", err, stdout)
	}
	if decoded.Backend != "stub" || !decoded.Installed || !decoded.Running || decoded.PID != 123 {
		t.Fatalf("decoded status = %+v, want installed running stub", decoded)
	}
	if decoded.Endpoint != server.URL || len(decoded.Logs) != 2 {
		t.Fatalf("decoded status endpoint/logs = %q/%+v", decoded.Endpoint, decoded.Logs)
	}
	if decoded.HealthStatus != protocol.HealthStatusOK || decoded.HealthPID != 123 {
		t.Fatalf("health status = %q pid=%d, want ok/123", decoded.HealthStatus, decoded.HealthPID)
	}
}

func TestServiceStatusKeepsHealthSeparateFromBackendRunningState(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	stdout, stderr, code := runServiceCommandForTest("status", "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	var decoded serviceStatus
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode status json: %v; raw=%q", err, stdout)
	}
	if decoded.Running {
		t.Fatalf("running = true, want false when backend reports stopped: %+v", decoded)
	}
	if decoded.HealthStatus != protocol.HealthStatusOK || decoded.HealthPID != 123 {
		t.Fatalf("health status = %q pid=%d, want ok/123", decoded.HealthStatus, decoded.HealthPID)
	}
}

func TestServiceLifecycleGuardBlocksMutatingCurrentSessionActions(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")

	for _, args := range [][]string{
		{"install"},
		{"uninstall"},
		{"start"},
		{"stop"},
		{"restart"},
		{"restart", "--if-installed"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			backend := &stubServiceBackend{status: serviceStatus{Installed: true}}
			withServiceCommandTestBackend(t, backend)

			stdout, _, code := runServiceCommandForTest(args...)
			if code != 1 {
				t.Fatalf("%v exit code = %d, want 1", args, code)
			}
			if stdout != "" {
				t.Fatalf("%v stdout = %q, want empty", args, stdout)
			}
			if len(backend.calls) != 0 {
				t.Fatalf("%v backend calls = %+v, want none", args, backend.calls)
			}
		})
	}
}

func TestServiceLifecycleGuardRejectsBeforeSpecLoad(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")
	originalLoadSpec := loadServiceSpec
	originalBackendFactory := serviceBackendFactory
	t.Cleanup(func() {
		loadServiceSpec = originalLoadSpec
		serviceBackendFactory = originalBackendFactory
	})
	loadCalled := false
	backend := &stubServiceBackend{}
	loadServiceSpec = func() (serviceSpec, error) {
		loadCalled = true
		return serviceSpec{}, errors.New("spec load should not run")
	}
	serviceBackendFactory = func() serviceBackend {
		return backend
	}

	stdout, _, code := runServiceCommandForTest("restart")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if loadCalled {
		t.Fatal("service lifecycle guard loaded spec before rejecting current-session mutation")
	}
	if stdout != "" || len(backend.calls) != 0 {
		t.Fatalf("stdout=%q calls=%+v, want no side effects", stdout, backend.calls)
	}
}

func TestServiceLifecycleGuardAllowsNonDisruptiveFlagsInsideCurrentSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-123")

	t.Run("uninstall_keep_running", func(t *testing.T) {
		backend := &stubServiceBackend{status: serviceStatus{Installed: true}}
		withServiceCommandTestBackend(t, backend)

		_, stderr, code := runServiceCommandForTest("uninstall", "--keep-running")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if backend.uninstallStop {
			t.Fatal("expected --keep-running to skip stop")
		}
		if !serviceBackendSawAction(backend.calls, serviceActionUninstall) {
			t.Fatalf("backend calls = %+v, want uninstall", backend.calls)
		}
	})
}

func TestServiceRootMismatchBoundaries(t *testing.T) {
	requestedRoot := filepath.Join(t.TempDir(), "requested")
	installedRoot := filepath.Join(t.TempDir(), "installed")
	foreignRegistration := serviceStatus{
		Installed: true,
		Loaded:    true,
		Running:   true,
		PID:       4242,
		Command:   []string{"/usr/local/bin/kent", "serve", "--persistence-root", installedRoot},
	}

	t.Run("status_reports_not_installed", func(t *testing.T) {
		backend := &stubServiceBackend{status: foreignRegistration}
		withServiceCommandTestBackend(t, backend)
		withServiceCommandTestSpecRoot(t, requestedRoot)

		stdout, stderr, code := runServiceCommandForTest("status", "--json", "--persistence-root", requestedRoot)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		var status serviceStatus
		if err := json.Unmarshal([]byte(stdout), &status); err != nil {
			t.Fatalf("decode status json: %v; stdout=%q", err, stdout)
		}
		if status.Installed || status.Running || status.Loaded || status.PID != 0 {
			t.Fatalf("status = %+v, want not installed/running/loaded for a foreign root", status)
		}
	})

	t.Run("stop_rejects_foreign_registration", func(t *testing.T) {
		backend := &stubServiceBackend{status: foreignRegistration}
		withServiceCommandTestBackend(t, backend)
		withServiceCommandTestSpecRoot(t, requestedRoot)

		_, _, code := runServiceCommandForTest("stop", "--persistence-root", requestedRoot)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if serviceBackendSawAction(backend.calls, serviceActionStop) {
			t.Fatalf("stop must not run against a registration for a different root; calls=%+v", backend.calls)
		}
	})

	t.Run("restart_if_installed_noops_for_foreign_registration", func(t *testing.T) {
		backend := &stubServiceBackend{status: foreignRegistration}
		withServiceCommandTestBackend(t, backend)
		withServiceCommandTestSpecRoot(t, requestedRoot)

		stdout, stderr, code := runServiceCommandForTest("restart", "--if-installed", "--persistence-root", requestedRoot)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if stdout != "" || serviceBackendSawAction(backend.calls, serviceActionRestart) || serviceBackendSawAction(backend.calls, serviceActionInstall) {
			t.Fatalf("stdout=%q calls=%+v, want quiet no-op without mutation", stdout, backend.calls)
		}
	})

	t.Run("stop_allows_matching_registration", func(t *testing.T) {
		backend := &stubServiceBackend{status: serviceStatus{
			Installed: true,
			Loaded:    true,
			Running:   true,
			Command:   []string{"/usr/local/bin/kent", "serve", "--persistence-root", requestedRoot},
		}}
		withServiceCommandTestBackend(t, backend)
		withServiceCommandTestSpecRoot(t, requestedRoot)

		_, stderr, code := runServiceCommandForTest("stop", "--persistence-root", requestedRoot)
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !serviceBackendSawAction(backend.calls, serviceActionStop) {
			t.Fatalf("backend calls = %+v, want stop", backend.calls)
		}
	})
}

func TestServiceRestartIfInstalledMissingServiceIsQuietNoOp(t *testing.T) {
	backend := &stubServiceBackend{status: serviceStatus{Installed: false}}
	withServiceCommandTestBackend(t, backend)

	stdout, stderr, code := runServiceCommandForTest("restart", "--if-installed")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stdout != "" || serviceBackendSawAction(backend.calls, serviceActionRestart) || serviceBackendSawAction(backend.calls, serviceActionInstall) {
		t.Fatalf("stdout=%q calls=%+v, want quiet no-op without mutation", stdout, backend.calls)
	}
}

func TestServiceRestartIfInstalledRefreshesRegistration(t *testing.T) {
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}}
	withServiceCommandTestBackend(t, backend)

	_, stderr, code := runServiceCommandForTest("restart", "--if-installed")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !backend.installForce || !backend.installStart {
		t.Fatalf("refresh flags force=%v start=%v, want force true start true", backend.installForce, backend.installStart)
	}
	if serviceBackendSawAction(backend.calls, serviceActionRestart) {
		t.Fatalf("restart should refresh by reinstalling the registration; calls=%+v", backend.calls)
	}
}

func TestServiceRestartIfInstalledStopsWhenRefreshFails(t *testing.T) {
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}, installErr: errors.New("install failed")}
	withServiceCommandTestBackend(t, backend)

	_, _, code := runServiceCommandForTest("restart", "--if-installed")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !serviceBackendSawAction(backend.calls, serviceActionInstall) {
		t.Fatalf("backend calls = %+v, want refresh install attempt", backend.calls)
	}
	if serviceBackendSawAction(backend.calls, serviceActionRestart) {
		t.Fatalf("restart should not run after refresh install failure; calls=%+v", backend.calls)
	}
}

func TestServiceActionErrorReturnsOne(t *testing.T) {
	backend := &stubServiceBackend{err: errors.New("boom")}
	withServiceCommandTestBackend(t, backend)

	_, _, code := runServiceCommandForTest("status", "--json")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

func TestServiceInstallRejectsUnmanagedRunningServer(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: false}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	_, _, code := runServiceCommandForTest("install")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if serviceBackendSawAction(backend.calls, serviceActionInstall) {
		t.Fatalf("install should not mutate when a manual server owns the endpoint; calls=%+v", backend.calls)
	}
}

func TestServiceInstallAllowsHealthyServerOwnedByLoadedService(t *testing.T) {
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
	backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 123}}
	withServiceCommandTestBackendEndpoint(t, backend, server.URL)

	_, stderr, code := runServiceCommandForTest("install", "--force")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if !backend.installForce || !backend.installStart {
		t.Fatalf("install flags force=%v start=%v, want force true start true", backend.installForce, backend.installStart)
	}
}

func TestServiceLifecycleOwnershipBoundaries(t *testing.T) {
	t.Run("start_calls_backend_without_ownership_conflict", func(t *testing.T) {
		backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: false}}
		withServiceCommandTestBackend(t, backend)

		_, stderr, code := runServiceCommandForTest("start")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !serviceBackendSawAction(backend.calls, serviceActionStart) {
			t.Fatalf("backend calls = %+v, want start", backend.calls)
		}
	})

	t.Run("start_rejects_unmanaged_healthy_listener", func(t *testing.T) {
		server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
		backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}}
		withServiceCommandTestBackendEndpoint(t, backend, server.URL)

		_, _, code := runServiceCommandForTest("start")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if serviceBackendSawAction(backend.calls, serviceActionStart) {
			t.Fatalf("start should not mutate when a manual server owns the endpoint; calls=%+v", backend.calls)
		}
	})

	t.Run("restart_allows_loaded_service_before_pid_visible", func(t *testing.T) {
		server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
		backend := &stubServiceBackend{status: serviceStatus{
			Installed: true,
			Loaded:    true,
			Running:   true,
			Command:   []string{"/usr/local/bin/kent", "serve"},
		}}
		withServiceCommandTestBackendEndpoint(t, backend, server.URL)

		_, stderr, code := runServiceCommandForTest("restart")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !serviceBackendSawAction(backend.calls, serviceActionRestart) {
			t.Fatalf("backend calls = %+v, want restart", backend.calls)
		}
	})

	t.Run("restart_allows_unloaded_installed_service_recovery", func(t *testing.T) {
		server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
		backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: false, Running: false}}
		withServiceCommandTestBackendEndpoint(t, backend, server.URL)

		_, stderr, code := runServiceCommandForTest("restart")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !serviceBackendSawAction(backend.calls, serviceActionRestart) {
			t.Fatalf("backend calls = %+v, want restart", backend.calls)
		}
	})

	t.Run("restart_allows_loaded_service_with_stale_running_state", func(t *testing.T) {
		server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
		backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: false}}
		withServiceCommandTestBackendEndpoint(t, backend, server.URL)

		_, stderr, code := runServiceCommandForTest("restart")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !serviceBackendSawAction(backend.calls, serviceActionRestart) {
			t.Fatalf("backend calls = %+v, want restart", backend.calls)
		}
	})

	t.Run("restart_allows_owned_service_with_unhealthy_health_probe", func(t *testing.T) {
		server := newServiceHealthTestServer(t, `{"status":"starting"}`, http.StatusServiceUnavailable)
		backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true}}
		withServiceCommandTestBackendEndpoint(t, backend, server.URL)

		_, stderr, code := runServiceCommandForTest("restart")
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
		}
		if !serviceBackendSawAction(backend.calls, serviceActionRestart) {
			t.Fatalf("backend calls = %+v, want restart", backend.calls)
		}
	})

	t.Run("restart_rejects_health_pid_mismatch", func(t *testing.T) {
		server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
		backend := &stubServiceBackend{status: serviceStatus{Installed: true, Loaded: true, Running: true, PID: 999}}
		withServiceCommandTestBackendEndpoint(t, backend, server.URL)

		_, _, code := runServiceCommandForTest("restart")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if serviceBackendSawAction(backend.calls, serviceActionRestart) {
			t.Fatalf("restart should not mutate when health PID disagrees with service PID; calls=%+v", backend.calls)
		}
	})

	t.Run("restart_rejects_health_when_registered_command_differs", func(t *testing.T) {
		server := newServiceHealthTestServer(t, `{"status":"ok","pid":123}`)
		backend := &stubServiceBackend{status: serviceStatus{
			Installed: true,
			Loaded:    true,
			Running:   true,
			Command:   []string{"/other/kent", "serve"},
		}}
		withServiceCommandTestBackendEndpoint(t, backend, server.URL)

		_, _, code := runServiceCommandForTest("restart")
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if serviceBackendSawAction(backend.calls, serviceActionRestart) {
			t.Fatalf("restart should not mutate when command proof does not match service spec; calls=%+v", backend.calls)
		}
	})
}

func TestServiceHelpSmoke(t *testing.T) {
	stdout, stderr, code := runServiceCommandForTest("restart", "--help")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if strings.TrimSpace(stdout)+strings.TrimSpace(stderr) == "" {
		t.Fatalf("help output is empty")
	}
}

func TestRootMismatchAllowsDefaultRootWithUnpinnedRegistration(t *testing.T) {
	defaultRoot, err := config.NormalizePersistenceRoot(config.DefaultPersistence)
	if err != nil {
		t.Fatalf("normalize default root: %v", err)
	}
	explicitRoot := filepath.Join(t.TempDir(), "explicit")

	tests := []struct {
		name    string
		command []string
		root    string
		wantErr bool
	}{
		{name: "unreadable_default", command: nil, root: defaultRoot, wantErr: false},
		{name: "unpinned_default", command: []string{"/usr/local/bin/kent", "serve"}, root: defaultRoot, wantErr: false},
		{name: "unreadable_explicit", command: nil, root: explicitRoot, wantErr: true},
		{name: "unpinned_explicit", command: []string{"/usr/local/bin/kent", "serve"}, root: explicitRoot, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := serviceStatus{Installed: true, Command: tt.command}
			spec := serviceSpec{Config: config.App{PersistenceRoot: tt.root}}
			err := rootMismatchError(status, spec)
			if tt.wantErr && err == nil {
				t.Fatalf("rootMismatchError(command=%+v, root=%q) = nil, want mismatch", tt.command, tt.root)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("rootMismatchError(command=%+v, root=%q) = %v, want nil", tt.command, tt.root, err)
			}
		})
	}
}

func serviceBackendSawAction(calls []serviceAction, want serviceAction) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
