//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newLaunchdHealthTestServer(t *testing.T, health func() (string, int)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		body, code := health()
		if code != http.StatusOK {
			http.Error(w, body, code)
			return
		}
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestLaunchdInstallWithoutStartWritesPlistWithoutLoading(t *testing.T) {
	spec := newLaunchdTestSpec(t)
	calls := captureLaunchdServiceCommands(t, func(context.Context, string, ...string) (serviceCommandResult, error) {
		return serviceCommandResult{}, errors.New("unexpected launchctl call")
	})

	if err := (launchdServiceBackend{}).Install(context.Background(), spec, true, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if got := readLaunchdRegisteredCommand(mustLaunchdPlistPath(t)); !stringSlicesEqual(got, serviceCommand(spec)) {
		t.Fatalf("registered command = %#v, want %#v", got, serviceCommand(spec))
	}
	if len(calls.commands()) != 0 {
		t.Fatalf("launchctl calls = %#v, want none", calls.commands())
	}
}

func TestLaunchdStartBootstrapsUnloadedServiceWithoutKickstart(t *testing.T) {
	spec := newLaunchdTestSpec(t)
	path := writeLaunchdTestPlist(t, spec)
	calls := captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			return launchdMissingResult(name, args)
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	if err := (launchdServiceBackend{}).Start(context.Background(), spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	if calls.count("bootstrap") != 1 || calls.count("kickstart") != 0 {
		t.Fatalf("launchctl calls = %#v, want one bootstrap and no kickstart", calls.commands())
	}
}

func TestLaunchdReloadWaitsForBootoutEvictionBeforeBootstrap(t *testing.T) {
	withFastLaunchdShutdownPolling(t)
	bootstrapped := false
	oldServerStopped := false
	server := newLaunchdHealthTestServer(t, func() (string, int) {
		if bootstrapped {
			return `{"status":"ok","pid":77}`, http.StatusOK
		}
		if oldServerStopped {
			return "stopped", http.StatusServiceUnavailable
		}
		return `{"status":"ok","pid":42}`, http.StatusOK
	})
	spec := newLaunchdTestSpec(t)
	spec.Endpoint = server.URL
	path := writeLaunchdTestPlist(t, spec)
	postBootoutPrints := 0
	var calls *launchdCommandRecorder
	calls = captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			if bootstrapped {
				return serviceCommandResult{Stdout: launchdPrintOutput(77, serviceCommand(spec))}, nil
			}
			if calls.count("bootout") == 0 {
				return serviceCommandResult{Stdout: launchdPrintOutput(42, serviceCommand(spec))}, nil
			}
			postBootoutPrints++
			if postBootoutPrints <= 2 {
				return serviceCommandResult{Stdout: launchdPrintOutput(42, serviceCommand(spec))}, nil
			}
			oldServerStopped = true
			return launchdMissingResult(name, args)
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			return serviceCommandResult{}, nil
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			if postBootoutPrints <= 2 {
				t.Fatalf("bootstrap ran before launchd stopped reporting the old label loaded")
			}
			bootstrapped = true
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	if err := (launchdServiceBackend{}).Restart(context.Background(), spec); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if !calls.before("bootout", "bootstrap") {
		t.Fatalf("launchctl calls = %#v, want bootout before bootstrap", calls.commands())
	}
}

func TestLaunchdReloadStopsUnloadedHealthyServerBeforeBootstrap(t *testing.T) {
	withFastLaunchdShutdownPolling(t)
	serverRequests := 0
	serverStopped := false
	bootstrapped := false
	server := newLaunchdHealthTestServer(t, func() (string, int) {
		serverRequests++
		if serverStopped && !bootstrapped {
			return "stopped", http.StatusServiceUnavailable
		}
		if bootstrapped {
			return `{"status":"ok","pid":77}`, http.StatusOK
		}
		return `{"status":"ok","pid":42}`, http.StatusOK
	})
	spec := newLaunchdTestSpec(t)
	spec.Endpoint = server.URL
	path := writeLaunchdTestPlist(t, spec)
	signaledPID := replaceLaunchdProcessHooks(t, func(pid int) {
		serverStopped = true
	})
	calls := captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			if bootstrapped {
				return serviceCommandResult{Stdout: launchdPrintOutput(77, serviceCommand(spec))}, nil
			}
			return launchdMissingResult(name, args)
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			if !serverStopped || serverRequests < 2 {
				t.Fatalf("bootstrap ran before old healthy server stopped")
			}
			bootstrapped = true
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	if err := reloadLaunchdService(context.Background(), spec, path); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if *signaledPID != 42 {
		t.Fatalf("signaled pid = %d, want 42", *signaledPID)
	}
	if calls.count("bootstrap") != 1 {
		t.Fatalf("launchctl calls = %#v, want one bootstrap", calls.commands())
	}
}

func TestLaunchdBootstrapTransientRecoveryBootsOutBeforeRetry(t *testing.T) {
	spec := newLaunchdTestSpec(t)
	path := writeLaunchdTestPlist(t, spec)
	var calls *launchdCommandRecorder
	calls = captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			if calls.count("bootout") > 0 {
				return launchdMissingResult(name, args)
			}
			return serviceCommandResult{Stdout: launchdPrintOutput(42, serviceCommand(spec))}, nil
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			if calls.count("bootstrap") == 1 {
				return serviceCommandResult{Stderr: "Bootstrap failed: 5: Input/output error", Code: 5}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "Bootstrap failed: 5: Input/output error", Code: 5}}
			}
			return serviceCommandResult{}, nil
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	if err := bootstrapLaunchdService(context.Background(), spec, path); err != nil {
		t.Fatalf("bootstrap recovery: %v", err)
	}
	if calls.count("bootstrap") != 2 || !calls.between("bootstrap", "bootout", "bootstrap") {
		t.Fatalf("launchctl calls = %#v, want failed bootstrap, bootout, retry bootstrap", calls.commands())
	}
}

