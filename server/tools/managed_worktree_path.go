package tools

import (
	"errors"
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

var ErrForeignManagedWorktreeEdit = errors.New(ForeignManagedWorktreeEditDeniedMessage)

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
		for _, managedRoot := range context.managedRoots {
			if properlyNestedPaths(managedRoot, resolved) {
				return nil, fmt.Errorf("managed worktree roots %q and %q overlap", managedRoot, resolved)
			}
		}
		if !slices.Contains(context.managedRoots, resolved) {
			context.managedRoots = append(context.managedRoots, resolved)
		}
	}
	slices.Sort(context.managedRoots)
	if context.currentRoot != nil {
		if err := validateCurrentManagedWorktreeRoot(*context.currentRoot, context.managedRoots); err != nil {
			return nil, err
		}
	}
	return context, nil
}

func (c ManagedWorktreePathContext) IsForeignManagedWorktreePath(resolvedPath string) bool {
	if c.currentRoot != nil && pathWithin(*c.currentRoot, resolvedPath) {
		return false
	}
	return pathWithin(c.baseRoot, resolvedPath)
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
	if err := validateCurrentManagedWorktreeRoot(current, next.managedRoots); err != nil {
		return nil, err
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

func validateCurrentManagedWorktreeRoot(current string, managedRoots []string) error {
	for _, managedRoot := range managedRoots {
		if samePathIdentity(current, managedRoot) {
			continue
		}
		if properlyNestedPaths(current, managedRoot) {
			return fmt.Errorf("managed worktree root %q overlaps current managed worktree root %q", managedRoot, current)
		}
	}
	return nil
}

func properlyNestedPaths(first string, second string) bool {
	return !samePathIdentity(first, second) && (pathWithin(first, second) || pathWithin(second, first))
}

func samePathIdentity(first string, second string) bool {
	return pathWithin(first, second) && pathWithin(second, first)
}
