package serverapi

import (
	"core/shared/runtimeids"
	"errors"
	"fmt"
	"strings"

	"core/shared/clientui"
)

type ProjectListRequest struct{}

type ProjectListResponse struct {
	Projects []clientui.ProjectSummary
}

type ProjectBinding struct {
	ProjectID       string `json:"project_id"`
	ProjectKey      string `json:"project_key"`
	ProjectName     string `json:"project_name"`
	WorkspaceID     string `json:"workspace_id"`
	CanonicalRoot   string `json:"canonical_root"`
	WorkspaceName   string `json:"workspace_name"`
	WorkspaceStatus string `json:"workspace_status"`
}

type ProjectResolvePathRequest struct {
	Path string `json:"path"`
}

type ProjectResolvePathResponse struct {
	CanonicalRoot    string                       `json:"canonical_root"`
	PathAvailability clientui.ProjectAvailability `json:"path_availability"`
	Binding          *ProjectBinding              `json:"binding,omitempty"`
}

type ProjectBindingPlanMode string

const (
	ProjectBindingPlanModeInteractive ProjectBindingPlanMode = "interactive"
	ProjectBindingPlanModeHeadless    ProjectBindingPlanMode = "headless"
)

type ProjectBindingPlanKind string

const (
	ProjectBindingPlanKindBound                    ProjectBindingPlanKind = "bound"
	ProjectBindingPlanKindLocalUnbound             ProjectBindingPlanKind = "local_unbound"
	ProjectBindingPlanKindServerWorkspaceSelection ProjectBindingPlanKind = "server_workspace_selection"
	ProjectBindingPlanKindHeadlessRemoteSelected   ProjectBindingPlanKind = "headless_remote_selected"
	ProjectBindingPlanKindHeadlessRemoteAmbiguous  ProjectBindingPlanKind = "headless_remote_ambiguous"
)

type ProjectBindingPlanRequest struct {
	Path string                 `json:"path"`
	Mode ProjectBindingPlanMode `json:"mode"`
}

type ProjectBindingPlanResponse struct {
	Kind             ProjectBindingPlanKind        `json:"kind"`
	CanonicalRoot    string                        `json:"canonical_root"`
	PathAvailability clientui.ProjectAvailability  `json:"path_availability"`
	Binding          *ProjectBinding               `json:"binding,omitempty"`
	Projects         []clientui.ProjectSummary     `json:"projects,omitempty"`
	Workspace        *ProjectWorkspacePlanSelected `json:"workspace,omitempty"`
}

type ProjectWorkspacePlanSelected struct {
	ProjectID   string `json:"project_id"`
	WorkspaceID string `json:"workspace_id"`
}

type ProjectCreateRequest struct {
	DisplayName   string `json:"display_name"`
	ProjectKey    string `json:"project_key,omitempty"`
	WorkspaceRoot string `json:"workspace_root"`
}

type ProjectCreateResponse struct {
	Binding ProjectBinding `json:"binding"`
}

type ProjectHomeListRequest struct {
	PageSize  int    `json:"page_size"`
	PageToken string `json:"page_token"`
}

type ProjectHomeListResponse struct {
	Projects          []ProjectHomeSummary `json:"projects"`
	NextPageToken     string               `json:"next_page_token"`
	GeneratedAtUnixMs int64                `json:"generated_at_unix_ms"`
}

type ProjectHomeSummary struct {
	ProjectID            string                  `json:"project_id"`
	ProjectKey           string                  `json:"project_key"`
	DisplayName          string                  `json:"display_name"`
	PrimaryWorkspace     ProjectWorkspaceSummary `json:"primary_workspace"`
	DefaultWorkflowID    *runtimeids.WorkflowID  `json:"default_workflow_id"`
	DefaultWorkflowName  string                  `json:"default_workflow_name,omitempty"`
	DefaultWorkflowValid bool                    `json:"default_workflow_valid"`
	UpdatedAtUnixMs      int64                   `json:"updated_at_unix_ms"`
	TaskCount            int                     `json:"task_count"`
	AttentionCount       int                     `json:"attention_count"`
	WorkflowCount        int                     `json:"workflow_count"`
}

