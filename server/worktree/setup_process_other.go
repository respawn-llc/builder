//go:build windows

package worktree

import (
	"os/exec"
	"strconv"
)

func configureSetupCommand(*exec.Cmd) {}

func terminateSetupCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}
