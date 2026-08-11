package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"core/shared/config"
)

type FileAccessMode uint8

const (
	FileAccessRead FileAccessMode = iota + 1
	FileAccessMutation
)

type FileAccessRequest struct {
	RequestedPath    string
	ResolvedPath     string
	WorkingDirectory string
}

type FileAccessApprovalKind uint8

const (
	FileAccessApprovalDeny FileAccessApprovalKind = iota + 1
	FileAccessApprovalAllowOnce
	FileAccessApprovalAllowSession
	FileAccessApprovalSessionCached
)

type FileAccessApproval struct {
	Kind       FileAccessApprovalKind
	Commentary *string
}

type FileAccessApprover func(context.Context, FileAccessRequest) (FileAccessApproval, error)

type FileAccessOutcomeKind uint8

const (
	FileAccessTargetAccepted FileAccessOutcomeKind = iota + 1
	FileAccessAllowed
	FileAccessDeniedOutsideWorkspace
	FileAccessDeniedByUser
	FileAccessDeniedByPathPolicy
	FileAccessDeniedForeignManagedWorktree
	FileAccessApprovalFailed
	FileAccessPolicyFailed
)

type FileAccessReason string

const (
	FileAccessReasonTrustedRoot     FileAccessReason = "trusted_root"
	FileAccessReasonTemporaryAllow  FileAccessReason = "temporary_allow"
	FileAccessReasonConfiguredAllow FileAccessReason = "configured_allow"
	FileAccessReasonCallAllow       FileAccessReason = "call_allow"
	FileAccessReasonAllowOnce       FileAccessReason = "allow_once"
	FileAccessReasonAllowSession    FileAccessReason = "allow_session"
	FileAccessReasonSessionAllow    FileAccessReason = "session_allow"
)

type FileAccessOutcome struct {
	Kind       FileAccessOutcomeKind
	Request    FileAccessRequest
	Reason     FileAccessReason
	PathDeny   *PathDenyMatch
	Commentary *string
	Cause      error
}

func (o FileAccessOutcome) IsAllowed() bool {
	return o.Kind == FileAccessAllowed
}

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

type FileAccessPolicy struct {
	context               FilesystemContext
	mode                  FileAccessMode
	allowOutsideWorkspace bool
	approver              FileAccessApprover
	pathDenyPolicy        PathDenyPolicy
}

type FileAccessPolicyConfig struct {
	Context               FilesystemContext
	Mode                  FileAccessMode
	AllowOutsideWorkspace bool
	Approver              FileAccessApprover
	PathDenyPolicy        PathDenyPolicy
}

func NewFileAccessPolicy(config FileAccessPolicyConfig) (*FileAccessPolicy, error) {
	if err := ValidateFileAccessScope(config.Context.Access); err != nil {
		return nil, fmt.Errorf("validate file access scope: %w", err)
	}
	if config.Mode != FileAccessRead && config.Mode != FileAccessMutation {
		return nil, fmt.Errorf("unsupported file access mode %d", config.Mode)
	}
	if config.Mode == FileAccessRead && len(config.PathDenyPolicy.rules) > 0 {
		return nil, errors.New("read file access policy cannot contain mutation path-deny rules")
	}
	return &FileAccessPolicy{
		context:               config.Context.Clone(),
		mode:                  config.Mode,
		allowOutsideWorkspace: config.AllowOutsideWorkspace,
		approver:              config.Approver,
		pathDenyPolicy:        config.PathDenyPolicy,
	}, nil
}

type FileAccessCall struct {
	policy   *FileAccessPolicy
	approved map[string]fileAccessCallApproval
}

type fileAccessCallApproval struct {
	identity *string
}

func (a fileAccessCallApproval) matches(path string) bool {
	current := canonicalFileAccessIdentity(path)
	return a.identity != nil && current != nil && *a.identity == *current
}

func (p *FileAccessPolicy) BeginCall() *FileAccessCall {
	return &FileAccessCall{
		policy:   p,
		approved: make(map[string]fileAccessCallApproval),
	}
}

