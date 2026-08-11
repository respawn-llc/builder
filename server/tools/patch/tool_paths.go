package patch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"core/server/tools"
	"core/shared/config"
)

type options struct {
	allowOutsideWorkspace    bool
	outsideWorkspaceApprover tools.FileAccessApprover
	pathDenyPolicy           tools.PathDenyPolicy
}

type Option func(*options)

func WithAllowOutsideWorkspace(allow bool) Option {
	return func(options *options) {
		options.allowOutsideWorkspace = allow
	}
}

func WithOutsideWorkspaceApprover(approver tools.FileAccessApprover) Option {
	return func(options *options) {
		options.outsideWorkspaceApprover = approver
	}
}

func WithPathDenyPolicy(policy tools.PathDenyPolicy) Option {
	return func(options *options) {
		options.pathDenyPolicy = policy
	}
}

func (t *Tool) resolvePath(ctx context.Context, path string, mustExist bool, accessCall *tools.FileAccessCall) (string, error) {
	real, err := t.resolvePathTarget(path, mustExist)
	if err != nil {
		return "", err
	}
	outcome := accessCall.Authorize(ctx, path, real)
	if !outcome.IsAllowed() {
		return "", fileAccessFailure(outcome)
	}
	return real, nil
}

func (t *Tool) resolvePathTarget(path string, mustExist bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(t.fileAccess.WorkingDirectory().LexicalPath, candidate)
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
