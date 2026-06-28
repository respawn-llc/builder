//go:build !windows

package main

import "os/exec"

func configureNoWindow(_ *exec.Cmd) {}
