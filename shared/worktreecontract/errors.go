package worktreecontract

import (
	"errors"
	"fmt"
	"strings"
)

var ErrWorktreeCreateContract = errors.New("invalid worktree create error contract")

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
