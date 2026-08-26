package worktreeui

import (
	"core/shared/worktreecontract"
	"errors"
	"testing"
)

func TestRequestForExistingBranchUsesTargetAsBaseRef(t *testing.T) {
	req, err := Request(" main ", "ignored", worktreecontract.CreateTargetResolutionKindExistingBranch)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.BaseRef != "main" || req.CreateBranch || req.BranchName != "" {
		t.Fatalf("request = %+v, want existing branch checkout", req)
	}
}

func TestRequestForDetachedRefUsesTargetAsBaseRef(t *testing.T) {
	req, err := Request(" HEAD~1 ", "ignored", worktreecontract.CreateTargetResolutionKindDetachedRef)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.BaseRef != "HEAD~1" || req.CreateBranch || req.BranchName != "" {
		t.Fatalf("request = %+v, want detached ref checkout", req)
	}
}

func TestRequestForNewBranchRequiresBaseRef(t *testing.T) {
	req, err := Request(" feature/a ", " HEAD ", worktreecontract.CreateTargetResolutionKindNewBranch)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.BaseRef != "HEAD" || !req.CreateBranch || req.BranchName != "feature/a" {
		t.Fatalf("request = %+v, want new branch request", req)
	}
}

func TestRequestRejectsBlankTarget(t *testing.T) {
	if _, err := Request(" ", "HEAD", worktreecontract.CreateTargetResolutionKindExistingBranch); err == nil || !errors.Is(err, ErrBranchTargetRequired) {
		t.Fatalf("error = %v, want target required", err)
	}
}

func TestRequestRejectsBlankBaseRefForNewBranch(t *testing.T) {
	_, err := Request("feature/a", " ", worktreecontract.CreateTargetResolutionKindNewBranch)
	var typed *worktreecontract.CreateError
	if !errors.As(err, &typed) || typed.Owner != worktreecontract.CreateErrorOwnerBaseRef {
		t.Fatalf("error = %T %v, want Base-ref-owned create error", err, err)
	}
}
