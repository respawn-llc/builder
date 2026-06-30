//go:build darwin

package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLaunchdRestartRefreshesStaleLoadedCommandBeforeBootstrap(t *testing.T) {
	withFastLaunchdShutdownPolling(t)
	spec := newLaunchdTestSpec(t)
	spec.Executable = "/new/kent"
	bootstrapped := false
	server := newLaunchdHealthTestServer(t, func() (string, int) {
		if bootstrapped {
			return `{"status":"ok","pid":77}`, http.StatusOK
		}
		return "stopped", http.StatusServiceUnavailable
	})
	spec.Endpoint = server.URL
	oldCommand := append([]string{"/old/kent"}, spec.Arguments...)
	path := writeLaunchdTestPlistWithCommand(t, spec, oldCommand)
	var calls *launchdCommandRecorder
	calls = captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			if bootstrapped {
				return serviceCommandResult{Stdout: launchdPrintOutput(77, readLaunchdRegisteredCommand(path))}, nil
			}
			if calls.count("bootout") > 0 {
				return serviceCommandResult{Stderr: "not found", Code: 113}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "not found", Code: 113}}
			}
			return serviceCommandResult{Stdout: launchdPrintOutput(42, oldCommand)}, nil
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			return serviceCommandResult{}, nil
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			if command := readLaunchdRegisteredCommand(path); !stringSlicesEqual(command, serviceCommand(spec)) {
				t.Fatalf("bootstrap read command %#v, want refreshed %#v", command, serviceCommand(spec))
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
	status, err := (launchdServiceBackend{}).Status(context.Background(), spec)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !stringSlicesEqual(status.Command, serviceCommand(spec)) {
		t.Fatalf("status command = %#v, want %#v", status.Command, serviceCommand(spec))
	}
	if calls.count("bootstrap") != 1 {
		t.Fatalf("calls = %#v, want one bootstrap", calls.commands())
	}
}

func TestLaunchdRestartIfInstalledRefreshesStaleLoadedCommandBeforeBootstrap(t *testing.T) {
	withFastLaunchdShutdownPolling(t)
	bootstrapped := false
	server := newLaunchdHealthTestServer(t, func() (string, int) {
		if bootstrapped {
			return `{"status":"ok","pid":77}`, http.StatusOK
		}
		return "starting", http.StatusServiceUnavailable
	})
	spec := newLaunchdTestSpec(t)
	spec.Executable = "/new/kent"
	spec.Endpoint = server.URL
	withLaunchdServiceCommandSpec(t, spec)
	oldCommand := append([]string{"/old/kent"}, spec.Arguments...)
	path := writeLaunchdTestPlistWithCommand(t, spec, oldCommand)
	var calls *launchdCommandRecorder
	calls = captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			if bootstrapped {
				return serviceCommandResult{Stdout: launchdPrintOutput(77, readLaunchdRegisteredCommand(path))}, nil
			}
			if calls.count("bootout") > 0 {
				return serviceCommandResult{Stderr: "not found", Code: 113}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "not found", Code: 113}}
			}
			return serviceCommandResult{Stdout: launchdPrintOutput(42, oldCommand)}, nil
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			return serviceCommandResult{}, nil
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			if command := readLaunchdRegisteredCommand(path); !stringSlicesEqual(command, serviceCommand(spec)) {
				t.Fatalf("bootstrap read command %#v, want refreshed %#v", command, serviceCommand(spec))
			}
			bootstrapped = true
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	var stdout strings.Builder
	var stderr strings.Builder
	code := serviceSubcommand([]string{"restart", "--if-installed"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr.String())
	}
	status, err := (launchdServiceBackend{}).Status(context.Background(), spec)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !stringSlicesEqual(status.Command, serviceCommand(spec)) {
		t.Fatalf("status command = %#v, want %#v", status.Command, serviceCommand(spec))
	}
	if calls.count("bootstrap") != 1 {
		t.Fatalf("calls = %#v, want one bootstrap", calls.commands())
	}
	if !strings.Contains(stdout.String(), "Restarted "+serviceDisplayName) {
		t.Fatalf("stdout = %q, want restart confirmation", stdout.String())
	}
}

func TestLaunchdRestartRejectsLoadedCommandSkewAfterBootstrap(t *testing.T) {
	withFastLaunchdShutdownPolling(t)
	bootstrapped := false
	server := newLaunchdHealthTestServer(t, func() (string, int) {
		if bootstrapped {
			return `{"status":"ok","pid":77}`, http.StatusOK
		}
		return "starting", http.StatusServiceUnavailable
	})
	spec := newLaunchdTestSpec(t)
	spec.Executable = "/new/kent"
	spec.Endpoint = server.URL
	path := writeLaunchdTestPlist(t, spec)
	oldCommand := append([]string{"/old/kent"}, spec.Arguments...)
	var calls *launchdCommandRecorder
	calls = captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			if bootstrapped {
				return serviceCommandResult{Stdout: launchdPrintOutput(77, oldCommand)}, nil
			}
			if calls.count("bootout") > 0 {
				return serviceCommandResult{Stderr: "not found", Code: 113}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "not found", Code: 113}}
			}
			return serviceCommandResult{Stdout: launchdPrintOutput(42, oldCommand)}, nil
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			return serviceCommandResult{}, nil
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			bootstrapped = true
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	err := (launchdServiceBackend{}).Restart(context.Background(), spec)
	if err == nil {
		t.Fatal("expected restart to reject loaded command skew")
	}
	if !errors.Is(err, errLaunchdServerNotHealthy) {
		t.Fatalf("error = %v, want startup verification failure", err)
	}
}

