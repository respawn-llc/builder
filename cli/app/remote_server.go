package app

import (
	"context"
	"errors"

	"core/cli/app/internal/remoteattach"
	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/theme"

	"google.golang.org/protobuf/types/known/emptypb"
)

type remoteAppServer struct {
	remote         *client.Remote
	identity       protocol.ServerIdentity
	cfg            config.App
	presentation   startupPresentation
	retarget       *sessionWorkspaceRetargetContext
	clientSettings config.ClientSettings
}

type startupPresentation struct {
	Theme string
}

func newRemoteAppServerWithAuth(remote *client.Remote, cfg config.App) *remoteAppServer {
	if remote == nil {
		return nil
	}
	server := &remoteAppServer{
		remote:       remote,
		identity:     remote.Identity(),
		cfg:          cfg,
		presentation: startupPresentation{Theme: theme.Resolve(cfg.Settings.Theme)},
	}
	if binding, present := remote.ProjectBinding(); present {
		server.retarget = sessionWorkspaceRetargetContextFromBinding(binding, server.presentation.Theme)
	}
	return server
}

func (s *remoteAppServer) PresentationTheme() string {
	return s.presentation.Theme
}

func (s *remoteAppServer) PromptCommandCatalogClient(_ context.Context, sessionID string, _ clientui.SessionExecutionTarget) (apicontract.PromptCommandCatalogService, error) {
	if s == nil {
		return nil, errors.New("remote server is required")
	}
	return s.remote.PromptCommandCatalogClientForSession(sessionID)
}

func (s *remoteAppServer) Close() error {
	if s == nil {
		return nil
	}
	if s.remote == nil {
		return nil
	}
	return s.remote.Close()
}

func (s *remoteAppServer) Config() config.App {
	if s == nil {
		return config.App{}
	}
	return s.cfg
}

func (s *remoteAppServer) ClientSettings() config.ClientSettings {
	if s == nil {
		return config.ClientSettings{}
	}
	return s.clientSettings
}

func (s *remoteAppServer) workspaceRetargetContext() *sessionWorkspaceRetargetContext {
	if s == nil || s.retarget == nil {
		return nil
	}
	copied := *s.retarget
	return &copied
}

func (s *remoteAppServer) BindProjectWorkspace(ctx context.Context, projectID string, workspaceID string) (interactiveSessionServer, error) {
	if s == nil {
		return nil, errors.New("remote server is required")
	}
	bound, err := remoteattach.BindProjectWorkspace(ctx, s.remote, s.cfg, projectID, workspaceID, config.ExplicitPersistenceRootID(s.cfg))
	if err != nil {
		return nil, err
	}
	binding, present := bound.ProjectBinding()
	if !present {
		closeErr := bound.Close()
		s.remote = nil
		return nil, errors.Join(errors.New("remote project attachment binding is required"), closeErr)
	}
	retargetContext := sessionWorkspaceRetargetContextFromBinding(binding, s.presentation.Theme)
	next := newRemoteAppServerWithAuth(bound, s.cfg)
	next.presentation = s.presentation
	next.retarget = retargetContext
	next.clientSettings = s.clientSettings
	s.remote = nil
	return next, nil
}

func (s *remoteAppServer) ReattachSession(ctx context.Context, sessionID string) error {
	if s == nil || s.remote == nil {
		return errors.New("remote server is required")
	}
	bound, err := remoteattach.BindSession(
		ctx,
		s.remote,
		s.cfg,
		sessionID,
		config.ExplicitPersistenceRootID(s.cfg),
	)
	if err != nil {
		return err
	}
	binding, present := bound.ProjectBinding()
	if !present {
		closeErr := bound.Close()
		s.remote = nil
		return errors.Join(errors.New("remote Session attachment binding is required"), closeErr)
	}
	s.remote = bound
	s.retarget = sessionWorkspaceRetargetContextFromBinding(binding, s.presentation.Theme)
	return nil
}

func (s *remoteAppServer) AuthStatusClient() apicontract.AuthStatusService {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) ProjectID() string {
	if s == nil || s.remote == nil {
		return ""
	}
	return s.remote.ProjectID()
}

func (s *remoteAppServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	if s == nil {
		return runtimeAttachmentClients{}
	}
	return runtimeAttachmentClients{
		ProcessControls:   s.remote,
		ProcessViews:      s.remote,
		PromptControl:     s.remote,
		RuntimeControls:   s.remote,
		SessionTranscript: s.remote,
		SessionRuntime:    s.remote,
		SessionViews:      s.remote,
		Worktrees:         s.remote,
	}
}

func (s *remoteAppServer) ProjectViewClient() apicontract.ProjectViewService {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) ServerStatusClient() apicontract.ServerStatusService {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) SessionLaunchClient() apicontract.SessionLaunchService {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) SessionLifecycleClient() apicontract.SessionLifecycleService {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) SessionViewClient() apicontract.SessionViewService {
	if s == nil {
		return nil
	}
	return s.remote
}

func (s *remoteAppServer) Reauthenticate(ctx context.Context, interactor authInteractor, interactiveAuth bool) error {
	if s == nil || s.remote == nil {
		return errors.New("remote server is required")
	}
	status, err := s.remote.GetBootstrapStatus(ctx, &emptypb.Empty{})
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
