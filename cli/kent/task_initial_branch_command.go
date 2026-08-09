package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"core/shared/serverapi"
)

func parseExplicitBranchName(raw string, provided bool) (*string, error) {
	if !provided {
		return nil, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("--branch-name requires a non-blank branch name")
	}
	return &raw, nil
}

func writeWorkflowTaskInitialBranchError(stderr io.Writer, err error) bool {
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) {
		return false
	}
	if validationErr := branchErr.Validate(); validationErr != nil {
		fmt.Fprintf(stderr, "Invalid workflow task branch error: %v.\n", validationErr)
		return true
	}
	switch branchErr.Reason {
	case serverapi.WorkflowTaskInitialBranchErrorReasonInvalidName:
		fmt.Fprintf(stderr, "Branch name %q is not a valid Git branch name.\n", branchErr.BranchName)
	case serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision:
		fmt.Fprintf(stderr, "Branch %q already exists locally as %q.\n", branchErr.BranchName, *branchErr.Ref)
	case serverapi.WorkflowTaskInitialBranchErrorReasonRemoteTrackingCollision:
		fmt.Fprintf(
			stderr,
			"Branch %q conflicts with locally known remote-tracking branch %q on remote %q.\n",
			branchErr.BranchName,
			*branchErr.Ref,
			*branchErr.Remote,
		)
	case serverapi.WorkflowTaskInitialBranchErrorReasonNoManagedTarget:
		fmt.Fprintf(stderr, "Branch %q requires a managed-worktree execution target.\n", branchErr.BranchName)
	case serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree:
		fmt.Fprintf(stderr, "Branch %q cannot be used because this operation cannot create the task's first managed worktree.\n", branchErr.BranchName)
	case serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch:
		fmt.Fprintf(
			stderr,
			"Task worktree uses branch %q; requested branch %q cannot rename it.\n",
			*branchErr.ExistingBranchName,
			branchErr.BranchName,
		)
	default:
		fmt.Fprintln(stderr, branchErr)
	}
	return true
}
