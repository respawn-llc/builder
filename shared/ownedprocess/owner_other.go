//go:build !(aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris || windows)

package ownedprocess

import (
	"os"
	"os/exec"
)

func prepareCommand(*exec.Cmd) {}

func terminateTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}

func killTree(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}

func waitTree(*os.Process) error {
	return nil
}
