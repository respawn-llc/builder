//go:build windows

package workflowrunner

import (
	"os"
	"os/exec"
)

func prepareScriptCommand(*exec.Cmd) {}

func terminateScriptProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}

func killScriptProcess(process *os.Process) error {
	if process == nil {
		return nil
	}
	return process.Kill()
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