func TestLaunchdBootstrapRecoverySurfacesBootoutFailure(t *testing.T) {
	spec := newLaunchdTestSpec(t)
	path := writeLaunchdTestPlist(t, spec)
	calls := captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			return serviceCommandResult{Stderr: "Bootstrap failed: 5: Input/output error", Code: 5}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "Bootstrap failed: 5: Input/output error", Code: 5}}
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			return serviceCommandResult{Stderr: "bootout failed", Code: 5}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "bootout failed", Code: 5}}
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	err := bootstrapLaunchdService(context.Background(), spec, path)
	if err == nil || !strings.Contains(err.Error(), "bootout failed") {
		t.Fatalf("bootstrap recovery error = %v, want bootout failure", err)
	}
	if calls.count("bootstrap") != 1 || calls.count("bootout") != 1 {
		t.Fatalf("launchctl calls = %#v, want one failed bootstrap and one bootout", calls.commands())
	}
}

func TestLaunchdStartDoesNotHideNonTransientBootstrapError(t *testing.T) {
	spec := newLaunchdTestSpec(t)
	path := writeLaunchdTestPlist(t, spec)
	calls := captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			return launchdMissingResult(name, args)
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			return serviceCommandResult{Stderr: "invalid property list", Code: 78}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "invalid property list", Code: 78}}
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	err := (launchdServiceBackend{}).Start(context.Background(), spec)
	var cmdErr serviceCommandError
	if !errors.As(err, &cmdErr) || cmdErr.Result.Code != 78 {
		t.Fatalf("start error = %v, want surfaced non-transient bootstrap command error", err)
	}
	if calls.count("bootout") != 0 {
		t.Fatalf("launchctl calls = %#v, want no stale-service recovery", calls.commands())
	}
}

