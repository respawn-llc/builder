package patch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"core/server/tools"
	"core/shared/config"
)

type OutsideWorkspaceRequest = tools.FSGuardRequest
type OutsideWorkspaceDecision = tools.FSGuardDecision

const (
	OutsideWorkspaceDecisionDeny         = tools.FSGuardDecisionDeny
	OutsideWorkspaceDecisionAllowOnce    = tools.FSGuardDecisionAllowOnce
	OutsideWorkspaceDecisionAllowSession = tools.FSGuardDecisionAllowSession
)

type OutsideWorkspaceApproval = tools.FSGuardApproval
type OutsideWorkspaceApprover = tools.FSGuardApprover

type Option func(*Tool)

func WithAllowOutsideWorkspace(allow bool) Option {
	return func(t *Tool) {
		t.allowOutsideWorkspace = allow
	}
}

func WithOutsideWorkspaceApprover(approver OutsideWorkspaceApprover) Option {
	return func(t *Tool) {
		t.outsideWorkspaceApprover = approver
	}
}

func WithPathDenyPolicy(policy tools.PathDenyPolicy) Option {
	return func(t *Tool) {
		t.pathDenyPolicy = policy
	}
}

const outsideWorkspaceRejectionInstruction = "If it's essential to the task, ask the user to make the edit manually at the end of the task."

func (t *Tool) resolvePath(ctx context.Context, path string, mustExist bool, approvedOutside map[string]bool) (string, error) {
	real, err := t.resolvePathTarget(path, mustExist)
	if err != nil {
		return "", err
	}
	return t.guardResolvedPath(ctx, path, real, approvedOutside)
}

func (t *Tool) resolvePathTarget(path string, mustExist bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(t.workspaceRoot, candidate)
	}
	candidate = filepath.Clean(candidate)

	real := candidate
	if mustExist {
		var err error
		real, err = config.ResolveExistingPathRealPath(real)
		if err != nil {
			return "", fmt.Errorf("resolve path %q: %w", path, err)
		}
	} else {
		var err error
		real, err = config.ResolveExistingAncestorRealPath(real)
		if err != nil {
			return "", fmt.Errorf("resolve path %q: %w", path, err)
		}
	}

	return real, nil
}

func (t *Tool) guardResolvedPath(ctx context.Context, path string, real string, approvedOutside map[string]bool) (string, error) {
	guard := NewOutsideWorkspaceGuardWithPolicy(
		t.workspaceRoot,
		t.workspaceRootReal,
		t.workspaceRootInfo,
		t.workspaceOnly,
		t.allowOutsideWorkspace,
		t.outsideWorkspaceApprover,
		func() bool {
			t.outsideWorkspaceSessionMu.RLock()
			defer t.outsideWorkspaceSessionMu.RUnlock()
			return t.outsideWorkspaceSessionAllow
		},
		func(allow bool) {
			t.outsideWorkspaceSessionMu.Lock()
			t.outsideWorkspaceSessionAllow = allow
			t.outsideWorkspaceSessionMu.Unlock()
		},
		outsideWorkspaceRejectionInstruction,
		OutsideWorkspaceErrorLabels{
			OutsidePath:          "patch target outside workspace",
			ApprovalFailed:       "outside-workspace edit approval failed",
			RejectedByUserPrefix: "patch target outside workspace rejected by user",
		},
		OutsideWorkspaceFailureFactory{},
		IsPathInTemporaryDir,
		nil,
		t.pathDenyPolicy,
	)
	return guard.Allow(ctx, path, real, approvedOutside)
}
