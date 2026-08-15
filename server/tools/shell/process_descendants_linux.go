//go:build linux

package shell

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func managedProcessSnapshot() (map[int]managedProcessSnapshotEntry, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	snapshot := make(map[int]managedProcessSnapshotEntry, len(entries))
	var snapshotErrors []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		process, err := readLinuxProcess(pid)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
				continue
			}
			snapshotErrors = append(snapshotErrors, fmt.Errorf("inspect process %d: %w", pid, err))
			continue
		}
		snapshot[pid] = process
	}
	return snapshot, errors.Join(snapshotErrors...)
}

func readLinuxProcess(pid int) (managedProcessSnapshotEntry, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return managedProcessSnapshotEntry{}, err
	}
	commandEnd := strings.LastIndexByte(string(raw), ')')
	if commandEnd < 0 || commandEnd+2 >= len(raw) {
		return managedProcessSnapshotEntry{}, fmt.Errorf("parse /proc/%d/stat: malformed command field", pid)
	}
	fields := strings.Fields(string(raw[commandEnd+2:]))
	if len(fields) <= 19 {
		return managedProcessSnapshotEntry{}, fmt.Errorf("parse /proc/%d/stat: missing process identity", pid)
	}
	parentPID, err := strconv.Atoi(fields[1])
	if err != nil {
		return managedProcessSnapshotEntry{}, fmt.Errorf("parse /proc/%d/stat parent PID: %w", pid, err)
	}
	startedAt, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return managedProcessSnapshotEntry{}, fmt.Errorf("parse /proc/%d/stat start time: %w", pid, err)
	}
	return managedProcessSnapshotEntry{parentPID: parentPID, startedAt: startedAt}, nil
}
