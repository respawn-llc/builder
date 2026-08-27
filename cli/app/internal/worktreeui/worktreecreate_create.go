package worktreeui

import (
	"errors"
	"strings"

	"core/shared/protoapi"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
)

// ErrBranchTargetRequired guards worktree-create target validation. Callers
// and tests match it with errors.Is rather than comparing rendered text.
var ErrBranchTargetRequired = errors.New("Branch or ref is required")

func Request(branchTarget string, baseRef string, kind worktreepb.CreateTargetResolutionKind) (*worktreepb.CreateRequest, error) {
	if strings.TrimSpace(branchTarget) == "" {
		return nil, ErrBranchTargetRequired
	}
	target := strings.TrimSpace(branchTarget)
	spec := &worktreepb.CreateSpec{CreateBranch: kind == worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH}
	if spec.CreateBranch {
		trimmedBaseRef := strings.TrimSpace(baseRef)
		spec.BaseRef = &trimmedBaseRef
		spec.BranchName = &target
	} else {
		spec.BaseRef = &target
	}
	if err := protoapi.Validate(spec); err != nil {
		return nil, protoapi.ClassifyWorktreeCreateValidation(err)
	}
	return &worktreepb.CreateRequest{Spec: spec}, nil
}