func TestLaunchdReloadDoesNotBootstrapWhileOldServerStillRunning(t *testing.T) {
	originalTimeout := launchdServiceShutdownTimeout
	originalInterval := launchdServiceShutdownPollInterval
	launchdServiceShutdownTimeout = time.Millisecond
	launchdServiceShutdownPollInterval = time.Millisecond
	t.Cleanup(func() {
		launchdServiceShutdownTimeout = originalTimeout
		launchdServiceShutdownPollInterval = originalInterval
	})
	server := newServiceHealthTestServer(t, `{"status":"ok","pid":42}`)
	spec := newLaunchdTestSpec(t)
	spec.Endpoint = server.URL
	path := writeLaunchdTestPlist(t, spec)
	replaceStuckLaunchdProcessHooks(t)
	calls := captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			return serviceCommandResult{Stdout: launchdPrintOutput(42, serviceCommand(spec))}, nil
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	err := reloadLaunchdService(context.Background(), spec, path)
	if !errors.Is(err, errLaunchdOldServerNotExited) || !errors.Is(err, errLaunchdServerProcessNotExited) {
		t.Fatalf("reload error = %v, want old-server and process-exit failures", err)
	}
	if calls.count("bootstrap") != 0 {
		t.Fatalf("launchctl calls = %#v, want no bootstrap while old server owns the port", calls.commands())
	}
}

func TestLaunchdStatusUsesLoadedCommandAndRunningStateFromPrint(t *testing.T) {
	spec := newLaunchdTestSpec(t)
	writeLaunchdTestPlist(t, spec)
	captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		if launchdCommandKey(name, args...) != "launchctl\x00print\x00"+launchdServiceTarget() {
			return serviceCommandResult{}, errors.New("unexpected command")
		}
		return serviceCommandResult{Stdout: "state = running\narguments = {\n\t/usr/local/bin/kent\n\tserve\n}\n"}, nil
	})

	status, err := (launchdServiceBackend{}).Status(context.Background(), spec)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Installed || !status.Loaded || !status.Running {
		t.Fatalf("status = %+v, want installed loaded running", status)
	}
	if !stringSlicesEqual(status.Command, []string{"/usr/local/bin/kent", "serve"}) {
		t.Fatalf("command = %#v, want loaded ProgramArguments", status.Command)
	}
}

func newLaunchdTestSpec(t *testing.T) serviceSpec {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return testLaunchdServiceSpec(t)
}

func writeLaunchdTestPlist(t *testing.T, spec serviceSpec) string {
	t.Helper()
	path := mustLaunchdPlistPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir launch agents: %v", err)
	}
	if err := os.WriteFile(path, []byte(renderLaunchdPlist(spec)), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	return path
}

func writeLaunchdTestPlistWithCommand(t *testing.T, spec serviceSpec, command []string) string {
	t.Helper()
	custom := spec
	if len(command) > 0 {
		custom.Executable = command[0]
		custom.Arguments = append([]string(nil), command[1:]...)
	}
	return writeLaunchdTestPlist(t, custom)
}

func launchdPrintOutput(pid int, command []string) string {
	var builder strings.Builder
	builder.WriteString("state = running\n")
	builder.WriteString("pid = ")
	builder.WriteString(strconv.Itoa(pid))
	builder.WriteString("\n")
	builder.WriteString("arguments = {\n")
	for _, arg := range command {
		builder.WriteString(arg)
		builder.WriteString("\n")
	}
	builder.WriteString("}\n")
	return builder.String()
}

func testLaunchdServiceSpec(t *testing.T) serviceSpec {
	t.Helper()
	root := t.TempDir()
	return serviceSpec{
		Executable:    "/usr/local/bin/kent",
		Arguments:     []string{"serve"},
		LogDir:        filepath.Join(root, "logs"),
		StdoutLogPath: filepath.Join(root, "logs", "server.log"),
		StderrLogPath: filepath.Join(root, "logs", "server.err.log"),
		Endpoint:      "http://127.0.0.1:1",
	}
}

func withFastLaunchdShutdownPolling(t *testing.T) {
	t.Helper()
	originalTimeout := launchdServiceShutdownTimeout
	originalInterval := launchdServiceShutdownPollInterval
	launchdServiceShutdownTimeout = 100 * time.Millisecond
	launchdServiceShutdownPollInterval = time.Millisecond
	t.Cleanup(func() {
		launchdServiceShutdownTimeout = originalTimeout
		launchdServiceShutdownPollInterval = originalInterval
	})
}

type launchdCommandRecorder struct {
	calls []serviceCommandInvocation
}

type serviceCommandInvocation struct {
	Name string
	Args []string
}

