//go:build windows

package main

import (
	brand "core/shared/config"
	"os"

	"golang.org/x/sys/windows"
)

const (
	stdoutLogEnvVar = brand.EnvPrefix + "SERVICE_STDOUT_LOG"
	stderrLogEnvVar = brand.EnvPrefix + "SERVICE_STDERR_LOG"
)

func redirectServiceLogs() {
	redirectStdStream(stdoutLogEnvVar, &os.Stdout, windows.STD_OUTPUT_HANDLE)
	redirectStdStream(stderrLogEnvVar, &os.Stderr, windows.STD_ERROR_HANDLE)
}

func redirectStdStream(envVar string, target **os.File, stdHandle uint32) {
	path := os.Getenv(envVar)

	_ = os.Unsetenv(envVar)
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
