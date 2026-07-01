package app

import (
	"context"
	"errors"
	"strings"

	"core/cli/app/internal/embeddedattach"
	"core/cli/app/internal/status"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

type appServerCore interface {
	Close() error
	OwnsServer() bool
	Config() config.App
}

type embeddedAppServer struct {
	inner              *embeddedattach.Server
	boundProjectID     string
	boundSessionLaunch client.SessionLaunchClient
}

func newEmbeddedAppServer(inner *embeddedattach.Server) *embeddedAppServer {
	if inner == nil {
		return nil
	}
	return &embeddedAppServer{inner: inner}
}

func (s *embeddedAppServer) Close() error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Close()
}

func (s *embeddedAppServer) OwnsServer() bool {
	return s != nil && s.inner != nil
}

func (s *embeddedAppServer) Config() config.App {
	if s == nil || s.inner == nil {
		return config.App{}
	}
	return s.inner.Config()
}

func (s *embeddedAppServer) BindProjectWorkspace(ctx context.Context, projectID string, workspaceID string) (interactiveSessionServer, error) {
	if s == nil {
		_, err := embeddedattach.BindProjectWorkspace(ctx, embeddedattach.WorkspaceBindingRequest{ProjectID: projectID, WorkspaceID: workspaceID})
		return nil, err
	}
	bound, err := embeddedattach.BindProjectWorkspace(ctx, embeddedattach.WorkspaceBindingRequest{
		Server:      s.inner,
		ProjectID:   projectID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, err
	}
	return &embeddedAppServer{
		inner:              s.inner,
		boundProjectID:     bound.ProjectID,
		boundSessionLaunch: bound.SessionLaunch,
	}, nil
}

func (s *embeddedAppServer) AuthStateResolver() status.AuthStateResolver {
	if s == nil || s.inner == nil {
		return nil
	}
	return status.NormalizeAuthStateResolver(s.inner.AuthManager())
}

func (s *embeddedAppServer) AuthStatePath() string {
	if s == nil || s.inner == nil || s.inner.AuthManager() == nil {
		return ""
	}
	return config.GlobalAuthConfigPath(s.Config())
}

func (s *embeddedAppServer) AuthStatusClient() client.AuthStatusClient {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.AuthStatusClient()
}

func (s *embeddedAppServer) AuthBootstrapClient() client.AuthBootstrapClient {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.AuthBootstrapClient()
}

func (s *embeddedAppServer) SharesProcessWith(other *embeddedAppServer) bool {
	return s != nil && other != nil && s.inner == other.inner
}

func (s *embeddedAppServer) ProjectID() string {
	if s == nil {
		return ""
	}
	if trimmed := strings.TrimSpace(s.boundProjectID); trimmed != "" {
		return trimmed
	}
	if s.inner == nil {
		return ""
	}
	return s.inner.ProjectID()
}

func (s *embeddedAppServer) ProjectViewClient() client.ProjectViewClient {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.ProjectViewClient()
}

func (s *embeddedAppServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	if s == nil || s.inner == nil {
		return runtimeAttachmentClients{}
	}
	return runtimeAttachmentClients{
		ApprovalViews:   s.inner.ApprovalViewClient(),
		AskViews:        s.inner.AskViewClient(),
		ProcessControls: s.inner.ProcessControlClient(),
		ProcessOutput:   s.inner.ProcessOutputClient(),
		ProcessViews:    s.inner.ProcessViewClient(),
		PromptActivity:  s.inner.PromptActivityClient(),
		PromptControl:   s.inner.PromptControlClient(),
		RuntimeControls: s.inner.RuntimeControlClient(),
		SessionActivity: s.inner.SessionActivityClient(),
		SessionRuntime:  s.inner.SessionRuntimeClient(),
		SessionViews:    s.inner.SessionViewClient(),
		Worktrees:       s.inner.WorktreeClient(),
	}
}

func (s *embeddedAppServer) SessionLaunchClient() client.SessionLaunchClient {
	if s == nil {
		return nil
	}
	if s.boundSessionLaunch != nil {
		return s.boundSessionLaunch
	}
	if s.inner == nil {
		return nil
	}
	return s.inner.SessionLaunchClient()
}

func (s *embeddedAppServer) SessionViewClient() client.SessionViewClient {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.SessionViewClient()
}

func (s *embeddedAppServer) SessionLifecycleClient() client.SessionLifecycleClient {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.SessionLifecycleClient()
}

func (s *embeddedAppServer) RunPromptClient() client.RunPromptClient {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.RunPromptClient()
}

func (s *embeddedAppServer) Reauthenticate(ctx context.Context, interactor authInteractor, interactiveAuth bool) error {
	if s == nil || s.inner == nil {
		return errors.New("embedded server is required")
	}
	status, err := s.AuthBootstrapClient().GetAuthBootstrapStatus(ctx, serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		return err
	}
	cfg := s.inner.Config()
	if interactive, ok := interactor.(*interactiveAuthInteractor); ok {
		return interactive.completeRemoteAuthBootstrap(ctx, s.AuthBootstrapClient(), cfg.Settings, status, true)
	}
	return ensureRemoteAuthReady(ctx, s.AuthBootstrapClient(), cfg.Settings, interactor, interactiveAuth)
}

func (s *embeddedAppServer) EnsureAuthReady(ctx context.Context, interactor authInteractor, interactiveAuth bool) error {
	if s == nil || s.inner == nil {
		return errors.New("embedded server is required")
	}
	cfg := s.inner.Config()
	return ensureRemoteAuthReady(ctx, s.AuthBootstrapClient(), cfg.Settings, interactor, interactiveAuth)
}