func captureLaunchdServiceCommands(t *testing.T, fn func(context.Context, string, ...string) (serviceCommandResult, error)) *launchdCommandRecorder {
	t.Helper()
	original := runServiceCommand
	recorder := &launchdCommandRecorder{}
	runServiceCommand = func(ctx context.Context, name string, args ...string) (serviceCommandResult, error) {
		recorder.calls = append(recorder.calls, serviceCommandInvocation{Name: name, Args: append([]string(nil), args...)})
		return fn(ctx, name, args...)
	}
	t.Cleanup(func() { runServiceCommand = original })
	return recorder
}

func (r *launchdCommandRecorder) commands() []serviceCommandInvocation {
	return append([]serviceCommandInvocation(nil), r.calls...)
}

func (r *launchdCommandRecorder) count(action string) int {
	count := 0
	for _, call := range r.calls {
		if call.Name == "launchctl" && len(call.Args) > 0 && call.Args[0] == action {
			count++
		}
	}
	return count
}

func (r *launchdCommandRecorder) before(first string, second string) bool {
	return r.index(first, 0) >= 0 && r.index(first, 0) < r.index(second, 0)
}

func (r *launchdCommandRecorder) between(first string, middle string, second string) bool {
	firstIndex := r.index(first, 0)
	middleIndex := r.index(middle, firstIndex+1)
	secondIndex := r.index(second, middleIndex+1)
	return firstIndex >= 0 && middleIndex > firstIndex && secondIndex > middleIndex
}

func (r *launchdCommandRecorder) index(action string, start int) int {
	for i := start; i < len(r.calls); i++ {
		call := r.calls[i]
		if call.Name == "launchctl" && len(call.Args) > 0 && call.Args[0] == action {
			return i
		}
	}
	return -1
}

func replaceLaunchdProcessHooks(t *testing.T, onSignal func(int)) *int {
	t.Helper()
	signaledPID := 0
	originalSignal := signalLaunchdServiceProcess
	originalKill := killLaunchdServiceProcess
	originalAlive := launchdServiceProcessAlive
	processAlive := true
	signalLaunchdServiceProcess = func(pid int) error {
		signaledPID = pid
		if onSignal != nil {
			onSignal(pid)
		}
		return nil
	}
	killLaunchdServiceProcess = func(pid int) error {
		processAlive = false
		return nil
	}
	launchdServiceProcessAlive = func(pid int) (bool, error) {
		return processAlive, nil
	}
	t.Cleanup(func() {
		signalLaunchdServiceProcess = originalSignal
		killLaunchdServiceProcess = originalKill
		launchdServiceProcessAlive = originalAlive
	})
	return &signaledPID
}

func replaceStuckLaunchdProcessHooks(t *testing.T) {
	t.Helper()
	originalSignal := signalLaunchdServiceProcess
	originalKill := killLaunchdServiceProcess
	originalAlive := launchdServiceProcessAlive
	signalLaunchdServiceProcess = func(pid int) error { return nil }
	killLaunchdServiceProcess = func(pid int) error { return nil }
	launchdServiceProcessAlive = func(pid int) (bool, error) { return true, nil }
	t.Cleanup(func() {
		signalLaunchdServiceProcess = originalSignal
		killLaunchdServiceProcess = originalKill
		launchdServiceProcessAlive = originalAlive
	})
}

func launchdCommandKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func launchdMissingResult(name string, args []string) (serviceCommandResult, error) {
	result := serviceCommandResult{Stderr: "not found", Code: 113}
	return result, serviceCommandError{Name: name, Args: args, Result: result}
}

func launchdDomainTarget() string {
	return "gui/" + currentUIDText()
}

func launchdServiceTarget() string {
	return launchdDomainTarget() + "/" + serviceLaunchdLabel
}

func currentUIDText() string {
	return strconv.Itoa(os.Getuid())
}

func mustLaunchdPlistPath(t *testing.T) string {
	t.Helper()
	path, err := launchdPlistPath()
	if err != nil {
		t.Fatalf("launchd plist path: %v", err)
	}
	return path
}

func stringSlicesEqual(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
