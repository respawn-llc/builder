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
	RequestedPath    string
	ResolvedPath     string
	WorkingDirectory string
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

type FilesystemRoot struct {
	LexicalPath string
	RealPath    string
	Info        os.FileInfo
}

type ProjectWorkspaceRoot struct {
	WorkspaceID *string
	FilesystemRoot
}

type ProjectWorkspaceScope struct {
	ProjectID string
	Roots     []ProjectWorkspaceRoot
}

func (b ProjectWorkspaceScope) Clone() ProjectWorkspaceScope {
	b.Roots = append([]ProjectWorkspaceRoot(nil), b.Roots...)
	return b
}

type FileAccessScope struct {
	WorkingDirectory    FilesystemRoot
	ExecutionTargetRoot FilesystemRoot
	ProjectWorkspace    ProjectWorkspaceScope
}

func ValidateFileAccessScope(scope FileAccessScope) error {
	if strings.TrimSpace(scope.WorkingDirectory.LexicalPath) == "" ||
		strings.TrimSpace(scope.WorkingDirectory.RealPath) == "" ||
		strings.TrimSpace(scope.ExecutionTargetRoot.LexicalPath) == "" ||
		strings.TrimSpace(scope.ExecutionTargetRoot.RealPath) == "" {
		return errors.New("file access scope requires working and execution target roots")
	}
	return nil
}

func (s FileAccessScope) Clone() FileAccessScope {
	s.ProjectWorkspace = s.ProjectWorkspace.Clone()
	return s
}

type FilesystemContext struct {
	Access          FileAccessScope
	ManagedWorktree *ManagedWorktreePathContext
}

func (c FilesystemContext) Clone() FilesystemContext {
	return FilesystemContext{Access: c.Access.Clone(), ManagedWorktree: c.ManagedWorktree}
}

func (c FilesystemContext) Equal(other FilesystemContext) bool {
	if !sameFilesystemRoot(c.Access.WorkingDirectory, other.Access.WorkingDirectory) ||
		!sameFilesystemRoot(c.Access.ExecutionTargetRoot, other.Access.ExecutionTargetRoot) ||
		c.Access.ProjectWorkspace.ProjectID != other.Access.ProjectWorkspace.ProjectID ||
		len(c.Access.ProjectWorkspace.Roots) != len(other.Access.ProjectWorkspace.Roots) ||
		!c.ManagedWorktree.Equal(other.ManagedWorktree) {
		return false
	}
	for i, left := range c.Access.ProjectWorkspace.Roots {
		right := other.Access.ProjectWorkspace.Roots[i]
		if !sameWorkspaceID(left.WorkspaceID, right.WorkspaceID) ||
			!sameFilesystemRoot(left.FilesystemRoot, right.FilesystemRoot) {
			return false
		}
	}
	return true
}

func sameFilesystemRoot(left, right FilesystemRoot) bool {
	if left.LexicalPath != right.LexicalPath || left.RealPath != right.RealPath {
		return false
	}
	if left.Info == nil || right.Info == nil {
		return left.Info == nil && right.Info == nil
	}
	return os.SameFile(left.Info, right.Info)
}

func sameWorkspaceID(left, right *string) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

type FSGuard struct {
	scope                 FileAccessScope
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
	Scope                 FileAccessScope
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
		scope:                 config.Scope.Clone(),
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
		RequestedPath:    requestedPath,
		ResolvedPath:     resolvedPath,
		WorkingDirectory: g.scope.WorkingDirectory.LexicalPath,
	}
	match, denied, denyErr := g.pathDenyPolicy.Check(PathDenyCheck{
		RequestedPath:        requestedPath,
		ResolvedPath:         resolvedPath,
		WorkingDirectoryReal: g.scope.WorkingDirectory.RealPath,
	})
	if denyErr != nil {
		return "", denyErr
	}
	if denied {
		return "", g.noPermission(requestedPath, match.Message)
	}
	if !g.workspaceOnly {
		return resolvedPath, nil
	}
	insideWorkspace, containmentErr := g.isWithinTrustedRoot(resolvedPath)
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

// LexicalPathForDenyPolicy resolves a requested tool path for deny-policy
// matching without following symlinks below the workspace root.
func LexicalPathForDenyPolicy(workspaceRootReal string, requestedPath string) (string, error) {
	if strings.TrimSpace(requestedPath) == "" {
		return "", errors.New("path is required")
	}
	path := requestedPath
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspaceRootReal, path)
	}
	return filepath.Clean(path), nil
}

func (g FSGuard) isWithinTrustedRoot(real string) (bool, error) {
	roots := make([]FilesystemRoot, 0, len(g.scope.ProjectWorkspace.Roots)+1)
	roots = append(roots, g.scope.ExecutionTargetRoot)
	for _, root := range g.scope.ProjectWorkspace.Roots {
		roots = append(roots, root.FilesystemRoot)
	}
	for _, root := range roots {
		inside, err := isWithinFSGuardRoot(root, real)
		if err != nil {
			return false, err
		}
		if inside {
			return true, nil
		}
	}
	return false, nil
}

func isWithinFSGuardRoot(root FilesystemRoot, real string) (bool, error) {
	if root.RealPath == "" {
		return false, nil
	}
	rel, relErr := filepath.Rel(root.RealPath, real)
	if relErr == nil {
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true, nil
		}
		return false, nil
	}

	if root.Info == nil {
		return false, nil
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
		if os.SameFile(info, root.Info) {
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
