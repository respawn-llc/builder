package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"core/server/auth"
	"core/server/launch"
	"core/server/metadata"
	"core/server/runprompt"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionlaunch"
	askquestion "core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type Core struct {
	bundles   *Bundles
	closeOnce sync.Once
	closeErr  error
}

type unregisteredSessionLaunchClient struct{}

func (unregisteredSessionLaunchClient) PlanSession(context.Context, serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	return serverapi.SessionPlanResponse{}, serverapi.ErrWorkspaceNotRegistered
}

type unregisteredRunPromptClient struct{}

func (unregisteredRunPromptClient) RunPrompt(context.Context, serverapi.RunPromptRequest, serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	return serverapi.RunPromptResponse{}, serverapi.ErrWorkspaceNotRegistered
}

type projectContext struct {
	config         config.App
	projectID      string
	projectRoot    string
	projectSession string
}

func (s *Core) ProjectExists(ctx context.Context, projectID string) error {
	if s == nil || s.safeBundles().Persistence.metadataStore == nil {
		return errors.New("metadata store is required")
	}
	_, err := s.safeBundles().Persistence.metadataStore.GetProjectOverview(ctx, strings.TrimSpace(projectID))
	return err
}

func (s *Core) SessionBelongsToProject(ctx context.Context, sessionID string, projectID string) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return fmt.Errorf("session id is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return fmt.Errorf("project id is required")
	}
	if s == nil || s.safeBundles().Persistence.metadataStore == nil {
		return errors.New("metadata store is required")
	}
	belongs, err := s.safeBundles().Persistence.metadataStore.SessionBelongsToProject(ctx, trimmedSessionID, trimmedProjectID)
	if err != nil {
		return err
	}
	if !belongs {
		return fmt.Errorf("session %q not available", trimmedSessionID)
	}
	return nil
}

func (s *Core) SessionLaunchClientForProject(ctx context.Context, projectID string) (client.SessionLaunchClient, error) {
	return s.SessionLaunchClientForProjectWorkspace(ctx, projectID, s.safeBundles().Projects.cfg.WorkspaceRoot)
}

func (s *Core) SessionLaunchClientForProjectWorkspaceID(ctx context.Context, projectID string, workspaceID string) (client.SessionLaunchClient, error) {
	projectCtx, err := s.resolveProjectContext(ctx, projectID, workspaceID, "")
	if err != nil {
		return nil, err
	}
	return s.sessionLaunchClientForProjectContext(projectCtx), nil
}

func (s *Core) SessionLaunchClientForProjectWorkspace(ctx context.Context, projectID string, workspaceRoot string) (client.SessionLaunchClient, error) {
	projectCtx, err := s.resolveProjectContext(ctx, projectID, "", workspaceRoot)
	if err != nil {
		return nil, err
	}
	return s.sessionLaunchClientForProjectContext(projectCtx), nil
}

func (s *Core) RunPromptClientForProject(ctx context.Context, projectID string) (client.RunPromptClient, error) {
	return s.RunPromptClientForProjectWorkspace(ctx, projectID, s.safeBundles().Projects.cfg.WorkspaceRoot)
}

func (s *Core) RunPromptClientForProjectWorkspaceID(ctx context.Context, projectID string, workspaceID string) (client.RunPromptClient, error) {
	projectCtx, err := s.resolveProjectContext(ctx, projectID, workspaceID, "")
	if err != nil {
		return nil, err
	}
	return s.runPromptClientForProjectContext(projectCtx), nil
}

func (s *Core) RunPromptClientForProjectWorkspace(ctx context.Context, projectID string, workspaceRoot string) (client.RunPromptClient, error) {
	projectCtx, err := s.resolveProjectContext(ctx, projectID, "", workspaceRoot)
	if err != nil {
		return nil, err
	}
	return s.runPromptClientForProjectContext(projectCtx), nil
}

