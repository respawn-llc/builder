//go:build aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package ownedprocess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateTree(process *os.Process) error {
	return signalProcessGroup(process, syscall.SIGTERM)
}

func killTree(process *os.Process) error {
	return signalProcessGroup(process, syscall.SIGKILL)
}

func waitTree(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return nil
	}
	for {
		err := syscall.Kill(-process.Pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func signalProcessGroup(process *os.Process, signal syscall.Signal) error {
	if process == nil || process.Pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-process.Pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