func (p *FileAccessPolicy) WorkingDirectory() FilesystemRoot {
	if p == nil {
		return FilesystemRoot{}
	}
	return p.context.Access.WorkingDirectory
}

func (p *FileAccessPolicy) CheckMutationTarget(requestedPath string, resolvedPath string) FileAccessOutcome {
	req := p.request(requestedPath, resolvedPath)
	if p == nil {
		return FileAccessOutcome{Kind: FileAccessPolicyFailed, Request: req, Cause: errors.New("file access policy is required")}
	}
	if p.mode != FileAccessMutation {
		return FileAccessOutcome{Kind: FileAccessPolicyFailed, Request: req, Cause: errors.New("mutation target check requires mutation file access policy")}
	}
	if p.context.ManagedWorktree != nil && p.context.ManagedWorktree.IsForeignManagedWorktreePath(resolvedPath) {
		return FileAccessOutcome{Kind: FileAccessDeniedForeignManagedWorktree, Request: req}
	}
	match, denied, err := p.pathDenyPolicy.Check(PathDenyCheck{
		RequestedPath:        requestedPath,
		ResolvedPath:         resolvedPath,
		WorkingDirectoryReal: p.context.Access.WorkingDirectory.RealPath,
	})
	if err != nil {
		return FileAccessOutcome{Kind: FileAccessPolicyFailed, Request: req, Cause: err}
	}
	if denied {
		matchCopy := match
		return FileAccessOutcome{Kind: FileAccessDeniedByPathPolicy, Request: req, PathDeny: &matchCopy}
	}
	return FileAccessOutcome{Kind: FileAccessTargetAccepted, Request: req}
}

func (p *FileAccessPolicy) ValidateMutationTarget(resolvedPath string) error {
	if p == nil {
		return errors.New("file access policy is required")
	}
	if p.mode != FileAccessMutation {
		return errors.New("mutation target validation requires mutation file access policy")
	}
	if p.context.ManagedWorktree != nil && p.context.ManagedWorktree.IsForeignManagedWorktreePath(resolvedPath) {
		return ErrForeignManagedWorktreeEdit
	}
	return nil
}

func (c *FileAccessCall) Authorize(ctx context.Context, requestedPath string, resolvedPath string) FileAccessOutcome {
	if c == nil || c.policy == nil {
		return FileAccessOutcome{
			Kind:    FileAccessPolicyFailed,
			Request: FileAccessRequest{RequestedPath: requestedPath, ResolvedPath: resolvedPath},
			Cause:   errors.New("file access call is required"),
		}
	}
	p := c.policy
	req := p.request(requestedPath, resolvedPath)
	if p.mode == FileAccessMutation {
		outcome := p.CheckMutationTarget(requestedPath, resolvedPath)
		if outcome.Kind != FileAccessTargetAccepted {
			return outcome
		}
	}
	insideWorkspace, containmentErr := p.isWithinTrustedRoot(resolvedPath)
	if containmentErr != nil {
		return FileAccessOutcome{
			Kind:    FileAccessPolicyFailed,
			Request: req,
			Cause:   fmt.Errorf("workspace boundary check for %q: %w", requestedPath, containmentErr),
		}
	}
	if insideWorkspace {
		return FileAccessOutcome{Kind: FileAccessAllowed, Request: req, Reason: FileAccessReasonTrustedRoot}
	}
	if IsPathInTemporaryDir(resolvedPath) {
		return FileAccessOutcome{Kind: FileAccessAllowed, Request: req, Reason: FileAccessReasonTemporaryAllow}
	}
	if p.allowOutsideWorkspace {
		return FileAccessOutcome{Kind: FileAccessAllowed, Request: req, Reason: FileAccessReasonConfiguredAllow}
	}
	if approval, ok := c.approved[resolvedPath]; ok {
		if approval.matches(resolvedPath) {
			return FileAccessOutcome{Kind: FileAccessAllowed, Request: req, Reason: FileAccessReasonCallAllow}
		}
		delete(c.approved, resolvedPath)
	}
	if p.approver == nil {
		return FileAccessOutcome{Kind: FileAccessDeniedOutsideWorkspace, Request: req}
	}
	approvalIdentity := canonicalFileAccessIdentity(resolvedPath)
	approval, approveErr := p.approver(ctx, req)
	if approveErr != nil {
		return FileAccessOutcome{Kind: FileAccessApprovalFailed, Request: req, Cause: approveErr}
	}
	switch approval.Kind {
	case FileAccessApprovalAllowOnce:
		c.rememberApproval(resolvedPath, approvalIdentity)
		return FileAccessOutcome{Kind: FileAccessAllowed, Request: req, Reason: FileAccessReasonAllowOnce}
	case FileAccessApprovalAllowSession:
		c.rememberApproval(resolvedPath, approvalIdentity)
		return FileAccessOutcome{Kind: FileAccessAllowed, Request: req, Reason: FileAccessReasonAllowSession}
	case FileAccessApprovalSessionCached:
		c.rememberApproval(resolvedPath, approvalIdentity)
		return FileAccessOutcome{Kind: FileAccessAllowed, Request: req, Reason: FileAccessReasonSessionAllow}
	case FileAccessApprovalDeny:
		return FileAccessOutcome{
			Kind:       FileAccessDeniedByUser,
			Request:    req,
			Commentary: cloneOptionalString(approval.Commentary),
		}
	default:
		return FileAccessOutcome{
			Kind:    FileAccessPolicyFailed,
			Request: req,
			Cause:   fmt.Errorf("unsupported file access approval kind %d", approval.Kind),
		}
	}
}

