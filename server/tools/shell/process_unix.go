//go:build !windows

package shell

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func prepareManagedExec(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func deprioritizeManagedProcess(process *os.Process) error {
	if process == nil || process.Pid <= 0 {
		return nil
	}
	if err := syscall.Setpriority(syscall.PRIO_PGRP, process.Pid, 10); err != nil {
		return fmt.Errorf("renice process group %d: %w", process.Pid, err)
	}
	return nil
}

func killManagedProcess(process *os.Process) error {
	descendants, captureErr := captureManagedDescendants(process)
	return errors.Join(captureErr, terminateManagedProcess(process, descendants))
}

func terminateManagedProcess(process *os.Process, descendants []managedProcessIdentity) error {
	if process == nil {
		return nil
	}
	pid := process.Pid
	if pid <= 0 {
		return nil
	}
	descendantErr := signalManagedDescendantPIDs(descendants, syscall.SIGTERM)
	groupErr := signalManagedProcessGroup(process, syscall.SIGTERM, "terminate")
	_ = process.Signal(os.Interrupt)
	return errors.Join(descendantErr, groupErr)
}

func forceKillManagedDescendants(descendants []managedProcessIdentity) error {
	return signalManagedDescendantPIDs(descendants, syscall.SIGKILL)
}

func forceKillManagedRoot(process *os.Process) error {
	if process == nil {
		return nil
	}
	pid := process.Pid
	if pid <= 0 {
		return nil
	}
	return signalManagedProcessGroup(process, syscall.SIGKILL, "kill")
}

func signalManagedProcessGroup(process *os.Process, signal syscall.Signal, action string) error {
	pid := process.Pid
	if err := syscall.Kill(-pid, signal); err != nil && err != syscall.ESRCH {
		probeErr := process.Signal(syscall.Signal(0))
		if errors.Is(probeErr, os.ErrProcessDone) || errors.Is(probeErr, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("%s process group %d: %w", action, pid, err)
	}
	return nil
}

func captureManagedDescendants(process *os.Process) ([]managedProcessIdentity, error) {
	if process == nil || process.Pid <= 0 {
		return nil, nil
	}
	processes, err := managedProcessSnapshot()
	if err != nil {
		return nil, fmt.Errorf("list descendants of process %d: %w", process.Pid, err)
	}
	return descendantProcesses(process.Pid, processes), nil
}

func signalManagedDescendantPIDs(descendants []managedProcessIdentity, signal syscall.Signal) error {
	var signalErrors []error
	for _, descendant := range descendants {
		if err := syscall.Kill(descendant.pid, signal); err != nil && err != syscall.ESRCH {
			signalErrors = append(signalErrors, fmt.Errorf("signal descendant process %d: %w", descendant.pid, err))
		}
	}
	return errors.Join(signalErrors...)
}

func managedDescendantsExited(descendants []managedProcessIdentity) bool {
	living, err := livingManagedDescendantPIDs(descendants)
	if err != nil {
		return false
	}
	return len(living) == 0
}

func livingManagedDescendantPIDs(descendants []managedProcessIdentity) ([]managedProcessIdentity, error) {
	processes, err := managedProcessSnapshot()
	if err != nil {
		return nil, err
	}
	living := make([]managedProcessIdentity, 0, len(descendants))
	for _, descendant := range descendants {
		current, ok := processes[descendant.pid]
		if ok && current.startedAt == descendant.startedAt {
			living = append(living, descendant)
		}
	}
	return living, nil
}

func descendantProcesses(rootPID int, processes map[int]managedProcessSnapshotEntry) []managedProcessIdentity {
	childrenByPID := make(map[int][]int)
	for pid, process := range processes {
		childrenByPID[process.parentPID] = append(childrenByPID[process.parentPID], pid)
	}
	descendants := make([]managedProcessIdentity, 0)
	seen := map[int]bool{rootPID: true}
	var visit func(int)
	visit = func(pid int) {
		for _, childPID := range childrenByPID[pid] {
			if seen[childPID] {
				continue
			}
			seen[childPID] = true
			visit(childPID)
			descendants = append(descendants, managedProcessIdentity{
				pid:       childPID,
				startedAt: processes[childPID].startedAt,
			})
		}
	}
	visit(rootPID)
	return descendants
}

func processExitState(err error) (int, string) {
	if err == nil {
		return 0, "completed"
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return 130, "killed"
	}
	exitCode := exitErr.ExitCode()
	if exitErr.ProcessState != nil {
		if status, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			exitCode = 128 + int(status.Signal())
			if exitCode <= 0 {
				exitCode = 130
			}
			return exitCode, "killed"
		}
	}
	if exitCode == -1 {
		exitCode = 1
	}
	return exitCode, "failed"
}
