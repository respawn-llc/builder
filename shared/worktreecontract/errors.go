package worktreecontract

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrWorktreeSelectorNotFound    = errors.New("worktree selector not found")
	ErrWorktreeSelectorAmbiguous   = errors.New("worktree selector is ambiguous")
	ErrWorktreeSelectorUnavailable = errors.New("worktree selector is unavailable")
	ErrWorktreeTransitionPending   = errors.New("a worktree transition is already pending for this session")
	ErrWorktreeSetupRetained       = errors.New("worktree setup failed after worktree creation")
	ErrWorktreeDeletePrecondition  = errors.New("worktree deletion requires additional authorization")
)

type SelectorErrorKind string

const (
	SelectorErrorKindNotFound    SelectorErrorKind = "not_found"
	SelectorErrorKindAmbiguous   SelectorErrorKind = "ambiguous"
	SelectorErrorKindUnavailable SelectorErrorKind = "unavailable"
)

type SelectorCandidate struct {
	Variant          TopologyVariant
	Selector         string
	BranchName       *string
	DisplayName      *string
	FallbackIdentity string
}

type SelectorError struct {
	Kind       SelectorErrorKind
	Input      string
	Candidates []SelectorCandidate
}

func (e *SelectorError) Error() string {
	if e == nil {
		return "worktree selector error"
	}
	return "worktree selector error: " + string(e.Kind)
}

func (e *SelectorError) Is(target error) bool {
	switch target {
	case ErrWorktreeSelectorNotFound:
		return e != nil && e.Kind == SelectorErrorKindNotFound
	case ErrWorktreeSelectorAmbiguous:
		return e != nil && e.Kind == SelectorErrorKindAmbiguous
	case ErrWorktreeSelectorUnavailable:
		return e != nil && e.Kind == SelectorErrorKindUnavailable
	default:
		return false
	}
}

func (e *SelectorError) Validate() error {
	if e == nil {
		return errors.New("worktree selector error is required")
	}
	if strings.TrimSpace(e.Input) == "" {
		return errors.New("worktree selector error input is required")
	}
	switch e.Kind {
	case SelectorErrorKindNotFound, SelectorErrorKindUnavailable:
		if len(e.Candidates) != 0 {
			return errors.New("non-ambiguous selector error cannot contain candidates")
		}
	case SelectorErrorKindAmbiguous:
		if len(e.Candidates) == 0 {
			return errors.New("ambiguous selector error requires candidates")
		}
		for _, candidate := range e.Candidates {
			if err := candidate.Validate(); err != nil {
				return err
			}
		}
	default:
		return errors.New("worktree selector error kind is invalid")
	}
	return nil
}

func (candidate SelectorCandidate) Validate() error {
	switch candidate.Variant {
	case TopologyVariantRegistered, TopologyVariantExternal, TopologyVariantMissing:
	default:
		return errors.New("worktree selector candidate variant is invalid")
	}
	if strings.TrimSpace(candidate.Selector) == "" || strings.TrimSpace(candidate.FallbackIdentity) == "" {
		return errors.New("worktree selector candidate requires selector and fallback_identity")
	}
	for _, fact := range []*string{candidate.BranchName, candidate.DisplayName} {
		if fact != nil && strings.TrimSpace(*fact) == "" {
			return errors.New("worktree selector candidate optional facts must not be empty")
		}
	}
	return nil
}

type TransitionPendingError struct {
	SessionID          string
	PendingOperationID OperationID
}

type ImmediateTransitionErrorKind string

const (
	ImmediateTransitionOriginInactive       ImmediateTransitionErrorKind = "origin_inactive"
	ImmediateTransitionApplyFailed          ImmediateTransitionErrorKind = "apply_failed"
	worktreeImmediateTransitionErrorMessage                              = "worktree transition could not become authoritative before command completion"
)

type ImmediateTransitionError struct {
	Kind  ImmediateTransitionErrorKind
	Cause error
}

