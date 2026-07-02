package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type FSGuardErrorLabels struct {
	OutsidePath          string
	ApprovalFailed       string
	RejectedByUserPrefix string
}

type FSGuardFailureFactory struct {
	ApprovalFailed        func(FSGuardRequest, error) error
	UserDenied            func(FSGuardRequest, FSGuardApproval, string) error
	NoPermission          func(string, string) error
	DefaultApprovalFailed func(string, string) error
	DefaultUserDenied     func(string, string) error
}

type FSGuardRequest struct {
	RequestedPath string
	ResolvedPath  string
	WorkspaceRoot string
}

type FSGuardDecision int

const (
	FSGuardDecisionDeny FSGuardDecision = iota
	FSGuardDecisionAllowOnce
	FSGuardDecisionAllowSession
)

type FSGuardApproval struct {
	Decision   FSGuardDecision
	Commentary string
}

type FSGuardApprover func(context.Context, FSGuardRequest) (FSGuardApproval, error)

type FSGuard struct {
	workspaceRoot         string
	workspaceRootReal     string
	workspaceRootInfo     os.FileInfo
	workspaceOnly         bool
	allowOutsideWorkspace bool
	approver              FSGuardApprover
	sessionAllowed        func() bool
	setSessionAllowed     func(bool)
	rejectionInstruction  string
	errorLabels           FSGuardErrorLabels
	failures              FSGuardFailureFactory
	temporaryPathAllowed  func(string) bool
	onApproved            func(FSGuardRequest, string)
	pathDenyPolicy        PathDenyPolicy
}

type FSGuardConfig struct {
	WorkspaceRoot         string
	WorkspaceRootReal     string
	WorkspaceRootInfo     os.FileInfo
	WorkspaceOnly         bool
	AllowOutsideWorkspace bool
	Approver              FSGuardApprover
	SessionAllowed        func() bool
	SetSessionAllowed     func(bool)
	RejectionInstruction  string
	ErrorLabels           FSGuardErrorLabels
	Failures              FSGuardFailureFactory
	TemporaryPathAllowed  func(string) bool
	OnApproved            func(FSGuardRequest, string)
	PathDenyPolicy        PathDenyPolicy
}

func NewFSGuard(config FSGuardConfig) FSGuard {
	return FSGuard{
		workspaceRoot:         config.WorkspaceRoot,
		workspaceRootReal:     config.WorkspaceRootReal,
		workspaceRootInfo:     config.WorkspaceRootInfo,
		workspaceOnly:         config.WorkspaceOnly,
		allowOutsideWorkspace: config.AllowOutsideWorkspace,
		approver:              config.Approver,
		sessionAllowed:        config.SessionAllowed,
		setSessionAllowed:     config.SetSessionAllowed,
		rejectionInstruction:  config.RejectionInstruction,
		errorLabels:           config.ErrorLabels,
		failures:              config.Failures,
		temporaryPathAllowed:  config.TemporaryPathAllowed,
		onApproved:            config.OnApproved,
		pathDenyPolicy:        config.PathDenyPolicy,
	}
}

