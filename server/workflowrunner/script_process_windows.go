//go:build windows

package workflowrunner

import (
	"os"
	"os/exec"
	"strconv"
)

func prepareScriptCommand(*exec.Cmd) {}

func terminateScriptProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return taskkillProcessTree(process.Pid, false)
}

func killScriptProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	if err := taskkillProcessTree(process.Pid, true); err != nil {
		return process.Kill()
	}
	return nil
}

func taskkillProcessTree(pid int, force bool) error {
	args := []string{"/PID", strconv.Itoa(pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	return exec.Command("taskkill", args...).Run()
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if code := exitErr.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}