func (c *FileAccessCall) ReuseApproval(fromResolvedPath string, toResolvedPath string) bool {
	if c == nil || strings.TrimSpace(fromResolvedPath) == "" || strings.TrimSpace(toResolvedPath) == "" {
		return false
	}
	approval, ok := c.approved[fromResolvedPath]
	if !ok || approval.identity == nil {
		return false
	}
	if info, err := os.Lstat(fromResolvedPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	targetIdentity, err := config.CanonicalPathIdentity(toResolvedPath)
	if err != nil || targetIdentity != *approval.identity {
		return false
	}
	c.approved[toResolvedPath] = approval
	return true
}

func (c *FileAccessCall) rememberApproval(resolvedPath string, identity *string) {
	c.approved[resolvedPath] = fileAccessCallApproval{identity: identity}
}

func canonicalFileAccessIdentity(path string) *string {
	identity, err := config.CanonicalPathIdentity(path)
	if err != nil {
		return nil
	}
	return &identity
}

func (p *FileAccessPolicy) request(requestedPath string, resolvedPath string) FileAccessRequest {
	workingDirectory := ""
	if p != nil {
		workingDirectory = p.context.Access.WorkingDirectory.LexicalPath
	}
	return FileAccessRequest{
		RequestedPath:    requestedPath,
		ResolvedPath:     resolvedPath,
		WorkingDirectory: workingDirectory,
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

func (p *FileAccessPolicy) isWithinTrustedRoot(real string) (bool, error) {
	roots := make([]FilesystemRoot, 0, len(p.context.Access.ProjectWorkspace.Roots)+1)
	roots = append(roots, p.context.Access.ExecutionTargetRoot)
	for _, root := range p.context.Access.ProjectWorkspace.Roots {
		roots = append(roots, root.FilesystemRoot)
	}
	for _, root := range roots {
		inside, err := filesystemRootContains(root, real)
		if err != nil {
			return false, err
		}
		if inside {
			return true, nil
		}
	}
	return false, nil
}

// FilesystemRootContains reports whether real is inside root using the same
// identity-aware containment rules as native file-access authorization.
func FilesystemRootContains(root FilesystemRoot, real string) (bool, error) {
	return filesystemRootContains(root, real)
}

func filesystemRootContains(root FilesystemRoot, real string) (bool, error) {
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

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
