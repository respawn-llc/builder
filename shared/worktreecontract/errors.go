package worktreecontract

import (
	"errors"
	"fmt"
	"strings"

	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
)

var (
	ErrWorktreeNotFound            = errors.New("worktree not found")
	ErrWorktreeBlocked             = errors.New("worktree is blocked")
	ErrWorktreeSelectorNotFound    = errors.New("worktree selector not found")
	ErrWorktreeSelectorAmbiguous   = errors.New("worktree selector is ambiguous")
	ErrWorktreeSelectorUnavailable = errors.New("worktree selector is unavailable")
	ErrWorktreeSetupRetained       = errors.New("worktree setup failed after worktree creation")
	ErrWorktreeDeletePrecondition  = errors.New("worktree deletion requires additional authorization")
	ErrWorktreeCreateContract      = errors.New("invalid worktree create error contract")
)

type SelectorError struct {
	Details *worktreepb.SelectorErrorDetails
}

func NewSelectorError(
	kind worktreepb.SelectorErrorKind,
	input string,
	candidates []*worktreepb.SelectorCandidate,
) *SelectorError {
	return &SelectorError{Details: &worktreepb.SelectorErrorDetails{
		Kind:       kind,
		Input:      input,
		Candidates: candidates,
	}}
}

func (e *SelectorError) Error() string {
	if e == nil || e.Details == nil {
		return "worktree selector error"
	}
	return "worktree selector error: " + e.Details.Kind.String()
}

func (e *SelectorError) Is(target error) bool {
	switch target {
	case ErrWorktreeSelectorNotFound:
		return e != nil && e.Details != nil &&
			e.Details.Kind == worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND
	case ErrWorktreeSelectorAmbiguous:
		return e != nil && e.Details != nil &&
			e.Details.Kind == worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS
	case ErrWorktreeSelectorUnavailable:
		return e != nil && e.Details != nil &&
			e.Details.Kind == worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_UNAVAILABLE
	default:
		return false
	}
}

type SetupRetainedError struct {
	Details *worktreepb.SetupRetainedDetails
	cause   error
}

func NewSetupRetainedError(
	worktree *worktreepb.RegisteredFacts,
	scriptPath string,
	diagnostic string,
	retainedPreviousWorktree *worktreepb.RetainedPreviousWorktree,
	cause error,
) (*SetupRetainedError, error) {
	if worktree == nil {
		return nil, errors.New("retained setup error requires a registered worktree")
	}
	if strings.TrimSpace(scriptPath) == "" {
		return nil, errors.New("retained setup error script_path is required")
	}
	if strings.TrimSpace(diagnostic) == "" {
		return nil, errors.New("retained setup error diagnostic is required")
	}
	return &SetupRetainedError{
		Details: &worktreepb.SetupRetainedDetails{
			Worktree:                 worktree,
			ScriptPath:               scriptPath,
			Diagnostic:               diagnostic,
			RetainedPreviousWorktree: retainedPreviousWorktree,
		},
		cause: cause,
	}, nil
}