func (g FSGuard) Allow(ctx context.Context, requestedPath string, resolvedPath string, approvedOutside map[string]bool) (string, error) {
	req := FSGuardRequest{
		RequestedPath: requestedPath,
		ResolvedPath:  resolvedPath,
		WorkspaceRoot: g.workspaceRoot,
	}
	match, denied, denyErr := g.pathDenyPolicy.Match(resolvedPath)
	if denyErr != nil {
		return "", fmt.Errorf("path deny policy check for %q: %w", requestedPath, denyErr)
	}
	if denied {
		return "", g.noPermission(requestedPath, match.Message)
	}
	if !g.workspaceOnly {
		return resolvedPath, nil
	}
	insideWorkspace, containmentErr := g.isWithinWorkspace(resolvedPath)
	if containmentErr != nil {
		return "", fmt.Errorf("workspace boundary check for %q: %w", requestedPath, containmentErr)
	}
	if insideWorkspace {
		return resolvedPath, nil
	}

	if g.temporaryPathAllowed != nil && g.temporaryPathAllowed(resolvedPath) {
		g.logApproved(req, "temporary_allow")
		return resolvedPath, nil
	}
	if g.allowOutsideWorkspace {
		g.logApproved(req, "configured_allow")
		return resolvedPath, nil
	}
	if g.sessionAllowed != nil && g.sessionAllowed() {
		g.logApproved(req, "session_allow")
		return resolvedPath, nil
	}
	if approvedOutside != nil && approvedOutside[resolvedPath] {
		g.logApproved(req, "call_allow")
		return resolvedPath, nil
	}
	if g.approver == nil {
		return "", g.noPermission(requestedPath, g.errorLabels.OutsidePath)
	}
	approval, approveErr := g.approver(ctx, req)
	if approveErr != nil {
		if g.failures.ApprovalFailed != nil {
			return "", g.failures.ApprovalFailed(req, approveErr)
		}
		return "", g.approvalFailed(requestedPath, approveErr.Error())
	}
	switch approval.Decision {
	case FSGuardDecisionAllowOnce:
		if approvedOutside != nil {
			approvedOutside[resolvedPath] = true
		}
		g.logApproved(req, "allow_once")
		return resolvedPath, nil
	case FSGuardDecisionAllowSession:
		if g.setSessionAllowed != nil {
			g.setSessionAllowed(true)
		}
		if approvedOutside != nil {
			approvedOutside[resolvedPath] = true
		}
		g.logApproved(req, "allow_session")
		return resolvedPath, nil
	default:
		if g.failures.UserDenied != nil {
			return "", g.failures.UserDenied(req, approval, g.rejectionInstruction)
		}
		return "", g.userDenied(requestedPath, approval.Commentary, g.rejectionInstruction)
	}
}

func (g FSGuard) isWithinWorkspace(real string) (bool, error) {
	rel, relErr := filepath.Rel(g.workspaceRootReal, real)
	if relErr == nil {
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true, nil
		}
		return false, nil
	}

	if g.workspaceRootInfo == nil {
		return false, errors.New("workspace root info unavailable")
	}

	current := real
	for {
		info, statErr := os.Stat(current)
		if statErr != nil {
			if !errors.Is(statErr, os.ErrNotExist) {
				return false, fmt.Errorf("stat candidate path %q: %w", current, statErr)
			}
			next := filepath.Dir(current)
			if next == current {
				return false, fmt.Errorf("stat candidate path %q: %w", real, statErr)
			}
			current = next
			continue
		}
		if os.SameFile(info, g.workspaceRootInfo) {
			return true, nil
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}

	return false, nil
}

func (g FSGuard) noPermission(path, reason string) error {
	if g.failures.NoPermission != nil {
		return g.failures.NoPermission(path, reason)
	}
	return fmt.Errorf("no file edit permission for %s: %s", path, reason)
}

func (g FSGuard) approvalFailed(path, reason string) error {
	if g.failures.DefaultApprovalFailed != nil {
		return g.failures.DefaultApprovalFailed(path, reason)
	}
	return fmt.Errorf("file edit approval failed for %s: %s", path, reason)
}

func (g FSGuard) userDenied(path, commentary string, instruction string) error {
	if g.failures.DefaultUserDenied != nil {
		return g.failures.DefaultUserDenied(path, commentary)
	}
	message := fmt.Sprintf("user denied edit for %s", path)
	if strings.TrimSpace(commentary) != "" {
		message += ": " + strings.TrimSpace(commentary)
	}
	if strings.TrimSpace(instruction) != "" {
		message += ": " + strings.TrimSpace(instruction)
	}
	return errors.New(message)
}

func (g FSGuard) logApproved(req FSGuardRequest, reason string) {
	if g.onApproved != nil {
		g.onApproved(req, reason)
	}
}