const MaxProjectWorkspacePageSize = 100

type ProjectWorkspaceListRequest struct {
	ProjectID string `json:"project_id"`
	Offset    int    `json:"offset"`
	Limit     int    `json:"limit"`
}

type ProjectWorkspaceListResponse struct {
	ProjectID  string                       `json:"project_id"`
	Offset     int                          `json:"offset"`
	Workspaces []ProjectWorkspaceCatalogRow `json:"workspaces"`
	NextOffset *int                         `json:"next_offset"`
}

type ProjectWorkspaceGetResult string

const (
	ProjectWorkspaceGetResultAttached    ProjectWorkspaceGetResult = "attached"
	ProjectWorkspaceGetResultNotAttached ProjectWorkspaceGetResult = "not_attached"
)

type ProjectWorkspaceGetRequest struct {
	ProjectID string `json:"project_id"`
	ProjectWorkspaceSelector
}

type ProjectWorkspaceGetResponse struct {
	ProjectID string                      `json:"project_id"`
	Result    ProjectWorkspaceGetResult   `json:"result"`
	Workspace *ProjectWorkspaceCatalogRow `json:"workspace"`
}

type ProjectWorkspaceCatalogRow struct {
	WorkspaceID string `json:"workspace_id"`
	DisplayName string `json:"display_name"`
	RootPath    string `json:"root_path"`
	IsDefault   bool   `json:"is_default"`
}

func (r ProjectWorkspaceCatalogRow) Validate() error {
	if strings.TrimSpace(r.WorkspaceID) == "" || strings.TrimSpace(r.WorkspaceID) != r.WorkspaceID {
		return errors.New("workspace_id is invalid")
	}
	if strings.TrimSpace(r.RootPath) == "" || strings.TrimSpace(r.RootPath) != r.RootPath {
		return errors.New("root_path is invalid")
	}
	if strings.TrimSpace(r.DisplayName) == "" && !IsFilesystemRootPath(r.RootPath) {
		return errors.New("display_name must not be blank")
	}
	if strings.TrimSpace(r.DisplayName) != r.DisplayName {
		return errors.New("display_name must not have leading or trailing whitespace")
	}
	return nil
}

func (r ProjectWorkspaceListResponse) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return errors.New("project_id is invalid")
	}
	if r.Offset < 0 {
		return errors.New("offset must be non-negative")
	}
	if r.Workspaces == nil {
		return errors.New("workspaces must be an array")
	}
	if len(r.Workspaces) > MaxProjectWorkspacePageSize {
		return fmt.Errorf("workspace page exceeds maximum size %d", MaxProjectWorkspacePageSize)
	}
	for index, workspace := range r.Workspaces {
		if err := workspace.Validate(); err != nil {
			return fmt.Errorf("workspaces[%d]: %w", index, err)
		}
	}
	if r.NextOffset != nil && *r.NextOffset <= 0 {
		return errors.New("next_offset must be positive")
	}
	return nil
}

func (r ProjectWorkspaceGetResponse) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" || strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return errors.New("project_id is invalid")
	}
	switch r.Result {
	case ProjectWorkspaceGetResultAttached:
		if r.Workspace == nil {
			return errors.New("attached result requires workspace")
		}
		return r.Workspace.Validate()
	case ProjectWorkspaceGetResultNotAttached:
		if r.Workspace != nil {
			return errors.New("not_attached result must not contain workspace")
		}
		return nil
	default:
		return errors.New("result is invalid")
	}
}

type ProjectEditGetRequest struct {
	ProjectID string `json:"project_id"`
}

