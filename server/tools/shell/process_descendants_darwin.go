//go:build darwin

package shell

import "golang.org/x/sys/unix"

func managedProcessSnapshot() (map[int]managedProcessSnapshotEntry, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	snapshot := make(map[int]managedProcessSnapshotEntry, len(processes))
	for _, process := range processes {
		snapshot[int(process.Proc.P_pid)] = managedProcessSnapshotEntry{
			parentPID: int(process.Eproc.Ppid),
			startedAt: uint64(process.Proc.P_starttime.Sec)*1_000_000 +
				uint64(process.Proc.P_starttime.Usec),
		}
	}
	return snapshot, nil
}