func (s *Core) resolveProjectContext(ctx context.Context, projectID string, workspaceID string, workspaceRoot string) (projectContext, error) {
	if s == nil || s.safeBundles().Persistence.metadataStore == nil {
		return projectContext{}, errors.New("metadata store is required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if trimmedProjectID == "" {
		return projectContext{}, errors.New("project id is required")
	}
	trimmedWorkspaceID := strings.TrimSpace(workspaceID)
	if trimmedWorkspaceID != "" {
		binding, err := s.safeBundles().Persistence.metadataStore.LookupWorkspaceBindingByID(ctx, trimmedWorkspaceID)
		if err != nil {
			return projectContext{}, err
		}
		if strings.TrimSpace(binding.ProjectID) != trimmedProjectID {
			return projectContext{}, fmt.Errorf("workspace %q is not bound to project %q", binding.CanonicalRoot, trimmedProjectID)
		}
		availability := clientui.ProjectAvailability(binding.WorkspaceStatus)
		switch availability {
		case clientui.ProjectAvailabilityMissing, clientui.ProjectAvailabilityInaccessible:
			return projectContext{}, serverapi.ProjectUnavailableError{
				ProjectID:    trimmedProjectID,
				RootPath:     binding.CanonicalRoot,
				Availability: availability,
			}
		}
		projectCfg, err := s.configForWorkspace(binding.CanonicalRoot)
		if err != nil {
			return projectContext{}, err
		}
		return projectContext{
			config:         projectCfg,
			projectID:      trimmedProjectID,
			projectRoot:    binding.CanonicalRoot,
			projectSession: filepath.Join(filepath.Join(projectCfg.PersistenceRoot, "projects"), trimmedProjectID, "sessions"),
		}, nil
	}
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot != "" {
		binding, err := s.safeBundles().Persistence.metadataStore.EnsureWorkspaceBinding(ctx, trimmedWorkspaceRoot)
		if err == nil {
			if strings.TrimSpace(binding.ProjectID) != trimmedProjectID {
				return projectContext{}, fmt.Errorf("workspace %q is not bound to project %q", binding.CanonicalRoot, trimmedProjectID)
			}
			projectCfg, err := s.configForWorkspace(binding.CanonicalRoot)
			if err != nil {
				return projectContext{}, err
			}
			return projectContext{
				config:         projectCfg,
				projectID:      trimmedProjectID,
				projectRoot:    binding.CanonicalRoot,
				projectSession: filepath.Join(filepath.Join(projectCfg.PersistenceRoot, "projects"), trimmedProjectID, "sessions"),
			}, nil
		}
		if !errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
			return projectContext{}, err
		}
	}
	overview, err := s.safeBundles().Persistence.metadataStore.GetProjectOverview(ctx, trimmedProjectID)
	if err != nil {
		return projectContext{}, err
	}
	if strings.TrimSpace(overview.Project.RootPath) == "" {
		return projectContext{}, fmt.Errorf("project %q has no root path", trimmedProjectID)
	}
	switch overview.Project.Availability {
	case clientui.ProjectAvailabilityMissing, clientui.ProjectAvailabilityInaccessible:
		return projectContext{}, serverapi.ProjectUnavailableError{
			ProjectID:    trimmedProjectID,
			RootPath:     overview.Project.RootPath,
			Availability: overview.Project.Availability,
		}
	}
	projectCfg, err := s.configForWorkspace(overview.Project.RootPath)
	if err != nil {
		return projectContext{}, err
	}
	return projectContext{
		config:         projectCfg,
		projectID:      trimmedProjectID,
		projectRoot:    overview.Project.RootPath,
		projectSession: filepath.Join(filepath.Join(projectCfg.PersistenceRoot, "projects"), trimmedProjectID, "sessions"),
	}, nil
}

func (s *Core) configForWorkspace(workspaceRoot string) (config.App, error) {
	if s == nil {
		return config.App{}, errors.New("core is required")
	}
	if strings.TrimSpace(s.safeBundles().Projects.cfg.WorkspaceRoot) != "" {
		currentRoot, currentErr := config.CanonicalWorkspaceRoot(s.safeBundles().Projects.cfg.WorkspaceRoot)
		requestedRoot, requestedErr := config.CanonicalWorkspaceRoot(workspaceRoot)
		if currentErr == nil && requestedErr == nil && currentRoot == requestedRoot {
			projectCfg := s.safeBundles().Projects.cfg
			projectCfg.WorkspaceRoot = requestedRoot
			return projectCfg, nil
		}
	}
	projectCfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		return config.App{}, err
	}
	projectCfg.PersistenceRoot = s.safeBundles().Projects.cfg.PersistenceRoot
	return projectCfg, nil
}

func (s *Core) sessionLaunchClientForProjectContext(projectCtx projectContext) client.SessionLaunchClient {
	if s == nil {
		return nil
	}
	scopeKey := projectWorkspaceScopeKey(projectCtx)
	s.safeBundles().Sessions.mu.Lock()
	defer s.safeBundles().Sessions.mu.Unlock()
	if cached := s.safeBundles().Sessions.sessionLaunchMap[scopeKey]; cached != nil {
		return cached
	}
	service := s.sessionLaunchServiceForProjectContextLocked(projectCtx)
	client := client.NewLoopbackSessionLaunchClient(service)
	s.safeBundles().Sessions.sessionLaunchMap[scopeKey] = client
	return client
}