type ProjectEditGetResponse struct {
	ProjectID   string `json:"project_id"`
	ProjectKey  string `json:"project_key"`
	DisplayName string `json:"display_name"`
}

type ProjectWorkspaceSummary struct {
	WorkspaceID     string `json:"workspace_id"`
	DisplayName     string `json:"display_name"`
	RootPath        string `json:"root_path"`
	Availability    string `json:"availability"`
	IsPrimary       bool   `json:"is_primary"`
	UpdatedAtUnixMs int64  `json:"updated_at_unix_ms"`
}

type ProjectUpdateRequest struct {
	ProjectID   string `json:"project_id"`
	DisplayName string `json:"display_name"`
	// ProjectKey, when non-empty, renames the project key used as the prefix for
	// future task short IDs. Empty leaves the existing key unchanged. Existing
	// task short IDs are frozen at creation and are not rewritten by a rename.
	ProjectKey string `json:"project_key,omitempty"`
}

type ProjectUpdateResponse struct {
	Project ProjectHomeSummary `json:"project"`
}

type ProjectDefaultWorkspaceSetRequest struct {
	ProjectID string `json:"project_id"`
	ProjectWorkspaceSelector
}

type ProjectDefaultWorkspaceSetResponse struct {
	Project ProjectHomeSummary `json:"project"`
}

type ProjectWorkspaceUnlinkRequest struct {
	ProjectID string `json:"project_id"`
	ProjectWorkspaceSelector
}

type ProjectWorkspaceUnlinkResponse struct {
	ProjectID   string                          `json:"project_id"`
	WorkspaceID string                          `json:"workspace_id"`
	Blockers    []ProjectWorkspaceUnlinkBlocker `json:"blockers,omitempty"`
	Project     *ProjectHomeSummary             `json:"project,omitempty"`
}

type ProjectWorkspaceUnlinkBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

func (s ProjectHomeSummary) Validate() error {
	for field, value := range map[string]string{
		"project_id":               s.ProjectID,
		"display_name":             s.DisplayName,
		"primary_workspace_id":     s.PrimaryWorkspace.WorkspaceID,
		"primary_workspace_root":   s.PrimaryWorkspace.RootPath,
		"primary_workspace_status": s.PrimaryWorkspace.Availability,
	} {
		if strings.TrimSpace(value) == "" {
			return errors.New(field + " must not be blank")
		}
	}
	if strings.TrimSpace(s.PrimaryWorkspace.DisplayName) == "" && !IsFilesystemRootPath(s.PrimaryWorkspace.RootPath) {
		return errors.New("primary_workspace_name must not be blank")
	}
	if s.DefaultWorkflowID == nil {
		if strings.TrimSpace(s.DefaultWorkflowName) != "" {
			return errors.New("default workflow name must be absent when no workflow is present")
		}
		if s.DefaultWorkflowValid {
			return errors.New("default workflow validity must be false when no workflow is present")
		}
		return nil
	}
	if s.DefaultWorkflowID.IsZero() {
		return errors.New("default_workflow_id must not be zero when present")
	}
	if strings.TrimSpace(s.DefaultWorkflowName) == "" {
		return errors.New("default_workflow_name must not be blank when present")
	}
	if !s.DefaultWorkflowValid {
		return errors.New("default workflow validity must be true when a workflow is present")
	}
	return nil
}

// IsFilesystemRootPath recognizes filesystem roots without applying local OS path rules.
func IsFilesystemRootPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "/" {
		return true
	}
	if isWindowsDriveRoot(trimmed) {
		return true
	}
	return isWindowsUNCRoot(trimmed)
}

func isWindowsDriveRoot(path string) bool {
	return len(path) == 3 &&
		((path[0] >= 'a' && path[0] <= 'z') || (path[0] >= 'A' && path[0] <= 'Z')) &&
		path[1] == ':' &&
		isPathSeparator(path[2])
}

