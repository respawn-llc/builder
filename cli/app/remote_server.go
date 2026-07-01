package app

import (
	"context"
	"errors"
	"strings"

	"core/cli/app/internal/remoteattach"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type remoteAppServer struct {
	remote    *client.Remote
	identity  protocol.ServerIdentity
	projectID string
	cfg       config.App
	closeFn   func() error
	owns      bool
}

func newRemoteAppServerWithAuth(remote *client.Remote, cfg config.App, closeFn func() error, ownsServer bool) *remoteAppServer {
	if remote == nil {
		return nil
	}
	if closeFn == nil {
		closeFn = remote.Close
	}
	return &remoteAppServer{remote: remote, identity: remote.Identity(), projectID: remote.ProjectID(), cfg: cfg, closeFn: closeFn, owns: ownsServer}
}

func (s *remoteAppServer) Close() error {
	if s == nil {
		return nil
	}
	if s.closeFn != nil {
		return s.closeFn()
	}
	if s.remote == nil {
		return nil
	}
	return s.remote.Close()
}

func (s *remoteAppServer) OwnsServer() bool {
	return s != nil && s.owns
}

func (s *remoteAppServer) Config() config.App {
	if s == nil {
		return config.App{}
	}
	return s.cfg
}

func (s *remoteAppServer) BindProjectWorkspace(ctx context.Context, projectID string, workspaceID string) (interactiveSessionServer, error) {
	if s == nil {
		_, err := remoteattach.BindProjectWorkspace(ctx, remoteattach.ProjectWorkspaceBindingRequest{ProjectID: projectID, WorkspaceID: workspaceID})
		return nil, err
	}
	bound, err := remoteattach.BindProjectWorkspace(ctx, remoteattach.ProjectWorkspaceBindingRequest{
		Current:     s.remote,
		Config:      s.cfg,
		ProjectID:   projectID,
		WorkspaceID: workspaceID,
		OwnsServer:  s.owns,
		OwnedClose:  s.closeFn,
		RootID:      config.ExplicitPersistenceRootID(s.cfg),
	})
	if err != nil {
		return nil, err
	}
	return newRemoteAppServerWithAuth(bound.Remote, s.cfg, bound.CloseFn, s.owns), nil
}

func (s *remoteAppServer) AuthStatusClient() client.AuthStatusClient {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) ProjectID() string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.projectID)
}

func (s *remoteAppServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	if s == nil {
		return runtimeAttachmentClients{}
	}
	return runtimeAttachmentClients{
		ApprovalViews:   s.remote,
		AskViews:        s.remote,
		ProcessControls: s.remote,
		ProcessOutput:   s.remote,
		ProcessViews:    s.remote,
		PromptActivity:  s.remote,
		PromptControl:   s.remote,
		RuntimeControls: s.remote,
		SessionActivity: s.remote,
		SessionRuntime:  s.remote,
		SessionViews:    s.remote,
		Worktrees:       s.remote,
	}
}

func (s *remoteAppServer) ProjectViewClient() client.ProjectViewClient {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) SessionLaunchClient() client.SessionLaunchClient {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) SessionLifecycleClient() client.SessionLifecycleClient {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) SessionViewClient() client.SessionViewClient {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) Reauthenticate(ctx context.Context, interactor authInteractor, interactiveAuth bool) error {
	if s == nil || s.remote == nil {
		return errors.New("remote server is required")
	}
	status, err := s.remote.GetAuthBootstrapStatus(ctx, serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		return err
	}
	if interactive, ok := interactor.(*interactiveAuthInteractor); ok {
		return interactive.completeRemoteAuthBootstrap(ctx, s.remote, s.cfg.Settings, status, true)
	}
	return ensureRemoteAuthReady(ctx, s.remote, s.cfg.Settings, interactor, interactiveAuth)
}

func (s *remoteAppServer) EnsureAuthReady(ctx context.Context, interactor authInteractor, interactiveAuth bool) error {
	if s == nil || s.remote == nil {
		return errors.New("remote server is required")
	}
	return ensureRemoteAuthReady(ctx, s.remote, s.cfg.Settings, interactor, interactiveAuth)
}
