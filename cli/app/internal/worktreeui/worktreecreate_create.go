package worktreeui

import (
	"errors"
	"strings"

	"core/shared/serverapi"
)

// ErrBranchTargetRequired guards worktree-create target validation. Callers
// and tests match it with errors.Is rather than comparing rendered text.
var ErrBranchTargetRequired = errors.New("Branch or ref is required")

func Request(branchTarget string, baseRef string, kind serverapi.WorktreeCreateTargetResolutionKind) (serverapi.WorktreeCreateRequest, error) {
	if strings.TrimSpace(branchTarget) == "" {
		return serverapi.WorktreeCreateRequest{}, ErrBranchTargetRequired
	}
	target := strings.TrimSpace(branchTarget)
	if kind == serverapi.WorktreeCreateTargetResolutionKindExistingBranch || kind == serverapi.WorktreeCreateTargetResolutionKindDetachedRef {
		return serverapi.WorktreeCreateRequest{BaseRef: target, CreateBranch: false}, nil
	}
	trimmedBaseRef := strings.TrimSpace(baseRef)
	if err := serverapi.ValidateWorktreeCreateSpec(trimmedBaseRef, true, target); err != nil {
		return serverapi.WorktreeCreateRequest{}, serverapi.NewWorktreeCreateError(
			serverapi.ProjectWorktreeCreateValidationOwner(err, true),
			err.Error(),
			err,
		)
	}
	return serverapi.WorktreeCreateRequest{BaseRef: trimmedBaseRef, CreateBranch: true, BranchName: target}, nil
}
