//go:build windows

package main

import (
	brand "core/shared/config"
	"os"

	"golang.org/x/sys/windows"
)

// stdoutLogEnvVar and stderrLogEnvVar carry the service log file paths from the
// supervisor to the server it launches. CreateProcessAsUser cannot inherit std
// handles across Terminal Services sessions (the supervisor runs in session 0,
// the server in the user's console session), so instead of redirecting via
// inherited handles the server opens these files itself. They are set only on
// the supervised child's environment.
const (
	stdoutLogEnvVar = brand.EnvPrefix + "SERVICE_STDOUT_LOG"
	stderrLogEnvVar = brand.EnvPrefix + "SERVICE_STDERR_LOG"
)

// redirectServiceLogs points this process's stdout/stderr at the service log
// files when launched by the service supervisor (the env vars are set). It runs
// before the command dispatcher reads os.Stdout/os.Stderr, and also updates the
// OS std handles so raw writes and child processes (shell tools) are captured.
// A no-op for a normally-run process where the env vars are unset.
func redirectServiceLogs() {
	redirectStdStream(stdoutLogEnvVar, &os.Stdout, windows.STD_OUTPUT_HANDLE)
	redirectStdStream(stderrLogEnvVar, &os.Stderr, windows.STD_ERROR_HANDLE)
}

func redirectStdStream(envVar string, target **os.File, stdHandle uint32) {
	path := os.Getenv(envVar)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	*target = f
	_ = windows.SetStdHandle(stdHandle, windows.Handle(f.Fd()))
}
