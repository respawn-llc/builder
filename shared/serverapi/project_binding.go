package serverapi

// ProjectBinding remains a server domain value because Session lifecycle
// operations use it independently of the Project Catalog API.
type ProjectBinding struct {
	ProjectID       string `json:"project_id"`
	ProjectKey      string `json:"project_key"`
	ProjectName     string `json:"project_name"`
	WorkspaceID     string `json:"workspace_id"`
	CanonicalRoot   string `json:"canonical_root"`
	WorkspaceName   string `json:"workspace_name"`
	WorkspaceStatus string `json:"workspace_status"`
}
