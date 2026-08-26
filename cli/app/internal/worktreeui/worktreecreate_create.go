package worktreeui

import (
	"core/shared/worktreecontract"
	"errors"
	"strings"
)

// ErrBranchTargetRequired guards worktree-create target validation. Callers
// and tests match it with errors.Is rather than comparing rendered text.
var ErrBranchTargetRequired = errors.New("Branch or ref is required")

func Request(branchTarget string, baseRef string, kind worktreecontract.CreateTargetResolutionKind) (worktreecontract.CreateRequest, error) {
	if strings.TrimSpace(branchTarget) == "" {
		return worktreecontract.CreateRequest{}, ErrBranchTargetRequired
	}
	target := strings.TrimSpace(branchTarget)
	if kind == worktreecontract.CreateTargetResolutionKindExistingBranch || kind == worktreecontract.CreateTargetResolutionKindDetachedRef {
		return worktreecontract.CreateRequest{BaseRef: target, CreateBranch: false}, nil
	}
	trimmedBaseRef := strings.TrimSpace(baseRef)
	if err := worktreecontract.ValidateCreateSpec(trimmedBaseRef, true, target); err != nil {
		return worktreecontract.CreateRequest{}, worktreecontract.NewCreateError(
			worktreecontract.ProjectCreateValidationOwner(err, true),
			err.Error(),
			err,
		)
	}
	return worktreecontract.CreateRequest{BaseRef: trimmedBaseRef, CreateBranch: true, BranchName: target}, nil
}
