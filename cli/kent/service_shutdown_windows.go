//go:build windows

package main

import (
	"context"
	brand "core/shared/config"
	"os"

	"golang.org/x/sys/windows"
)

const shutdownEventEnvVar = brand.EnvPrefix + "SERVICE_SHUTDOWN_EVENT"

func installServiceShutdownTrigger(ctx context.Context) context.Context {
	name := os.Getenv(shutdownEventEnvVar)

	_ = os.Unsetenv(shutdownEventEnvVar)
	if name == "" {
		return ctx
	}
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return ctx
	}
	event, err := windows.OpenEvent(windows.SYNCHRONIZE, false, namePtr)
	if err != nil {
		return ctx
	}
	derived, cancel := context.WithCancel(ctx)
	go func() {
		_, _ = windows.WaitForSingleObject(event, windows.INFINITE)
		cancel()
	}()
	return derived
}
