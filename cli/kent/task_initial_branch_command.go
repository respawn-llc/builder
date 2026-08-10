package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"core/shared/serverapi"
)

func addInitialBranchExecutionFlags(fs *flag.FlagSet) (*string, *string) {
	return fs.String("execution-target", "", "task-local execution target: "+executionTargetSelectorHelp),
		fs.String("branch-name", "", "branch name for the task's first managed worktree")
}

func parseInitialBranchExecutionOptions(
	fs *flag.FlagSet,
	executionTargetRaw string,
	branchNameRaw string,
	stderr io.Writer,
) (*serverapi.WorkflowExecutionTargetSelection, *string, bool) {
	executionTarget, err := parseOptionalTaskExecutionTarget(executionTargetRaw, flagExplicit(fs, "execution-target"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, nil, false
	}
	branchName, err := parseExplicitBranchName(branchNameRaw, flagExplicit(fs, "branch-name"))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, nil, false
	}
	return executionTarget, branchName, true
}

func writeWorkflowTaskTargetOrBranchError(stderr io.Writer, err error) bool {
	return writeWorkflowExecutionTargetError(stderr, err) || writeWorkflowTaskInitialBranchError(stderr, err)
}

func rejectInitialBranchForMoveNoOp(stderr io.Writer, branchName *string) bool {
	if branchName == nil {
		return false
	}
	fmt.Fprintln(stderr, "task move no-op does not accept --branch-name")
	return true
}

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
