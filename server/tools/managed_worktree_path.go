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
	baseRoot         string
	currentRoot      *string
	managedRoots     []string
	baseRootResolver ManagedWorktreeBaseRootResolver
}

type ManagedWorktreeBaseRootResolver func() (string, error)

const ForeignManagedWorktreeEditDeniedMessage = "Directly reaching into another agent's worktree is not permitted. Enter the worktree first instead with `kent worktree enter`"

var ErrForeignManagedWorktreeEditDenied = errors.New(ForeignManagedWorktreeEditDeniedMessage)

func NewManagedWorktreePathContext(
	baseDir string,
	currentWorktreeRoot *string,
	managedWorktreeRoots []string,
	baseRootResolver ManagedWorktreeBaseRootResolver,
) (*ManagedWorktreePathContext, error) {
	if baseRootResolver == nil {
		return nil, errors.New("managed worktree base root resolver is required")
	}
	base, err := config.ResolveExistingPathRealPath(strings.TrimSpace(baseDir))
	if err != nil {
		return nil, fmt.Errorf("resolve managed worktree base: %w", err)
	}
	context := &ManagedWorktreePathContext{
		baseRoot:         base,
		managedRoots:     make([]string, 0, len(managedWorktreeRoots)),
		baseRootResolver: baseRootResolver,
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

func (c ManagedWorktreePathContext) CheckMutationPath(resolvedPath string) error {
	configuredBaseRoot, err := c.baseRootResolver()
	if err != nil {
		return fmt.Errorf("resolve configured managed worktree root: %w", err)
	}
	baseRoot, err := config.ResolveExistingPathRealPath(strings.TrimSpace(configuredBaseRoot))
	if err != nil {
		return fmt.Errorf("resolve configured managed worktree root: %w", err)
	}
	if c.isForeignManagedWorktreePath(baseRoot, resolvedPath) {
		return ErrForeignManagedWorktreeEditDenied
	}
	return nil
}

func (c ManagedWorktreePathContext) isForeignManagedWorktreePath(baseRoot string, resolvedPath string) bool {
	if c.currentRoot != nil && pathWithin(*c.currentRoot, resolvedPath) {
		return false
	}
	return pathWithin(baseRoot, resolvedPath)
}

func (c *ManagedWorktreePathContext) WithCurrentWorktreeRoot(currentWorktreeRoot *string) (*ManagedWorktreePathContext, error) {
	if c == nil {
		return nil, nil
	}
	next := &ManagedWorktreePathContext{
		baseRoot:         c.baseRoot,
		managedRoots:     append([]string(nil), c.managedRoots...),
		baseRootResolver: c.baseRootResolver,
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
