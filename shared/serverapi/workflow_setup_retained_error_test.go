package serverapi

import (
	"errors"
	"testing"
)

func TestDecodeWorkflowSetupRetainedErrorRejectsMalformedNestedWorktreeFacts(t *testing.T) {
	blank := " "
	tests := []struct {
		name   string
		mutate func(*WorkflowSetupRetainedError)
	}{
		{
			name: "retained branch ref",
			mutate: func(setupErr *WorkflowSetupRetainedError) {
				setupErr.Worktree.Registered.Git.BranchRef = &blank
			},
		},
		{
			name: "retained branch name",
			mutate: func(setupErr *WorkflowSetupRetainedError) {
				setupErr.Worktree.Registered.Git.BranchName = &blank
			},
		},
		{
			name: "retained locked reason",
			mutate: func(setupErr *WorkflowSetupRetainedError) {
				setupErr.Worktree.Registered.Git.LockedReason = &blank
			},
		},
		{
			name: "retained prunable reason",
			mutate: func(setupErr *WorkflowSetupRetainedError) {
				setupErr.Worktree.Registered.Git.PrunableReason = &blank
			},
		},
		{
			name: "retained origin session",
			mutate: func(setupErr *WorkflowSetupRetainedError) {
				setupErr.Worktree.Registered.Kent.OriginSessionID = &blank
			},
		},
		{
			name: "previous retained worktree",
			mutate: func(setupErr *WorkflowSetupRetainedError) {
				setupErr.RetainedPreviousWorktree.Worktree.Registered.Git.BranchName = &blank
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setupErr := validWorkflowSetupRetainedError()
			test.mutate(setupErr)

			decoded := DecodeWorkflowSetupRetainedError(setupErr.RPCErrorData(), "setup failed")
			var typed *WorkflowSetupRetainedError
			if errors.As(decoded, &typed) {
				t.Fatalf("decoded malformed retained-worktree data as typed error: %+v", typed)
			}
			if decoded.Error() != "setup failed" {
				t.Fatalf("decoded error = %q, want generic fallback", decoded)
			}
		})
	}
}

func validWorkflowSetupRetainedError() *WorkflowSetupRetainedError {
	branchRef := "refs/heads/feature"
	branchName := "feature"
	lockedReason := "locked"
	prunableReason := "prunable"
	originSessionID := "session-1"

	return &WorkflowSetupRetainedError{
		Worktree: workflowRegisteredWorktreeTopology(
			"/repo/feature",
			"worktree-1",
			&branchRef,
			&branchName,
			&lockedReason,
			&prunableReason,
			&originSessionID,
		),
		ScriptPath: "/repo/scripts/setup.sh",
		Diagnostic: "setup failed",
		RetainedPreviousWorktree: &WorkflowRetainedPreviousWorktree{
			Worktree: workflowRegisteredWorktreeTopology(
				"/repo/previous",
				"worktree-2",
				&branchRef,
				&branchName,
				&lockedReason,
				&prunableReason,
				&originSessionID,
			),
		},
	}
}

func workflowRegisteredWorktreeTopology(
	root string,
	worktreeID string,
	branchRef *string,
	branchName *string,
	lockedReason *string,
	prunableReason *string,
	originSessionID *string,
) WorkflowRegisteredWorktreeTopology {
	return WorkflowRegisteredWorktreeTopology{
		Variant: "registered",
		Registered: &WorkflowRegisteredWorktreeFacts{
			Git: WorkflowWorktreeGitFacts{
				CanonicalRoot:  root,
				HeadObject:     "abc123",
				BranchRef:      branchRef,
				BranchName:     branchName,
				LockedReason:   lockedReason,
				PrunableReason: prunableReason,
				PathAvailable:  true,
			},
			Kent: WorkflowWorktreeKentFacts{
				WorktreeID:      worktreeID,
				CanonicalRoot:   root,
				DisplayName:     branchNameValue(branchName),
				Managed:         true,
				OriginSessionID: originSessionID,
			},
		},
	}
}

func branchNameValue(branchName *string) string {
	if branchName == nil {
		return "detached"
	}
	return *branchName
}
