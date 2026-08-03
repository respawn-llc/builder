package worktreecontract

type DirtyStateKind string

const (
	DirtyStateClean   DirtyStateKind = "clean"
	DirtyStateDirty   DirtyStateKind = "dirty"
	DirtyStateUnknown DirtyStateKind = "unknown"
)

type TransitionKind string

const TransitionDelete TransitionKind = "delete"
