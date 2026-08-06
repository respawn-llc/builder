package metadata

import (
	"errors"
	"strings"
)

const ProjectWorkspaceCollectionLimit = 500

type ProjectWorkspace struct {
	WorkspaceID       *string
	CanonicalRoot     string
	AttachmentOrdinal int
}

type ProjectManagedWorktree struct {
	WorkspaceID   string
	CanonicalRoot string
}

type ProjectWorkspaceBoundary struct {
	ProjectID        string
	Workspaces       []ProjectWorkspace
	ManagedWorktrees []ProjectManagedWorktree
}

func (b ProjectWorkspaceBoundary) Clone() ProjectWorkspaceBoundary {
	b.Workspaces = append([]ProjectWorkspace(nil), b.Workspaces...)
	b.ManagedWorktrees = append([]ProjectManagedWorktree(nil), b.ManagedWorktrees...)
	return b
}

func (b ProjectWorkspaceBoundary) Normalize() (ProjectWorkspaceBoundary, error) {
	projectID := strings.TrimSpace(b.ProjectID)
	if projectID == "" {
		return ProjectWorkspaceBoundary{}, errors.New("project id is required")
	}
	normalized := ProjectWorkspaceBoundary{
		ProjectID:        projectID,
		Workspaces:       make([]ProjectWorkspace, 0, len(b.Workspaces)),
		ManagedWorktrees: make([]ProjectManagedWorktree, 0, len(b.ManagedWorktrees)),
	}
	seen := make(map[string]struct{}, len(b.Workspaces))
	for _, workspace := range b.Workspaces {
		root := strings.TrimSpace(workspace.CanonicalRoot)
		if root == "" {
			return ProjectWorkspaceBoundary{}, errors.New("project workspace root is required")
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		workspace.CanonicalRoot = root
		workspace.AttachmentOrdinal = len(normalized.Workspaces)
		normalized.Workspaces = append(normalized.Workspaces, workspace)
	}
	if len(normalized.Workspaces) > ProjectWorkspaceCollectionLimit {
		return ProjectWorkspaceBoundary{}, errors.New("project workspace boundary exceeds collection limit")
	}
	managedSeen := make(map[string]struct{}, len(b.ManagedWorktrees))
	for _, worktree := range b.ManagedWorktrees {
		workspaceID := strings.TrimSpace(worktree.WorkspaceID)
		root := strings.TrimSpace(worktree.CanonicalRoot)
		if workspaceID == "" {
			return ProjectWorkspaceBoundary{}, errors.New("project managed worktree workspace id is required")
		}
		if root == "" {
			return ProjectWorkspaceBoundary{}, errors.New("project managed worktree root is required")
		}
		key := workspaceID + "\x00" + root
		if _, exists := managedSeen[key]; exists {
			continue
		}
		managedSeen[key] = struct{}{}
		normalized.ManagedWorktrees = append(normalized.ManagedWorktrees, ProjectManagedWorktree{
			WorkspaceID: workspaceID, CanonicalRoot: root,
		})
	}
	return normalized, nil
}

func (b ProjectWorkspaceBoundary) Validate() error {
	_, err := b.Normalize()
	return err
}

func (b ProjectWorkspaceBoundary) WithWorkspace(workspace ProjectWorkspace) (ProjectWorkspaceBoundary, bool, error) {
	normalized, err := b.Normalize()
	if err != nil {
		return ProjectWorkspaceBoundary{}, false, err
	}
	workspace.CanonicalRoot = strings.TrimSpace(workspace.CanonicalRoot)
	if workspace.CanonicalRoot == "" {
		return ProjectWorkspaceBoundary{}, false, errors.New("project workspace root is required")
	}
	for _, existing := range normalized.Workspaces {
		if existing.CanonicalRoot == workspace.CanonicalRoot {
			return normalized, false, nil
		}
	}
	next := normalized.Clone()
	workspace.AttachmentOrdinal = 0
	next.Workspaces = append([]ProjectWorkspace{workspace}, next.Workspaces...)
	if len(next.Workspaces) > ProjectWorkspaceCollectionLimit {
		next.Workspaces = next.Workspaces[:ProjectWorkspaceCollectionLimit]
	}
	for index := range next.Workspaces {
		next.Workspaces[index].AttachmentOrdinal = index
	}
	return next, true, nil
}