func NewImmediateTransitionError(kind ImmediateTransitionErrorKind, cause error) *ImmediateTransitionError {
	if cause != nil {
		cause = fmt.Errorf("%s: %w", worktreeImmediateTransitionErrorMessage, cause)
	}
	return &ImmediateTransitionError{Kind: kind, Cause: cause}
}

func (e *ImmediateTransitionError) Error() string {
	if e == nil || e.Cause == nil {
		return worktreeImmediateTransitionErrorMessage
	}
	return e.Cause.Error()
}

func (e *ImmediateTransitionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *TransitionPendingError) Error() string {
	return ErrWorktreeTransitionPending.Error()
}

func (e *TransitionPendingError) Is(target error) bool {
	return target == ErrWorktreeTransitionPending
}

func (e *TransitionPendingError) Validate() error {
	if e == nil {
		return errors.New("worktree transition pending error is required")
	}
	if err := validateRequiredSessionID(e.SessionID); err != nil {
		return err
	}
	return e.PendingOperationID.Validate()
}

type SetupRetainedError struct {
	Worktree                 TopologyEntry
	ScriptPath               string
	Diagnostic               string
	RetainedPreviousWorktree *RetainedPreviousWorktree
	cause                    error
}

func NewSetupRetainedError(worktree TopologyEntry, scriptPath string, diagnostic string, cause error) (*SetupRetainedError, error) {
	result := &SetupRetainedError{
		Worktree:   worktree,
		ScriptPath: scriptPath,
		Diagnostic: diagnostic,
		cause:      cause,
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

func (e *SetupRetainedError) Error() string {
	base := ErrWorktreeSetupRetained.Error()
	if e == nil {
		return base
	}
	diagnostic := strings.TrimSpace(e.Diagnostic)
	if diagnostic == "" {
		return base
	}
	return fmt.Sprintf("%s: %s", base, diagnostic)
}

func (e *SetupRetainedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *SetupRetainedError) Is(target error) bool {
	return target == ErrWorktreeSetupRetained
}

func (e *SetupRetainedError) Validate() error {
	if e == nil {
		return errors.New("worktree setup retained error is required")
	}
	if e.Worktree.Variant != TopologyVariantRegistered {
		return errors.New("retained setup error requires a registered worktree")
	}
	if err := e.Worktree.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(e.ScriptPath) == "" {
		return errors.New("retained setup error script_path is required")
	}
	if strings.TrimSpace(e.Diagnostic) == "" {
		return errors.New("retained setup error diagnostic is required")
	}
	return validateRetainedPreviousWorktree(e.RetainedPreviousWorktree)
}

type DeletePreconditionError struct {
	DirtyState DirtyState
}

func (e *DeletePreconditionError) Error() string {
	base := ErrWorktreeDeletePrecondition.Error()
	if e == nil {
		return base
	}
	switch e.DirtyState.Kind {
	case DirtyStateDirty:
		if e.DirtyState.DirtyFileCount != nil && *e.DirtyState.DirtyFileCount > 0 {
			return fmt.Sprintf(
				"%s: %d modified or untracked file(s); force folder removal to continue",
				base,
				*e.DirtyState.DirtyFileCount,
			)
		}
	case DirtyStateUnknown:
		if e.DirtyState.UnknownCause != nil {
			cause := strings.TrimSpace(*e.DirtyState.UnknownCause)
			if cause != "" {
				return fmt.Sprintf(
					"%s: worktree cleanliness could not be determined: %s; force folder removal to continue",
					base,
					cause,
				)
			}
		}
	}
	return base
}

func (e *DeletePreconditionError) Is(target error) bool {
	return target == ErrWorktreeDeletePrecondition
}

func (e *DeletePreconditionError) Validate() error {
	if e == nil {
		return errors.New("worktree delete precondition error is required")
	}
	return ValidateDeletePrecondition(
		e.DirtyState.Kind,
		e.DirtyState.DirtyFileCount,
		e.DirtyState.UnknownCause,
	)
}
