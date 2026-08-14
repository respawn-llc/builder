package shell

type managedProcessIdentity struct {
	pid       int
	startedAt uint64
}

type managedProcessSnapshotEntry struct {
	parentPID int
	startedAt uint64
}
