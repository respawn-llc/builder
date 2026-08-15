//go:build !windows && !darwin && !linux

package shell

import "errors"

func managedProcessSnapshot() (map[int]managedProcessSnapshotEntry, error) {
	return nil, errors.New("managed descendant cleanup is unsupported on this platform")
}
