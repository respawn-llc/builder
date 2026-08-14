//go:build !windows && !darwin && !linux

package shell

func managedProcessSnapshot() (map[int]managedProcessSnapshotEntry, error) {
	return map[int]managedProcessSnapshotEntry{}, nil
}
