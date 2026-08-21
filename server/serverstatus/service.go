package serverstatus

import (
	"context"

	"core/server/auth"
	"core/server/authservice"
	"core/server/workflow"
	"core/shared/config"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	"core/shared/protocol"

	"google.golang.org/protobuf/types/known/emptypb"
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

func (s *ServerStatusService) GetReadiness(ctx context.Context, _ *emptypb.Empty) (*serverpb.GetReadinessSuccess, error) {
	authReady := false
	settings := config.Settings{}
	if s != nil {
		settings = s.settings
	}
	authRequired := authservice.StartupAuthRequired(settings)
	// Identity-independent providers must not consult the auth store: a corrupt
	// OpenAI credential file cannot block a custom provider's server readiness.
	if authRequired && s != nil && s.authManager != nil {
		state, err := s.authManager.Load(ctx)
		if err != nil {
			return nil, err
		}
		authReady = auth.EvaluateStartupGate(state).Ready
	}
	ready := authReady || !authRequired
	readiness := &serverpb.Readiness{
		Ready:           ready,
		ServerVersion:   config.Version,
		ServerBuild:     config.Version,
		ProtocolVersion: protocol.Version,
		AuthReady:       authReady,
		AuthRequired:    authRequired,
		SubagentRoles:   subagentRoleSummaries(settings),
	}
	if s != nil {
		readiness.Endpoint = s.endpoint
	}
	if !ready {
		readiness.Causes = []*serverpb.ReadinessCause{{
			Code:     "server_not_ready",
			Severity: serverpb.ReadinessSeverity_READINESS_SEVERITY_ERROR,
		}}
	}
	return &serverpb.GetReadinessSuccess{Readiness: readiness}, nil
}

func (s *ServerStatusService) GetUpdateStatus(ctx context.Context, _ *emptypb.Empty) (*serverpb.GetUpdateStatusSuccess, error) {
	if s == nil || s.updates == nil {
		return nil, ErrUpdateStatusServiceClosed
	}
	result, err := s.updates.status(ctx)
	if err != nil {
		return nil, err
	}
	return &serverpb.GetUpdateStatusSuccess{Status: result.proto()}, nil
}

func subagentRoleSummaries(settings config.Settings) []*serverpb.SubagentRoleSummary {
	names := append([]string{workflow.DefaultAgentRole}, config.AvailableSubagentRoleNames(settings, false)...)
	roles := make([]*serverpb.SubagentRoleSummary, 0, len(names))
	for _, name := range names {
		roles = append(roles, &serverpb.SubagentRoleSummary{Name: name})
	}
	return roles
}
