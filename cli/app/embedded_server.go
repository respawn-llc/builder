package app

import (
	"context"
	"errors"
	"strings"

	"core/cli/app/internal/embeddedattach"
	"core/cli/app/internal/status"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/theme"
)

func (s *embeddedAppServer) PresentationTheme() string {
	if s == nil || s.inner == nil {
		panic("embedded startup presentation theme is required")
	}
	return theme.Resolve(s.Config().Settings.Theme)
}

type appServerCore interface {
	Close() error
	OwnsServer() bool
	Config() config.App
}

type embeddedAppServer struct {
	inner              *embeddedattach.Server
	boundProjectID     string
	boundWorkspaceID   *string
	boundSessionLaunch apicontract.SessionLaunchService
	retarget           *sessionWorkspaceRetargetContext
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

func (s *embeddedAppServer) Failures() <-chan error {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.Failures()
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

func (s *embeddedAppServer) workspaceRetargetContext() *sessionWorkspaceRetargetContext {
	if s == nil || s.retarget == nil {
		return nil
	}
	copied := *s.retarget
	return &copied
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
	nextWorkspaceID := strings.TrimSpace(workspaceID)
	if nextWorkspaceID == "" {
		return nil, errors.New("workspace id is required")
	}
	retargetContext, err := resolveSessionWorkspaceRetargetContext(
		ctx,
		s.ProjectViewClient(),
		bound.ProjectID,
		nextWorkspaceID,
		s.PresentationTheme(),
	)
	if err != nil {
		return nil, err
	}
	return &embeddedAppServer{
		inner:              s.inner,
		boundProjectID:     bound.ProjectID,
		boundWorkspaceID:   textutil.Value(nextWorkspaceID),
		boundSessionLaunch: bound.SessionLaunch,
		retarget:           retargetContext,
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

func (s *embeddedAppServer) AuthStatusClient() apicontract.AuthStatusService {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.AuthStatusClient()
}

func (s *embeddedAppServer) AuthBootstrapClient() apicontract.AuthBootstrapService {
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

func (s *embeddedAppServer) ProjectViewClient() apicontract.ProjectViewService {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.ProjectViewClient()
}

func (s *embeddedAppServer) ServerStatusClient() apicontract.ServerStatusService {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.ServerStatusClient()
}

func (s *embeddedAppServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	if s == nil || s.inner == nil {
		return runtimeAttachmentClients{}
	}
	return runtimeAttachmentClients{
		ProcessControls:   s.inner.ProcessControlClient(),
		ProcessOutput:     s.inner.ProcessOutputClient(),
		ProcessViews:      s.inner.ProcessViewClient(),
		PromptControl:     s.inner.PromptControlClient(),
		RuntimeControls:   s.inner.RuntimeControlClient(),
		SessionTranscript: s.inner.SessionTranscriptClient(),
		SessionRuntime:    s.inner.SessionRuntimeClient(),
		SessionViews:      s.inner.SessionViewClient(),
		Worktrees:         s.inner.WorktreeClient(),
	}
}

func (s *embeddedAppServer) SessionLaunchClient() apicontract.SessionLaunchService {
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

func (s *embeddedAppServer) SessionViewClient() apicontract.SessionViewService {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.SessionViewClient()
}

func (s *embeddedAppServer) SessionLifecycleClient() apicontract.SessionLifecycleService {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.SessionLifecycleClient()
}

func (s *embeddedAppServer) RunPromptClient() apicontract.RunPromptService {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.RunPromptClient()
}

func (s *embeddedAppServer) PromptCommandCatalogClient(ctx context.Context, _ string, target clientui.SessionExecutionTarget) (apicontract.PromptCommandCatalogService, error) {
	if s == nil || s.inner == nil {
		return nil, errors.New("embedded server is required")
	}
	workspaceRoot := strings.TrimSpace(target.WorkspaceRoot)
	if target.Worktree != nil {
		var err error
		workspaceRoot, err = clientui.SessionExecutionWorkspaceRoot(target, workspaceRoot)
		if err != nil {
			return nil, err
		}
	}
	if workspaceRoot == "" && s.boundWorkspaceID != nil {
		binding, err := s.inner.MetadataStore().LookupWorkspaceBindingByID(ctx, *s.boundWorkspaceID)
		if err != nil {
			return nil, err
		}
		workspaceRoot = binding.CanonicalRoot
	}
	if workspaceRoot == "" {
		workspaceRoot = s.Config().WorkspaceRoot
	}
	return s.inner.PromptCommandCatalogClientForProjectWorkspace(ctx, s.ProjectID(), workspaceRoot)
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