func isWindowsUNCRoot(path string) bool {
	if len(path) < 5 || !isPathSeparator(path[0]) || !isPathSeparator(path[1]) {
		return false
	}
	index := 2
	serverStart := index
	for index < len(path) && !isPathSeparator(path[index]) {
		index++
	}
	if index == serverStart {
		return false
	}
	for index < len(path) && isPathSeparator(path[index]) {
		index++
	}
	shareStart := index
	for index < len(path) && !isPathSeparator(path[index]) {
		index++
	}
	if index == shareStart {
		return false
	}
	for index < len(path) {
		if !isPathSeparator(path[index]) {
			return false
		}
		index++
	}
	return true
}

func isPathSeparator(value byte) bool {
	return value == '/' || value == '\\'
}

func (r ProjectDefaultWorkspaceSetResponse) Validate() error {
	return r.Project.Validate()
}

func (r ProjectWorkspaceUnlinkResponse) Validate() error {
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id must not be blank")
	}
	if strings.TrimSpace(r.WorkspaceID) == "" {
		return errors.New("workspace_id must not be blank")
	}
	for _, blocker := range r.Blockers {
		if strings.TrimSpace(blocker.Code) == "" {
			return errors.New("unlink blocker code must not be blank")
		}
		if strings.TrimSpace(blocker.Message) == "" {
			return errors.New("unlink blocker message must not be blank")
		}
		if blocker.Count < 0 {
			return errors.New("unlink blocker count must not be negative")
		}
	}
	if r.Project != nil {
		if err := r.Project.Validate(); err != nil {
			return fmt.Errorf("unlink response project: %w", err)
		}
	}
	return nil
}

type ProjectDeleteRequest struct {
	ProjectID string `json:"project_id"`
}

type ProjectDeleteResponse struct {
	ProjectID string                 `json:"project_id"`
	Deleted   bool                   `json:"deleted"`
	Blockers  []ProjectDeleteBlocker `json:"blockers,omitempty"`
}

type ProjectDeleteBlocker struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

type ProjectAttachWorkspaceRequest struct {
	ProjectID     string `json:"project_id"`
	WorkspaceRoot string `json:"workspace_root"`
}

// ProjectWorkspaceSelector identifies one workspace without using an empty
// string as an absence sentinel. Its fields are intentionally flat so legacy
// workspace_id request payloads remain wire-compatible.
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
	return s.WorkspaceID
}

func (s ProjectWorkspaceSelector) WorkspaceRootValue() *string {
	return s.WorkspaceRoot
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

type ProjectWorkspaceAttachOutcome string

const (
	ProjectWorkspaceAttachOutcomeAttached        ProjectWorkspaceAttachOutcome = "attached"
	ProjectWorkspaceAttachOutcomeAlreadyAttached ProjectWorkspaceAttachOutcome = "already_attached"
)

type ProjectAttachWorkspaceResponse struct {
	Binding ProjectBinding                `json:"binding"`
	Outcome ProjectWorkspaceAttachOutcome `json:"outcome"`
}

func (r ProjectAttachWorkspaceResponse) Validate() error {
	switch r.Outcome {
	case ProjectWorkspaceAttachOutcomeAttached, ProjectWorkspaceAttachOutcomeAlreadyAttached:
	default:
		return errors.New("outcome is invalid")
	}
	if strings.TrimSpace(r.Binding.ProjectID) == "" {
		return errors.New("binding.project_id is required")
	}
	if strings.TrimSpace(r.Binding.WorkspaceID) == "" {
		return errors.New("binding.workspace_id is required")
	}
	if strings.TrimSpace(r.Binding.CanonicalRoot) == "" {
		return errors.New("binding.canonical_root is required")
	}
	return nil
}

type ProjectRebindWorkspaceRequest struct {
	OldWorkspaceRoot string `json:"old_workspace_root"`
	NewWorkspaceRoot string `json:"new_workspace_root"`
}

type ProjectRebindWorkspaceResponse struct {
	Binding ProjectBinding `json:"binding"`
}

type ProjectGetOverviewRequest struct {
	ProjectID string
}

type ProjectGetOverviewResponse struct {
	Overview clientui.ProjectOverview
}

func (r ProjectGetOverviewRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project id is required")
	}
	return nil
}

