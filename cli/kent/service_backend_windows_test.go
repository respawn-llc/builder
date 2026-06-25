//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"core/shared/config"
)

func TestWindowsInstallWithoutForceRejectsExistingDifferentScript(t *testing.T) {
	spec := windowsServiceTestSpec(t)
	if err := os.MkdirAll(filepath.Dir(windowsTaskScriptPath(spec)), 0o755); err != nil {
		t.Fatalf("mkdir task script dir: %v", err)
	}
	if err := os.WriteFile(windowsTaskScriptPath(spec), []byte("old script"), 0o644); err != nil {
		t.Fatalf("write existing task script: %v", err)
	}
	calls := captureWindowsServiceCommands(t, func(context.Context, string, ...string) (serviceCommandResult, error) {
		return serviceCommandResult{}, errors.New("unexpected command")
	})

	err := (scheduledTaskServiceBackend{}).Install(context.Background(), spec, false, false)
	if err == nil {
		t.Fatal("expected existing script rejection")
	}
	if string(mustReadFile(t, windowsTaskScriptPath(spec))) != "old script" {
		t.Fatal("expected existing script to remain unchanged")
	}
	if len(calls.commands()) != 0 {
		t.Fatalf("commands = %+v, want none", calls.commands())
	}
}

func TestWindowsInstallRewritesOrphanScriptAndRegistersTask(t *testing.T) {
	spec := windowsServiceTestSpec(t)
	if err := os.MkdirAll(filepath.Dir(windowsTaskScriptPath(spec)), 0o755); err != nil {
		t.Fatalf("mkdir task script dir: %v", err)
	}
	if err := os.WriteFile(windowsTaskScriptPath(spec), []byte("old script"), 0o644); err != nil {
		t.Fatalf("write existing task script: %v", err)
	}
	calls := captureWindowsServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		if name != "schtasks" {
			return serviceCommandResult{}, errors.New("unexpected command")
		}
		if len(args) > 0 && args[0] == "/Query" {
			return serviceCommandResult{}, errors.New("task missing")
		}
		return serviceCommandResult{}, nil
	})

	if err := (scheduledTaskServiceBackend{}).Install(context.Background(), spec, false, false); err != nil {
		t.Fatalf("install with orphan script: %v", err)
	}
	if string(mustReadFile(t, windowsTaskScriptPath(spec))) == "old script" {
		t.Fatal("expected orphan script to be rewritten")
	}
	if !calls.saw("schtasks", "/Create") {
		t.Fatalf("commands = %+v, want scheduled task registration", calls.commands())
	}
}

func TestWindowsInstallRemovesStartupFallbackAfterScheduledTaskRegistration(t *testing.T) {
	spec := windowsServiceTestSpec(t)
	if err := os.MkdirAll(filepath.Dir(windowsStartupItemPath()), 0o755); err != nil {
		t.Fatalf("mkdir startup dir: %v", err)
	}
	if err := os.WriteFile(windowsStartupItemPath(), []byte("fallback"), 0o644); err != nil {
		t.Fatalf("write startup item: %v", err)
	}
	calls := captureWindowsServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		if name != "schtasks" {
			return serviceCommandResult{}, errors.New("unexpected command")
		}
		if len(args) > 0 && args[0] == "/Query" {
			return serviceCommandResult{}, errors.New("task missing")
		}
		return serviceCommandResult{}, nil
	})

	if err := (scheduledTaskServiceBackend{}).Install(context.Background(), spec, true, false); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(windowsStartupItemPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup fallback stat err = %v, want not exist", err)
	}
	if !calls.saw("schtasks", "/Create") {
		t.Fatalf("commands = %+v, want scheduled task registration before fallback removal", calls.commands())
	}
}

