//go:build windows

package sessionruntime

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

func prepareScriptCommand(*exec.Cmd) {}

func terminateScriptProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return taskkillScriptProcessTree(process.Pid, false)
}

func killScriptProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	if err := taskkillScriptProcessTree(process.Pid, true); err != nil {
		return process.Kill()
	}
	return nil
}

func taskkillScriptProcessTree(pid int, force bool) error {
	args := []string{"/PID", strconv.Itoa(pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	if output, err := exec.Command("taskkill", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("taskkill process tree: %w: %s", err, output)
	}
	return nil
}