func (r ProjectResolvePathRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.Path) == "" {
		return errors.New("path is required")
	}
	return nil
}

func (r ProjectBindingPlanRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.Path) == "" {
		return errors.New("path is required")
	}
	switch r.Mode {
	case ProjectBindingPlanModeInteractive, ProjectBindingPlanModeHeadless:
		return nil
	default:
		return errors.New("mode must be interactive or headless")
	}
}

func (r ProjectCreateRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if err := validateProjectDisplayName(r.DisplayName); err != nil {
		return err
	}
	if strings.TrimSpace(r.WorkspaceRoot) == "" {
		return errors.New("workspace_root is required")
	}
	if strings.TrimSpace(r.ProjectKey) != "" {
		if _, err := runtimeids.ParseProjectKey(r.ProjectKey); err != nil {
			return err
		}
	}
	return nil
}

func (r ProjectUpdateRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(r.ProjectKey) != "" {
		if _, err := runtimeids.ParseProjectKey(r.ProjectKey); err != nil {
			return err
		}
	}
	return validateProjectDisplayName(r.DisplayName)
}

func (r ProjectDefaultWorkspaceSetRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	return r.ProjectWorkspaceSelector.Validate()
}

func (r ProjectWorkspaceUnlinkRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	return r.ProjectWorkspaceSelector.Validate()
}

func (r ProjectDeleteRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	return nil
}

func (r ProjectHomeListRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if r.PageSize < 0 {
		return errors.New("page_size must be non-negative")
	}
	if strings.TrimSpace(r.PageToken) != r.PageToken {
		return errors.New("page_token must not have leading or trailing whitespace")
	}
	return nil
}

func (r ProjectAttachWorkspaceRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(r.WorkspaceRoot) == "" {
		return errors.New("workspace_root is required")
	}
	return nil
}

func (r ProjectWorkspaceListRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return errors.New("project_id must not have leading or trailing whitespace")
	}
	if r.Offset < 0 {
		return errors.New("offset must be non-negative")
	}
	if r.Limit < 1 || r.Limit > MaxProjectWorkspacePageSize {
		return fmt.Errorf("limit must be between 1 and %d", MaxProjectWorkspacePageSize)
	}
	return nil
}

func (r ProjectWorkspaceGetRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return errors.New("project_id must not have leading or trailing whitespace")
	}
	return r.ProjectWorkspaceSelector.Validate()
}

func (r ProjectEditGetRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.ProjectID) == "" {
		return errors.New("project_id is required")
	}
	if strings.TrimSpace(r.ProjectID) != r.ProjectID {
		return errors.New("project_id must not have leading or trailing whitespace")
	}
	return nil
}

func (r ProjectRebindWorkspaceRequest) Validate() (resultErr error) {
	defer func() { resultErr = classifyRequestValidation(resultErr) }()
	if strings.TrimSpace(r.OldWorkspaceRoot) == "" {
		return errors.New("old_workspace_root is required")
	}
	if strings.TrimSpace(r.NewWorkspaceRoot) == "" {
		return errors.New("new_workspace_root is required")
	}
	return nil
}

func validateProjectDisplayName(name string) error {
	if name != strings.TrimSpace(name) {
		return errors.New("display_name must not have leading or trailing whitespace")
	}
	if strings.ContainsAny(name, "\r\n") {
		return errors.New("display_name must be one line")
	}
	if length := len([]rune(name)); length < 1 || length > 80 {
		return errors.New("display_name must be 1-80 visible characters")
	}
	return nil
}