func TestLaunchdRestartWaitsForHealthToBelongToNewLaunchdPID(t *testing.T) {
	withFastLaunchdShutdownPolling(t)
	bootout := false
	bootstrapped := false
	postBootstrapHealthRequests := 0
	server := newLaunchdHealthTestServer(t, func() (string, int) {
		switch {
		case bootstrapped:
			postBootstrapHealthRequests++
			if postBootstrapHealthRequests == 1 {
				return `{"status":"ok","pid":42}`, http.StatusOK
			}
			return `{"status":"ok","pid":77}`, http.StatusOK
		case bootout:
			return "stopped", http.StatusServiceUnavailable
		default:
			return `{"status":"ok","pid":42}`, http.StatusOK
		}
	})
	spec := newLaunchdTestSpec(t)
	spec.Endpoint = server.URL
	path := writeLaunchdTestPlist(t, spec)
	var calls *launchdCommandRecorder
	calls = captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			if bootstrapped {
				return serviceCommandResult{Stdout: launchdPrintOutput(77, serviceCommand(spec))}, nil
			}
			if bootout {
				return serviceCommandResult{Stderr: "not found", Code: 113}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "not found", Code: 113}}
			}
			return serviceCommandResult{Stdout: launchdPrintOutput(42, serviceCommand(spec))}, nil
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			bootout = true
			return serviceCommandResult{}, nil
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			bootstrapped = true
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	if err := (launchdServiceBackend{}).Restart(context.Background(), spec); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if postBootstrapHealthRequests < 2 {
		t.Fatalf("post-bootstrap health requests = %d, want retry until health pid belongs to launchd pid 77; calls=%#v", postBootstrapHealthRequests, calls.commands())
	}
}

func TestLaunchdRestartRejectsOldHealthPIDAfterBootstrap(t *testing.T) {
	originalTimeout := launchdServiceShutdownTimeout
	originalInterval := launchdServiceShutdownPollInterval
	launchdServiceShutdownTimeout = time.Millisecond
	launchdServiceShutdownPollInterval = time.Millisecond
	t.Cleanup(func() {
		launchdServiceShutdownTimeout = originalTimeout
		launchdServiceShutdownPollInterval = originalInterval
	})
	bootout := false
	bootstrapped := false
	server := newLaunchdHealthTestServer(t, func() (string, int) {
		switch {
		case bootstrapped:
			return `{"status":"ok","pid":42}`, http.StatusOK
		case bootout:
			return "stopped", http.StatusServiceUnavailable
		default:
			return `{"status":"ok","pid":42}`, http.StatusOK
		}
	})
	spec := newLaunchdTestSpec(t)
	spec.Endpoint = server.URL
	path := writeLaunchdTestPlist(t, spec)
	var calls *launchdCommandRecorder
	calls = captureLaunchdServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch launchdCommandKey(name, args...) {
		case "launchctl\x00print\x00" + launchdServiceTarget():
			if bootstrapped {
				return serviceCommandResult{Stdout: launchdPrintOutput(77, serviceCommand(spec))}, nil
			}
			if bootout {
				return serviceCommandResult{Stderr: "not found", Code: 113}, serviceCommandError{Name: name, Args: args, Result: serviceCommandResult{Stderr: "not found", Code: 113}}
			}
			return serviceCommandResult{Stdout: launchdPrintOutput(42, serviceCommand(spec))}, nil
		case "launchctl\x00bootout\x00" + launchdServiceTarget():
			bootout = true
			return serviceCommandResult{}, nil
		case "launchctl\x00bootstrap\x00" + launchdDomainTarget() + "\x00" + path:
			bootstrapped = true
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	err := (launchdServiceBackend{}).Restart(context.Background(), spec)
	if err == nil {
		t.Fatal("expected restart to reject old endpoint owner")
	}
	if !errors.Is(err, errLaunchdServerNotHealthy) {
		t.Fatalf("error = %v, want startup verification failure", err)
	}
	if calls.count("bootstrap") != 1 {
		t.Fatalf("calls = %#v, want one bootstrap", calls.commands())
	}
}

func withLaunchdServiceCommandSpec(t *testing.T, spec serviceSpec) {
	t.Helper()
	original := loadServiceSpec
	loadServiceSpec = func() (serviceSpec, error) { return spec, nil }
	t.Cleanup(func() { loadServiceSpec = original })
}
