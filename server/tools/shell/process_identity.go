package shell

type managedProcessIdentity struct {
	pid       int
	startedAt uint64
}

type managedProcessSnapshotEntry struct {
	parentPID int
	startedAt uint64
}

func livingManagedDescendantPIDsIn(
	descendants []managedProcessIdentity,
	processes map[int]managedProcessSnapshotEntry,
) []managedProcessIdentity {
	living := make([]managedProcessIdentity, 0, len(descendants))
	for _, descendant := range descendants {
		current, ok := processes[descendant.pid]
		if ok && current.startedAt == descendant.startedAt {
			living = append(living, descendant)
		}
	}
	return living
}
