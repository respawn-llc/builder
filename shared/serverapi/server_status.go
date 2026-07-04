package serverapi

type ServerReadinessRequest struct{}

type ServerReadinessResponse struct {
	Ready           bool                   `json:"ready"`
	ServerID        string                 `json:"server_id"`
	ServerVersion   string                 `json:"server_version"`
	ServerBuild     string                 `json:"server_build"`
	ProtocolVersion string                 `json:"protocol_version"`
	AuthReady       bool                   `json:"auth_ready"`
	AuthRequired    bool                   `json:"auth_required"`
	Endpoint        string                 `json:"endpoint"`
	SubagentRoles   []SubagentRoleSummary  `json:"subagent_roles,omitempty"`
	Causes          []ServerReadinessCause `json:"causes,omitempty"`
}

type SubagentRoleSummary struct {
	Name string `json:"name"`
}

type ServerReadinessCause struct {
	Code         string  `json:"code"`
	Severity     string  `json:"severity"`
	Summary      *string `json:"summary,omitempty"`
	NextAction   *string `json:"next_action,omitempty"`
	DiagnosticID string  `json:"diagnostic_id,omitempty"`
}
