package serverstatus

import (
	"context"

	"core/server/auth"
	"core/server/authservice"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type ServerStatusService struct {
	authManager *auth.Manager
	endpoint    string
	settings    config.Settings
}

func NewServerStatusService(authManager *auth.Manager, cfg config.App) *ServerStatusService {
	return &ServerStatusService{authManager: authManager, endpoint: config.ServerRPCURL(cfg), settings: cfg.Settings}
}

func (s *ServerStatusService) GetServerReadiness(ctx context.Context, _ serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	authReady := false
	settings := config.Settings{}
	if s != nil {
		settings = s.settings
	}
	authRequired := authservice.StartupAuthRequired(settings)
	// Only the OpenAI startup gate consults the auth store. When startup auth is
	// not required (custom/non-OpenAI provider), readiness must not depend on the
	// auth store at all, so a corrupt or inaccessible auth file can't block it.
	if authRequired && s != nil && s.authManager != nil {
		state, err := s.authManager.Load(ctx)
		if err != nil {
			return serverapi.ServerReadinessResponse{}, err
		}
		authReady = auth.EvaluateStartupGate(state).Ready
	}
	ready := authReady || !authRequired
	response := serverapi.ServerReadinessResponse{
		Ready:           ready,
		ServerVersion:   config.Version,
		ServerBuild:     config.Version,
		ProtocolVersion: protocol.Version,
		AuthReady:       authReady,
		AuthRequired:    authRequired,
		Endpoint:        "",
		SubagentRoles:   subagentRoleSummaries(settings),
	}
	if s != nil {
		response.Endpoint = s.endpoint
	}
	if !ready {
		summary := "Kent server is not ready."
		nextAction := "Resolve the startup blocker and retry."
		response.Causes = []serverapi.ServerReadinessCause{{
			Code:       "server_not_ready",
			Severity:   "error",
			Summary:    &summary,
			NextAction: &nextAction,
		}}
	}
	return response, nil
}

func subagentRoleSummaries(settings config.Settings) []serverapi.SubagentRoleSummary {
	names := append([]string{workflow.DefaultAgentRole}, config.AvailableSubagentRoleNames(settings, false)...)
	roles := make([]serverapi.SubagentRoleSummary, 0, len(names))
	for _, name := range names {
		roles = append(roles, serverapi.SubagentRoleSummary{Name: name})
	}
	return roles
}
