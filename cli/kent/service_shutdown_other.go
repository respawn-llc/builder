//go:build !windows

package main

import "context"

// installServiceShutdownTrigger is a no-op off Windows; launchd/systemd stop the
// server with SIGTERM directly, which the signal context already handles.
func installServiceShutdownTrigger(ctx context.Context) context.Context {
	return ctx
}
