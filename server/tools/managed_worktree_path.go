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
	currentRoot  *string
	managedRoots []string
}

const ForeignManagedWorktreeEditDeniedMessage = "Directly reaching into another agent's worktree is not permitted. Enter the worktree first instead with `kent worktree enter`"

func NewManagedWorktreePathContext(baseDir string, currentWorktreeRoot *string, managedWorktreeRoots []string) (*ManagedWorktreePathContext, error) {
	base, err := config.ResolveExistingPathRealPath(strings.TrimSpace(baseDir))
	if err != nil {
		return nil, fmt.Errorf("resolve managed worktree base: %w", err)
	}
	context := &ManagedWorktreePathContext{
		baseRoot:     base,
		managedRoots: make([]string, 0, len(managedWorktreeRoots)),
	}
	if currentWorktreeRoot != nil {
		current, err := config.ResolveExistingPathRealPath(*currentWorktreeRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve current managed worktree root: %w", err)
		}
		if !pathWithin(base, current) {
			return nil, fmt.Errorf("current managed worktree root %q is outside managed worktree base %q", current, base)
		}
		context.currentRoot = &current
	}
	for _, root := range managedWorktreeRoots {
		trimmedRoot := strings.TrimSpace(root)
		if trimmedRoot == "" {
			return nil, fmt.Errorf("managed worktree root is required")
		}
		resolved, err := config.ResolveExistingAncestorRealPath(trimmedRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve managed worktree root: %w", err)
		}
		if !pathWithin(base, resolved) {
			return nil, fmt.Errorf("managed worktree root %q is outside managed worktree base %q", resolved, base)
		}
		if !slices.Contains(context.managedRoots, resolved) {
			context.managedRoots = append(context.managedRoots, resolved)
		}
	}
	slices.Sort(context.managedRoots)
	return context, nil
}

func (c ManagedWorktreePathContext) IsForeignManagedWorktreePath(resolvedPath string) bool {
	if !pathWithin(c.baseRoot, resolvedPath) {
		return false
	}
	if c.currentRoot != nil && pathWithin(*c.currentRoot, resolvedPath) {
		return false
	}
	for _, managedRoot := range c.managedRoots {
		if pathWithin(managedRoot, resolvedPath) {
			return true
		}
	}
	return false
}

func (c *ManagedWorktreePathContext) WithCurrentWorktreeRoot(currentWorktreeRoot *string) (*ManagedWorktreePathContext, error) {
	if c == nil {
		return nil, nil
	}
	next := &ManagedWorktreePathContext{
		baseRoot:     c.baseRoot,
		managedRoots: append([]string(nil), c.managedRoots...),
	}
	if currentWorktreeRoot == nil {
		return next, nil
	}
	current, err := config.ResolveExistingPathRealPath(*currentWorktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve current managed worktree root: %w", err)
	}
	if !pathWithin(c.baseRoot, current) {
		return nil, fmt.Errorf("current managed worktree root %q is outside managed worktree base %q", current, c.baseRoot)
	}
	next.currentRoot = &current
	return next, nil
}

func (c *ManagedWorktreePathContext) Equal(other *ManagedWorktreePathContext) bool {
	if c == nil || other == nil {
		return c == other
	}
	if c.baseRoot != other.baseRoot {
		return false
	}
	if !slices.Equal(c.managedRoots, other.managedRoots) {
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
