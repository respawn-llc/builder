package serverstatus

import (
	"context"

	"core/server/auth"
	"core/server/authservice"
	"core/server/workflow"
	servicecontract "core/shared/apicontract"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type ServerStatusService struct {
	authManager *auth.Manager
	endpoint    string
	settings    config.Settings
	updates     *UpdateStatusService
}

func NewServerStatusService(authManager *auth.Manager, cfg config.App, updates *UpdateStatusService) *ServerStatusService {
	return &ServerStatusService{authManager: authManager, endpoint: config.ServerRPCURL(cfg), settings: cfg.Settings, updates: updates}
}

func (s *ServerStatusService) GetServerReadiness(ctx context.Context, req serverapi.ServerReadinessRequest) (serverapi.ServerReadinessResponse, error) {
	return servicecontract.WithValidated(
		req,
		servicecontract.NoSemanticValidation,
		func(request servicecontract.Validated[serverapi.ServerReadinessRequest]) (serverapi.ServerReadinessResponse, error) {
			return s.GetServerReadinessValidated(ctx, request)
		},
	)
}

func (s *ServerStatusService) GetServerReadinessValidated(ctx context.Context, _ servicecontract.Validated[serverapi.ServerReadinessRequest]) (serverapi.ServerReadinessResponse, error) {
	settings := config.Settings{}
	if s != nil {
		settings = s.settings
	}
	authRequired := authservice.StartupAuthRequired(settings)
	var manager *auth.Manager
	if authRequired && s != nil {
		manager = s.authManager
	}
	authReadiness, err := authservice.EvaluateReadiness(ctx, manager, authRequired)
	if err != nil {
		return serverapi.ServerReadinessResponse{}, err
	}
	response := serverapi.ServerReadinessResponse{
		Ready:           authReadiness.Ready,
		ServerVersion:   config.Version,
		ServerBuild:     config.Version,
		ProtocolVersion: protocol.Version,
		AuthReady:       authReadiness.Gate.Ready,
		AuthRequired:    authReadiness.Required,
		Endpoint:        "",
		SubagentRoles:   subagentRoleSummaries(settings),
	}
	if s != nil {
		response.Endpoint = s.endpoint
	}
	if !authReadiness.Ready {
		response.Causes = []serverapi.ServerReadinessCause{{
			Code:     "server_not_ready",
			Severity: "error",
		}}
	}
	return response, nil
}

func (s *ServerStatusService) GetUpdateStatus(ctx context.Context, req serverapi.UpdateStatusRequest) (serverapi.UpdateStatusResponse, error) {
	return servicecontract.WithValidated(
		req,
		servicecontract.SemanticValidationRequired,
		func(request servicecontract.Validated[serverapi.UpdateStatusRequest]) (serverapi.UpdateStatusResponse, error) {
			return s.GetUpdateStatusValidated(ctx, request)
		},
	)
}

func (s *ServerStatusService) GetUpdateStatusValidated(ctx context.Context, _ servicecontract.Validated[serverapi.UpdateStatusRequest]) (serverapi.UpdateStatusResponse, error) {
	if s == nil || s.updates == nil {
		return serverapi.UpdateStatusResponse{}, ErrUpdateStatusServiceClosed
	}
	result, err := s.updates.Status(ctx)
	if err != nil {
		return serverapi.UpdateStatusResponse{}, err
	}
	return serverapi.UpdateStatusResponse{Result: result}, nil
}

var _ servicecontract.ServerStatusService = (*ServerStatusService)(nil)
var _ servicecontract.ServerStatusTrustedService = (*ServerStatusService)(nil)

func subagentRoleSummaries(settings config.Settings) []serverapi.SubagentRoleSummary {
	names := append([]string{workflow.DefaultAgentRole}, config.AvailableSubagentRoleNames(settings, false)...)
	roles := make([]serverapi.SubagentRoleSummary, 0, len(names))
	for _, name := range names {
		roles = append(roles, serverapi.SubagentRoleSummary{Name: name})
	}
	return roles
}
