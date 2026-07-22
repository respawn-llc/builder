//go:build !windows

package blackbox

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

func requirePTYPlatform() error {
	return nil
}

func configureServerProcessGroup(command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return nil
}

func terminateServerProcessGroup(command *exec.Cmd) error {
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate standalone server process group: %w", err)
	}
	return nil
}

func killServerProcessGroup(command *exec.Cmd) error {
	if err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("force-kill standalone server process group: %w", err)
	}
	return nil
}
