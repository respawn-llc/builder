package worktreecontract

type DirtyState struct {
	Kind           DirtyStateKind
	DirtyFileCount *int
	UnknownCause   *string
}

func (state DirtyState) Validate() error {
	return ValidateDirtyState(state.Kind, state.DirtyFileCount, state.UnknownCause)
}
