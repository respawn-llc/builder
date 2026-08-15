package shell

type managedProcessIdentity struct {
	pid       int
	startedAt uint64
}

type managedProcessSnapshotEntry struct {
	parentPID      int
	processGroupID int
	startedAt      uint64
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

func managedProcessGroupMembers(
	processGroupID int,
	processes map[int]managedProcessSnapshotEntry,
) []managedProcessIdentity {
	members := make([]managedProcessIdentity, 0)
	for pid, process := range processes {
		if pid == processGroupID || process.processGroupID != processGroupID {
			continue
		}
		members = append(members, managedProcessIdentity{pid: pid, startedAt: process.startedAt})
	}
	return members
}

func mergeManagedProcessIdentities(
	first []managedProcessIdentity,
	second []managedProcessIdentity,
) []managedProcessIdentity {
	merged := make([]managedProcessIdentity, 0, len(first)+len(second))
	seen := make(map[managedProcessIdentity]struct{}, len(first)+len(second))
	appendUnique := func(identities []managedProcessIdentity) {
		for _, identity := range identities {
			if _, ok := seen[identity]; ok {
				continue
			}
			seen[identity] = struct{}{}
			merged = append(merged, identity)
		}
	}
	appendUnique(first)
	appendUnique(second)
	return merged
}
