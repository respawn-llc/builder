package worktreeui

import (
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
	"errors"
	"testing"
)

func TestRequestForExistingBranchUsesTargetAsBaseRef(t *testing.T) {
	req, err := Request(" main ", "ignored", worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.GetSpec().GetBaseRef() != "main" || req.GetSpec().GetCreateBranch() || req.GetSpec().GetBranchName() != "" {
		t.Fatalf("request = %+v, want existing branch checkout", req)
	}
}

func TestRequestForDetachedRefUsesTargetAsBaseRef(t *testing.T) {
	req, err := Request(" HEAD~1 ", "ignored", worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_DETACHED_REF)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.GetSpec().GetBaseRef() != "HEAD~1" || req.GetSpec().GetCreateBranch() || req.GetSpec().GetBranchName() != "" {
		t.Fatalf("request = %+v, want detached ref checkout", req)
	}
}

func TestRequestForNewBranchRequiresBaseRef(t *testing.T) {
	req, err := Request(" feature/a ", " HEAD ", worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.GetSpec().GetBaseRef() != "HEAD" || !req.GetSpec().GetCreateBranch() || req.GetSpec().GetBranchName() != "feature/a" {
		t.Fatalf("request = %+v, want new branch request", req)
	}
}

func TestRequestRejectsBlankTarget(t *testing.T) {
	if _, err := Request(" ", "HEAD", worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_EXISTING_BRANCH); err == nil || !errors.Is(err, ErrBranchTargetRequired) {
		t.Fatalf("error = %v, want target required", err)
	}
}

func TestRequestRejectsBlankBaseRefForNewBranch(t *testing.T) {
	_, err := Request("feature/a", " ", worktreepb.CreateTargetResolutionKind_WORKTREE_CREATE_TARGET_RESOLUTION_KIND_NEW_BRANCH)
	var typed *worktreecontract.CreateError
	if !errors.As(err, &typed) || typed.Owner != worktreecontract.CreateErrorOwnerBaseRef {
		t.Fatalf("error = %T %v, want Base-ref-owned create error", err, err)
	}
}
