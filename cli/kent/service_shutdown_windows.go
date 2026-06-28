//go:build windows

package main

import (
	"context"
	brand "core/shared/config"
	"os"

	"golang.org/x/sys/windows"
)

// shutdownEventEnvVar carries the name of the graceful-stop event from the
// service supervisor to the server it launches. It is set only on the supervised
// child's environment, so a normally-run `serve` never sees it.
const shutdownEventEnvVar = brand.EnvPrefix + "SERVICE_SHUTDOWN_EVENT"

// installServiceShutdownTrigger lets the LocalSystem service supervisor stop this
// server gracefully across the session boundary. A session-0 service cannot send
// a console Ctrl event to the windowless server in the user session, so instead
// the supervisor signals a named event (created by it, opened here) whose name it
// passes in shutdownEventEnvVar. When the event fires the returned context is
// cancelled, driving the exact same graceful shutdown path as an interrupt. When
// the server is not run by the supervisor (env var unset or the event cannot be
// opened) the original context is returned unchanged.
func installServiceShutdownTrigger(ctx context.Context) context.Context {
	name := os.Getenv(shutdownEventEnvVar)
	// Consume the variable so commands spawned by shell tools (which build their
	// environment from os.Environ()) do not inherit this service-only marker.
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
