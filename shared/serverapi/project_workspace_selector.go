package serverapi

import (
	"errors"
	"fmt"
	"strings"
)

type ProjectWorkspaceSelector struct {
	WorkspaceID   *string `json:"workspace_id,omitempty"`
	WorkspaceRoot *string `json:"workspace_root,omitempty"`
}

func NewProjectWorkspaceSelectorForID(workspaceID string) (ProjectWorkspaceSelector, error) {
	trimmed, err := normalizeWorkspaceSelectorArm(&workspaceID, "workspace_id")
	if err != nil {
		return ProjectWorkspaceSelector{}, err
	}
	return ProjectWorkspaceSelector{WorkspaceID: trimmed}, nil
}

func NewProjectWorkspaceSelectorForRoot(workspaceRoot string) (ProjectWorkspaceSelector, error) {
	trimmed, err := normalizeWorkspaceSelectorArm(&workspaceRoot, "workspace_root")
	if err != nil {
		return ProjectWorkspaceSelector{}, err
	}
	return ProjectWorkspaceSelector{WorkspaceRoot: trimmed}, nil
}

func (s ProjectWorkspaceSelector) Validate() error {
	id, idErr := normalizeWorkspaceSelectorArm(s.WorkspaceID, "workspace_id")
	root, rootErr := normalizeWorkspaceSelectorArm(s.WorkspaceRoot, "workspace_root")
	if idErr != nil {
		return idErr
	}
	if rootErr != nil {
		return rootErr
	}
	if (id == nil) == (root == nil) {
		return errors.New("exactly one workspace_id or workspace_root is required")
	}
	return nil
}

func (s ProjectWorkspaceSelector) WorkspaceIDValue() *string {
	value, _ := normalizeWorkspaceSelectorArm(s.WorkspaceID, "workspace_id")
	return value
}

func (s ProjectWorkspaceSelector) WorkspaceRootValue() *string {
	value, _ := normalizeWorkspaceSelectorArm(s.WorkspaceRoot, "workspace_root")
	return value
}

func normalizeWorkspaceSelectorArm(value *string, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, fmt.Errorf("%s is required", field)
	}
	return &trimmed, nil
}
