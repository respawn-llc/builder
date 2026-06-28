//go:build !windows

package main

// redirectServiceLogs is a no-op off Windows; launchd/systemd redirect the
// server's stdout/stderr to log files directly, and process launches stay within
// one session so handle inheritance is unaffected.
func redirectServiceLogs() {}