func TestWindowsStopStartupFallbackKillsTaskScriptProcess(t *testing.T) {
	spec := windowsServiceTestSpec(t)
	if err := os.MkdirAll(filepath.Dir(windowsStartupItemPath()), 0o755); err != nil {
		t.Fatalf("mkdir startup dir: %v", err)
	}
	if err := os.WriteFile(windowsStartupItemPath(), []byte("launcher"), 0o644); err != nil {
		t.Fatalf("write startup item: %v", err)
	}
	calls := captureWindowsServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch name {
		case "schtasks":
			return serviceCommandResult{}, errors.New("task missing")
		case "powershell":
			return serviceCommandResult{Stdout: "123\r\n"}, nil
		case "taskkill":
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	if err := (scheduledTaskServiceBackend{}).Stop(context.Background(), spec); err != nil {
		t.Fatalf("stop fallback: %v", err)
	}
	if !calls.sawAll("taskkill", "/T", "/F", "/PID", "123") {
		t.Fatalf("commands = %+v, want taskkill for fallback script pid", calls.commands())
	}
}

func TestWindowsStatusReportsRegisteredServerPID(t *testing.T) {
	spec := windowsServiceTestSpec(t)
	if err := os.MkdirAll(filepath.Dir(windowsTaskScriptPath(spec)), 0o755); err != nil {
		t.Fatalf("mkdir task script dir: %v", err)
	}
	if err := os.WriteFile(windowsTaskScriptPath(spec), []byte(renderWindowsTaskScript(spec)), 0o644); err != nil {
		t.Fatalf("write task script: %v", err)
	}
	captureWindowsServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch name {
		case "schtasks":
			return serviceCommandResult{Stdout: "Status: Running\r\n"}, nil
		case "powershell":
			script := strings.Join(args, " ")
			if strings.Contains(script, windowsTaskScriptPath(spec)) {
				return serviceCommandResult{Stdout: "111\r\n"}, nil
			}
			if strings.Contains(script, spec.Executable) {
				return serviceCommandResult{Stdout: "222\r\n"}, nil
			}
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	status, err := (scheduledTaskServiceBackend{}).Status(context.Background(), spec)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Installed || !status.Running || status.PID != 222 {
		t.Fatalf("status = %+v, want running service with registered server PID 222", status)
	}
}

func TestWindowsStatusDoesNotTreatBareServerProcessAsServiceRunning(t *testing.T) {
	spec := windowsServiceTestSpec(t)
	if err := os.MkdirAll(filepath.Dir(windowsTaskScriptPath(spec)), 0o755); err != nil {
		t.Fatalf("mkdir task script dir: %v", err)
	}
	if err := os.WriteFile(windowsTaskScriptPath(spec), []byte(renderWindowsTaskScript(spec)), 0o644); err != nil {
		t.Fatalf("write task script: %v", err)
	}
	captureWindowsServiceCommands(t, func(_ context.Context, name string, args ...string) (serviceCommandResult, error) {
		switch name {
		case "schtasks":
			return serviceCommandResult{Stdout: "Status: Ready\r\n"}, nil
		case "powershell":
			script := strings.Join(args, " ")
			if strings.Contains(script, windowsTaskScriptPath(spec)) {
				return serviceCommandResult{}, nil
			}
			if strings.Contains(script, spec.Executable) {
				return serviceCommandResult{Stdout: "222\r\n"}, nil
			}
			return serviceCommandResult{}, nil
		default:
			return serviceCommandResult{}, errors.New("unexpected command")
		}
	})

	status, err := (scheduledTaskServiceBackend{}).Status(context.Background(), spec)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Running || status.PID != 0 {
		t.Fatalf("status running=%v pid=%d, want stopped with no service PID", status.Running, status.PID)
	}
}

func TestParseWindowsCommandLinePreservesPathBackslashes(t *testing.T) {
	got := parseWindowsCommandLine(`"C:\Users\Nek\AppData\Local\Kent\kent.exe" serve`)
	want := []string{`C:\Users\Nek\AppData\Local\Kent\kent.exe`, "serve"}
	if !windowsStringSlicesEqual(got, want) {
		t.Fatalf("parseWindowsCommandLine = %#v, want %#v", got, want)
	}
}

func windowsServiceTestSpec(t *testing.T) serviceSpec {
	t.Helper()
	temp := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(temp, "AppData", "Roaming"))
	return serviceSpec{
		Config:        config.App{PersistenceRoot: filepath.Join(temp, config.ConfigDirName)},
		Executable:    filepath.Join(temp, "kent.exe"),
		Arguments:     []string{"serve"},
		LogDir:        filepath.Join(temp, config.ConfigDirName, "logs"),
		StdoutLogPath: filepath.Join(temp, config.ConfigDirName, "logs", "server.log"),
		StderrLogPath: filepath.Join(temp, config.ConfigDirName, "logs", "server.err.log"),
		Endpoint:      "http://127.0.0.1:53082",
	}
}

type windowsCommandRecorder struct {
	calls []serviceCommandInvocation
}

type serviceCommandInvocation struct {
	Name string
	Args []string
}

func captureWindowsServiceCommands(t *testing.T, fn func(context.Context, string, ...string) (serviceCommandResult, error)) *windowsCommandRecorder {
	t.Helper()
	original := runServiceCommand
	recorder := &windowsCommandRecorder{}
	runServiceCommand = func(ctx context.Context, name string, args ...string) (serviceCommandResult, error) {
		recorder.calls = append(recorder.calls, serviceCommandInvocation{Name: name, Args: append([]string(nil), args...)})
		return fn(ctx, name, args...)
	}
	t.Cleanup(func() { runServiceCommand = original })
	return recorder
}

func (r *windowsCommandRecorder) commands() []serviceCommandInvocation {
	return append([]serviceCommandInvocation(nil), r.calls...)
}

func (r *windowsCommandRecorder) saw(name string, firstArg string) bool {
	for _, call := range r.calls {
		if call.Name == name && len(call.Args) > 0 && call.Args[0] == firstArg {
			return true
		}
	}
	return false
}

func (r *windowsCommandRecorder) sawAll(values ...string) bool {
	for _, call := range r.calls {
		actual := append([]string{call.Name}, call.Args...)
		if windowsStringSlicesEqual(actual, values) {
			return true
		}
	}
	return false
}

func windowsStringSlicesEqual(a []string, b []string) bool {
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

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}
	return data
}
