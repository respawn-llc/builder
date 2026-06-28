//go:build !windows

package main

import "os/exec"

// configureNoWindow is a no-op off Windows; only Windows console subprocesses
// would otherwise flash a window.
func configureNoWindow(_ *exec.Cmd) {}