func (s *Core) sessionLaunchServiceForProjectContext(projectCtx projectContext) *sessionlaunch.Service {
	if s == nil {
		return nil
	}
	s.safeBundles().Sessions.mu.Lock()
	defer s.safeBundles().Sessions.mu.Unlock()
	return s.sessionLaunchServiceForProjectContextLocked(projectCtx)
}

func (s *Core) sessionLaunchServiceForProjectContextLocked(projectCtx projectContext) *sessionlaunch.Service {
	scopeKey := projectWorkspaceScopeKey(projectCtx)
	if cached := s.safeBundles().Sessions.sessionServices[scopeKey]; cached != nil {
		return cached
	}
	service := sessionlaunch.NewService(launch.Planner{
		Config:       projectCtx.config,
		ContainerDir: projectCtx.projectSession,
		StoreOptions: s.safeBundles().Persistence.metadataStore.AuthoritativeSessionStoreOptions(),
		ReloadConfig: func() (config.App, error) {
			return s.configForWorkspace(projectCtx.projectRoot)
		},
	}, s.safeBundles().Persistence.sessionStores).
		WithAuthStateReader(s.safeBundles().Auth.support.AuthManager).
		WithPromptHistoryReader(s.safeBundles().Persistence.metadataStore)
	s.safeBundles().Sessions.sessionServices[scopeKey] = service
	return service
}

func (s *Core) runPromptClientForProjectContext(projectCtx projectContext) client.RunPromptClient {
	if s == nil {
		return nil
	}
	scopeKey := projectWorkspaceScopeKey(projectCtx)
	s.safeBundles().Sessions.runPromptMu.Lock()
	defer s.safeBundles().Sessions.runPromptMu.Unlock()
	if cached := s.safeBundles().Sessions.runPromptMap[scopeKey]; cached != nil {
		return cached
	}
	client := runprompt.NewLoopbackRunPromptClient(runprompt.HeadlessBootstrap{
		SessionLaunch:    s.sessionLaunchServiceForProjectContext(projectCtx),
		AuthManager:      s.safeBundles().Auth.support.AuthManager,
		FastModeState:    s.safeBundles().Runtime.fastModeState,
		Background:       s.safeBundles().Runtime.background,
		RuntimeRegistry:  s.safeBundles().Runtime.runtimeRegistry,
		PromptHistory:    s.safeBundles().Persistence.metadataStore,
		SessionRuntime:   s.safeBundles().Runtime.sessionRuntimeService,
		PersistenceRoot:  projectCtx.config.PersistenceRoot,
	})
	s.safeBundles().Sessions.runPromptMap[scopeKey] = client
	return client
}

func projectWorkspaceScopeKey(projectCtx projectContext) string {
	return strings.TrimSpace(projectCtx.projectID) + "\n" + strings.TrimSpace(projectCtx.config.WorkspaceRoot)
}

func (s *Core) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.bundles != nil {
			s.closeErr = closeLifecycleResources(s.safeBundles().cleanup)
		}
	})
	return s.closeErr
}

func (s *Core) Config() config.App {
	if s == nil {
		return config.App{}
	}
	return s.safeBundles().Projects.cfg
}

func (s *Core) ContainerDir() string {
	if s == nil {
		return ""
	}
	return s.safeBundles().Projects.containerDir
}

func (s *Core) MetadataStore() *metadata.Store {
	if s == nil {
		return nil
	}
	return s.safeBundles().Persistence.metadataStore
}

func (s *Core) OAuthOptions() auth.OpenAIOAuthOptions {
	if s == nil {
		return auth.OpenAIOAuthOptions{}
	}
	return s.safeBundles().Auth.support.OAuthOptions
}

func (s *Core) AuthManager() *auth.Manager {
	if s == nil {
		return nil
	}
	return s.safeBundles().Auth.support.AuthManager
}

func (s *Core) ServerAuthRequired() bool {
	if s == nil {
		return true
	}
	return s.safeBundles().Auth.authRequired
}

func (s *Core) FastModeState() *runtime.FastModeState {
	if s == nil {
		return nil
	}
	return s.safeBundles().Runtime.fastModeState
}

