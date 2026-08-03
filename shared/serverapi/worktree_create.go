package serverapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/invariant"
	"core/shared/protocol"
)

var ErrWorktreeCreateContract = errors.New("invalid worktree create error contract")

type WorktreeCreateErrorOwner string

const (
	WorktreeCreateErrorOwnerBaseRef WorktreeCreateErrorOwner = "base_ref"
	WorktreeCreateErrorOwnerForm    WorktreeCreateErrorOwner = "form"
)

type WorktreeCreateError struct {
	Owner      WorktreeCreateErrorOwner `json:"owner"`
	Diagnostic string                   `json:"diagnostic"`
	cause      error
}

func NewWorktreeCreateError(owner WorktreeCreateErrorOwner, diagnostic string, cause error) error {
	result := &WorktreeCreateError{Owner: owner, Diagnostic: diagnostic, cause: cause}
	if err := result.Validate(); err != nil {
		return newWorktreeCreateContractError("worktree.create.constructor", owner, diagnostic, err)
	}
	return result
}

func (e *WorktreeCreateError) Error() string {
	if e == nil {
		return "worktree creation failed"
	}
	diagnostic := strings.TrimSpace(e.Diagnostic)
	if diagnostic == "" {
		return "worktree creation failed"
	}
	return "worktree creation failed: " + diagnostic
}

func (e *WorktreeCreateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *WorktreeCreateError) RPCErrorCode() int {
	return protocol.ErrCodeWorktreeCreate
}

func (e *WorktreeCreateError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Owner      WorktreeCreateErrorOwner `json:"owner"`
		Diagnostic string                   `json:"diagnostic"`
	}{
		Owner:      e.Owner,
		Diagnostic: e.Diagnostic,
	})
}

func (e *WorktreeCreateError) Validate() error {
	if e == nil {
		return errors.New("worktree create error is required")
	}
	switch e.Owner {
	case WorktreeCreateErrorOwnerBaseRef, WorktreeCreateErrorOwnerForm:
	default:
		return errors.New("worktree create error owner is invalid")
	}
	if strings.TrimSpace(e.Diagnostic) == "" {
		return errors.New("worktree create error diagnostic is required")
	}
	return nil
}

type WorktreeCreateContractError struct {
	Operation  string
	Owner      WorktreeCreateErrorOwner
	Diagnostic string
	Cause      error
}

func newWorktreeCreateContractError(operation string, owner WorktreeCreateErrorOwner, diagnostic string, cause error) *WorktreeCreateContractError {
	return &WorktreeCreateContractError{
		Operation:  strings.TrimSpace(operation),
		Owner:      owner,
		Diagnostic: diagnostic,
		Cause:      cause,
	}
}

func (e *WorktreeCreateContractError) Error() string {
	if e == nil {
		return ErrWorktreeCreateContract.Error()
	}
	if e.Cause == nil {
		return fmt.Sprintf("%s: operation=%q owner=%q", ErrWorktreeCreateContract.Error(), e.Operation, e.Owner)
	}
	return fmt.Sprintf("%s: operation=%q owner=%q: %v", ErrWorktreeCreateContract.Error(), e.Operation, e.Owner, e.Cause)
}

func (e *WorktreeCreateContractError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *WorktreeCreateContractError) Is(target error) bool {
	return target == ErrWorktreeCreateContract
}

func ValidateWorktreeCreateErrorBoundary(err error, operation string, policy invariant.Policy) error {
	var typed *WorktreeCreateError
	if !errors.As(err, &typed) {
		return nil
	}
	validationErr := typed.Validate()
	if validationErr == nil {
		return nil
	}
	diagnostic := invariant.Diagnostic{
		Scope: invariant.ScopeWorktreeContract,
		Fields: map[invariant.Field]string{
			invariant.FieldOperation:       strings.TrimSpace(operation),
			invariant.FieldRawOwner:        string(typed.Owner),
			invariant.FieldValidationCause: validationErr.Error(),
		},
	}
	policy.Check(false, diagnostic)
	return newWorktreeCreateContractError(operation, typed.Owner, typed.Diagnostic, validationErr)
}

func DecodeWorktreeCreateError(data json.RawMessage, message string) error {
	var payload struct {
		Owner      *WorktreeCreateErrorOwner `json:"owner"`
		Diagnostic *string                   `json:"diagnostic"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return newWorktreeCreateContractError("worktree.create.remote.decode", "", message, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return newWorktreeCreateContractError("worktree.create.remote.decode", "", message, errors.New("multiple JSON values"))
		}
		return newWorktreeCreateContractError("worktree.create.remote.decode", "", message, err)
	}
	if payload.Owner == nil || payload.Diagnostic == nil {
		return newWorktreeCreateContractError("worktree.create.remote.decode", "", message, errors.New("owner and diagnostic are required"))
	}
	result := &WorktreeCreateError{Owner: *payload.Owner, Diagnostic: *payload.Diagnostic}
	if err := result.Validate(); err != nil {
		return newWorktreeCreateContractError("worktree.create.remote.decode", result.Owner, result.Diagnostic, err)
	}
	return result
}

type WorktreeCreateValidationKind string

const (
	WorktreeCreateValidationBaseRefRequired     WorktreeCreateValidationKind = "base_ref_required"
	WorktreeCreateValidationBranchNameRequired  WorktreeCreateValidationKind = "branch_name_required"
	WorktreeCreateValidationBranchNameForbidden WorktreeCreateValidationKind = "branch_name_forbidden"
)

type WorktreeCreateValidationError struct {
	Kind       WorktreeCreateValidationKind
	Diagnostic string
}

func (e *WorktreeCreateValidationError) Error() string {
	if e == nil {
		return "worktree create specification is invalid"
	}
	return e.Diagnostic
}

func ProjectWorktreeCreateValidationOwner(err error, baseRefEditable bool) WorktreeCreateErrorOwner {
	var validationErr *WorktreeCreateValidationError
	if errors.As(err, &validationErr) &&
		validationErr.Kind == WorktreeCreateValidationBaseRefRequired &&
		baseRefEditable {
		return WorktreeCreateErrorOwnerBaseRef
	}
	return WorktreeCreateErrorOwnerForm
}

func ValidateWorktreeCreateSpec(baseRef string, createBranch bool, branchName string) error {
	baseRef = strings.TrimSpace(baseRef)
	branchName = strings.TrimSpace(branchName)
	if createBranch {
		if branchName == "" {
			return &WorktreeCreateValidationError{
				Kind:       WorktreeCreateValidationBranchNameRequired,
				Diagnostic: "branch name is required when create_branch=true",
			}
		}
		if baseRef == "" {
			return &WorktreeCreateValidationError{
				Kind:       WorktreeCreateValidationBaseRefRequired,
				Diagnostic: "base ref is required when create_branch=true",
			}
		}
		return nil
	}
	if baseRef == "" {
		return &WorktreeCreateValidationError{
			Kind:       WorktreeCreateValidationBaseRefRequired,
			Diagnostic: "base ref is required when create_branch=false",
		}
	}
	if branchName != "" {
		return &WorktreeCreateValidationError{
			Kind:       WorktreeCreateValidationBranchNameForbidden,
			Diagnostic: "branch name must be empty when create_branch=false",
		}
	}
	return nil
}
