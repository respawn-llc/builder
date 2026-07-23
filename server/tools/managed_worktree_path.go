package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"core/shared/config"
)

type ManagedWorktreePathContext struct {
	baseRoot    string
	currentRoot *string
}

func NewManagedWorktreePathContext(baseDir string, currentWorktreeRoot *string) (*ManagedWorktreePathContext, error) {
	base, err := config.ResolveExistingPathRealPath(strings.TrimSpace(baseDir))
	if err != nil {
		return nil, fmt.Errorf("resolve managed worktree base: %w", err)
	}
	context := &ManagedWorktreePathContext{baseRoot: base}
	if currentWorktreeRoot == nil {
		return context, nil
	}
	current, err := config.ResolveExistingPathRealPath(*currentWorktreeRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve current managed worktree root: %w", err)
	}
	if !pathWithin(base, current) {
		return nil, fmt.Errorf("current managed worktree root %q is outside managed worktree base %q", current, base)
	}
	context.currentRoot = &current
	return context, nil
}

func (c ManagedWorktreePathContext) WarnsFor(requestedPath string, resolvedPath string) bool {
	if !filepath.IsAbs(requestedPath) || !pathWithin(c.baseRoot, resolvedPath) {
		return false
	}
	return c.currentRoot == nil || !pathWithin(*c.currentRoot, resolvedPath)
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