func (s *Core) Background() *shelltool.Manager {
	if s == nil {
		return nil
	}
	return s.safeBundles().Runtime.background
}

func (s *Core) BackgroundRouter() *runtimewire.BackgroundEventRouter {
	if s == nil {
		return nil
	}
	return s.safeBundles().Runtime.backgroundRouter
}

func (s *Core) SessionViewClient() client.SessionViewClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Sessions.sessionViews
}

func (s *Core) ProjectID() string {
	if s == nil {
		return ""
	}
	return s.safeBundles().Projects.projectID
}

func (s *Core) ProjectViewClient() client.ProjectViewClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Projects.projectViews
}

func (s *Core) AuthBootstrapClient() client.AuthBootstrapClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Auth.authBootstrap
}

func (s *Core) AuthStatusClient() client.AuthStatusClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Auth.authStatus
}

func (s *Core) AskViewClient() client.AskViewClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Prompts.askViews
}

func (s *Core) ApprovalViewClient() client.ApprovalViewClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Prompts.approvalViews
}

func (s *Core) ProcessViewClient() client.ProcessViewClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Processes.processViews
}

func (s *Core) RuntimeControlClient() client.RuntimeControlClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Runtime.runtimeControls
}

func (s *Core) ServerStatusClient() client.ServerStatusClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Auth.serverStatus
}

func (s *Core) PromptControlClient() client.PromptControlClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Prompts.promptControl
}

func (s *Core) PromptActivityClient() client.PromptActivityClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Prompts.promptActivity
}

func (s *Core) ProcessControlClient() client.ProcessControlClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Processes.processControls
}

func (s *Core) ProcessOutputClient() client.ProcessOutputClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Processes.processOutput
}

func (s *Core) SessionActivityClient() client.SessionActivityClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Runtime.sessionActivity
}

func (s *Core) SessionLaunchClient() client.SessionLaunchClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Sessions.sessionLaunch
}

func (s *Core) SessionRuntimeClient() client.SessionRuntimeClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Runtime.sessionRuntime
}

func (s *Core) SessionLifecycleClient() client.SessionLifecycleClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Sessions.sessionLifecycle
}

func (s *Core) WorktreeClient() client.WorktreeClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Worktrees.worktrees
}

func (s *Core) WorkflowClient() client.WorkflowClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Workflows.workflows
}

func (s *Core) RegisterSessionStore(store *session.Store) {
	if s == nil || s.safeBundles().Persistence.sessionStores == nil {
		return
	}
	s.safeBundles().Persistence.sessionStores.RegisterStore(store)
}

func (s *Core) ResolveSessionStore(sessionID string) (*session.Store, error) {
	if s == nil || s.safeBundles().Persistence.sessionStores == nil {
		return nil, nil
	}
	return s.safeBundles().Persistence.sessionStores.ResolveStore(context.Background(), sessionID)
}

func (s *Core) PublishRuntimeEvent(sessionID string, evt runtime.Event) {
	if s == nil || s.safeBundles().Runtime.runtimeRegistry == nil {
		return
	}
	s.safeBundles().Runtime.runtimeRegistry.PublishRuntimeEvent(sessionID, evt)
}

func (s *Core) BeginPendingPrompt(sessionID string, req askquestion.AskQuestionRequest) {
	if s == nil || s.safeBundles().Runtime.runtimeRegistry == nil {
		return
	}
	s.safeBundles().Runtime.runtimeRegistry.BeginPendingPrompt(sessionID, req)
}

func (s *Core) CompletePendingPrompt(sessionID string, requestID string) {
	if s == nil || s.safeBundles().Runtime.runtimeRegistry == nil {
		return
	}
	s.safeBundles().Runtime.runtimeRegistry.CompletePendingPrompt(sessionID, requestID)
}

func (s *Core) AwaitPromptResponse(ctx context.Context, sessionID string, req askquestion.AskQuestionRequest) (askquestion.AskQuestionResponse, error) {
	if s == nil || s.safeBundles().Runtime.runtimeRegistry == nil {
		return askquestion.AskQuestionResponse{}, fmt.Errorf("runtime registry is required")
	}
	return s.safeBundles().Runtime.runtimeRegistry.AwaitPromptResponse(ctx, sessionID, req)
}

func (s *Core) RunPromptClient() client.RunPromptClient {
	if s == nil {
		return nil
	}
	return s.safeBundles().Sessions.runPrompt
}
