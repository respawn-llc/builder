package tools

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"core/shared/config"
)

type ManagedWorktreePathContext struct {
	baseRoot     string
	managedRoots []string
	currentRoot  *string
}

const ForeignManagedWorktreeEditDeniedMessage = "Directly reaching into another agent's worktree is not permitted. Enter the worktree first instead with `kent worktree enter`"

func NewManagedWorktreePathContext(baseDir string, currentWorktreeRoot *string) (*ManagedWorktreePathContext, error) {
	base, err := config.ResolveExistingPathRealPath(strings.TrimSpace(baseDir))
	if err != nil {
		return nil, fmt.Errorf("resolve managed worktree base: %w", err)
	}
	context := &ManagedWorktreePathContext{baseRoot: base}
	return context.withCurrentWorktreeRoot(currentWorktreeRoot)
}

// NewManagedWorktreePathContextForRoots creates a managed-worktree policy from
// metadata-owned managed roots. This keeps denial effective even when the
// active client settings omit the configured base directory.
func NewManagedWorktreePathContextForRoots(roots []string, currentWorktreeRoot *string) (*ManagedWorktreePathContext, error) {
	context := &ManagedWorktreePathContext{}
	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		real, err := config.ResolveExistingAncestorRealPath(strings.TrimSpace(root))
		if err != nil {
			return nil, fmt.Errorf("resolve managed worktree root: %w", err)
		}
		if _, exists := seen[real]; exists {
			continue
		}
		seen[real] = struct{}{}
		context.managedRoots = append(context.managedRoots, real)
	}
	if len(context.managedRoots) == 0 {
		return nil, fmt.Errorf("managed worktree roots are required")
	}
	return context.withCurrentWorktreeRoot(currentWorktreeRoot)
}

func (c *ManagedWorktreePathContext) withCurrentWorktreeRoot(currentWorktreeRoot *string) (*ManagedWorktreePathContext, error) {
	if currentWorktreeRoot == nil {
		return c, nil
	}
	current, err := config.ResolveExistingPathRealPath(*currentWorktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve current managed worktree root: %w", err)
	}
	if c.baseRoot != "" && !pathWithin(c.baseRoot, current) {
		return c, nil
	}
	if len(c.managedRoots) > 0 {
		for _, root := range c.managedRoots {
			if pathWithin(root, current) {
				c.currentRoot = &current
				return c, nil
			}
		}
		return c, nil
	}
	c.currentRoot = &current
	return c, nil
}

func (c ManagedWorktreePathContext) IsForeignManagedWorktreePath(requestedPath string, resolvedPath string) bool {
	if !filepath.IsAbs(requestedPath) {
		return false
	}
	managed := false
	if len(c.managedRoots) > 0 {
		for _, root := range c.managedRoots {
			if pathWithin(root, resolvedPath) {
				managed = true
				break
			}
		}
	} else {
		managed = c.baseRoot != "" && pathWithin(c.baseRoot, resolvedPath)
	}
	if !managed {
		return false
	}
	return c.currentRoot == nil || !pathWithin(*c.currentRoot, resolvedPath)
}

func (c *ManagedWorktreePathContext) WithCurrentWorktreeRoot(currentWorktreeRoot *string) (*ManagedWorktreePathContext, error) {
	if c == nil {
		return nil, nil
	}
	next := &ManagedWorktreePathContext{baseRoot: c.baseRoot, managedRoots: append([]string(nil), c.managedRoots...)}
	return next.withCurrentWorktreeRoot(currentWorktreeRoot)
}

func (c *ManagedWorktreePathContext) Equal(other *ManagedWorktreePathContext) bool {
	if c == nil || other == nil {
		return c == other
	}
	if c.baseRoot != other.baseRoot || !slices.Equal(c.managedRoots, other.managedRoots) {
		return false
	}
	if c.currentRoot == nil || other.currentRoot == nil {
		return c.currentRoot == nil && other.currentRoot == nil
	}
	return *c.currentRoot == *other.currentRoot
}

func pathWithin(root string, path string) bool {
	rootIdentity, err := config.CanonicalLexicalPathIdentity(root)
	if err != nil {
		return false
	}
	pathIdentity, err := config.CanonicalLexicalPathIdentity(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootIdentity, pathIdentity)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