func (e *SetupRetainedError) Error() string {
	if e == nil || e.Details == nil || strings.TrimSpace(e.Details.Diagnostic) == "" {
		return ErrWorktreeSetupRetained.Error()
	}
	return fmt.Sprintf("%s: %s", ErrWorktreeSetupRetained, strings.TrimSpace(e.Details.Diagnostic))
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

type DeletePreconditionError struct {
	Details *worktreepb.DeletePreconditionDetails
}

func NewDeletePreconditionError(dirtyState *worktreepb.DirtyState) *DeletePreconditionError {
	return &DeletePreconditionError{
		Details: &worktreepb.DeletePreconditionDetails{DirtyState: dirtyState},
	}
}

func (e *DeletePreconditionError) Error() string {
	if e == nil || e.Details == nil || e.Details.DirtyState == nil {
		return ErrWorktreeDeletePrecondition.Error()
	}
	switch e.Details.DirtyState.Kind {
	case worktreepb.DirtyStateKind_DIRTY_STATE_DIRTY:
		if e.Details.DirtyState.DirtyFileCount != nil {
			return fmt.Sprintf(
				"%s: %d modified or untracked file(s); force folder removal to continue",
				ErrWorktreeDeletePrecondition,
				*e.Details.DirtyState.DirtyFileCount,
			)
		}
	case worktreepb.DirtyStateKind_DIRTY_STATE_UNKNOWN:
		if e.Details.DirtyState.UnknownCause != nil {
			return fmt.Sprintf(
				"%s: worktree cleanliness could not be determined: %s; force folder removal to continue",
				ErrWorktreeDeletePrecondition,
				strings.TrimSpace(*e.Details.DirtyState.UnknownCause),
			)
		}
	}
	return ErrWorktreeDeletePrecondition.Error()
}

func (e *DeletePreconditionError) Is(target error) bool {
	return target == ErrWorktreeDeletePrecondition
}

type CreateErrorOwner string

const (
	CreateErrorOwnerBaseRef CreateErrorOwner = "base_ref"
	CreateErrorOwnerForm    CreateErrorOwner = "form"
)

type CreateError struct {
	Owner      CreateErrorOwner
	Diagnostic string
	cause      error
}

func NewCreateError(owner CreateErrorOwner, diagnostic string, cause error) error {
	result := &CreateError{Owner: owner, Diagnostic: diagnostic, cause: cause}
	if err := result.Validate(); err != nil {
		return NewCreateContractError("worktree.create.constructor", CreateErrorOwnerPointer(owner), diagnostic, err)
	}
	return result
}

func (e *CreateError) Error() string {
	if e == nil || strings.TrimSpace(e.Diagnostic) == "" {
		return "worktree creation failed"
	}
	return "worktree creation failed: " + strings.TrimSpace(e.Diagnostic)
}

func (e *CreateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *CreateError) Validate() error {
	if e == nil {
		return errors.New("worktree create error is required")
	}
	switch e.Owner {
	case CreateErrorOwnerBaseRef, CreateErrorOwnerForm:
	default:
		return errors.New("worktree create error owner is invalid")
	}
	if strings.TrimSpace(e.Diagnostic) == "" {
		return errors.New("worktree create error diagnostic is required")
	}
	return nil
}

type CreateContractError struct {
	Operation  string
	Owner      *CreateErrorOwner
	Diagnostic string
	Cause      error
}

func CreateErrorOwnerPointer(owner CreateErrorOwner) *CreateErrorOwner {
	return &owner
}

func NewCreateContractError(operation string, owner *CreateErrorOwner, diagnostic string, cause error) *CreateContractError {
	return &CreateContractError{
		Operation:  strings.TrimSpace(operation),
		Owner:      owner,
		Diagnostic: diagnostic,
		Cause:      cause,
	}
}

func (e *CreateContractError) Error() string {
	if e == nil {
		return ErrWorktreeCreateContract.Error()
	}
	details := fmt.Sprintf("%s: operation=%q", ErrWorktreeCreateContract.Error(), e.Operation)
	if e.Owner != nil {
		details += fmt.Sprintf(" owner=%q", *e.Owner)
	}
	if e.Cause == nil {
		return details
	}
	return fmt.Sprintf("%s: %v", details, e.Cause)
}

func (e *CreateContractError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CreateContractError) Is(target error) bool {
	return target == ErrWorktreeCreateContract
}

func ValidateCreateError(err error, operation string) error {
	var typed *CreateError
	if !errors.As(err, &typed) {
		return nil
	}
	if validationErr := typed.Validate(); validationErr != nil {
		var owner *CreateErrorOwner
		diagnostic := validationErr.Error()
		if typed != nil {
			owner = CreateErrorOwnerPointer(typed.Owner)
			diagnostic = typed.Diagnostic
		}
		return NewCreateContractError(operation, owner, diagnostic, validationErr)
	}
	return nil
}
