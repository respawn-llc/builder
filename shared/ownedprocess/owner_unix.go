//go:build aix || darwin || dragonfly || freebsd || illumos || ios || linux || netbsd || openbsd || solaris

package ownedprocess

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type unixProcessTree struct {
	cmd       *exec.Cmd
	closeOnce sync.Once
	closeErr  error
}

func startProcessTree(cmd *exec.Cmd) (processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &unixProcessTree{cmd: cmd}, nil
}

func (tree *unixProcessTree) Wait() error {
	return tree.cmd.Wait()
}

func (tree *unixProcessTree) Terminate() error {
	return signalProcessGroup(tree.cmd.Process, syscall.SIGTERM)
}

func (tree *unixProcessTree) Kill() error {
	return signalProcessGroup(tree.cmd.Process, syscall.SIGKILL)
}

func (tree *unixProcessTree) Close() error {
	tree.closeOnce.Do(func() {
		tree.closeErr = waitProcessGroup(tree.cmd.Process)
	})
	return tree.closeErr
}

func waitProcessGroup(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return nil
	}
	for {
		err := syscall.Kill(-process.Pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait for owned process group %d to disappear: %w", process.Pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func signalProcessGroup(process *os.Process, signal syscall.Signal) error {
	if process == nil || process.Pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-process.Pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal owned process group %d with %s: %w", process.Pid, signal, err)
	}
	return nil
}
