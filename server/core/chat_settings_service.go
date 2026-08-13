package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"core/server/launch"
	"core/server/sessionlaunch"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

type chatSettingsService struct {
	core *Core
}

func (s chatSettingsService) ReadChatSettings(
	ctx context.Context,
	req serverapi.ChatSettingsReadRequest,
) (serverapi.ChatSettingsReadResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ChatSettingsReadResponse{}, err
	}
	switch req.Target.Kind() {
	case serverapi.ChatSettingsReadTargetLazy:
		projectID, workspaceID, _ := req.Target.Lazy()
		service, err := s.core.SessionLaunchClientForProjectWorkspaceID(
			ctx,
			projectID,
			workspaceID,
		)
		if err != nil {
			return serverapi.ChatSettingsReadResponse{}, err
		}
		scoped, ok := service.(*sessionlaunch.Service)
		if !ok {
			return serverapi.ChatSettingsReadResponse{}, errors.New(
				"Chat settings require scoped Session launch service",
			)
		}
		return scoped.LazyChatSettings(ctx)
	case serverapi.ChatSettingsReadTargetSession:
		sessionID, _ := req.Target.SessionID()
		service, err := s.materializedService(ctx, sessionID.String())
		if err != nil {
			return serverapi.ChatSettingsReadResponse{}, err
		}
		return service.MaterializedChatSettings(ctx, sessionID)
	default:
		return serverapi.ChatSettingsReadResponse{}, errors.New(
			"Chat settings target kind is invalid",
		)
	}
}

func (s chatSettingsService) materializedService(
	ctx context.Context,
	sessionID string,
) (*sessionlaunch.Service, error) {
	if s.core == nil || s.core.safeBundles().Persistence.metadataStore == nil {
		return nil, errors.New("metadata store is required")
	}
	scope, err := s.core.safeBundles().Persistence.metadataStore.ResolveSessionChatSettingsScope(
		ctx,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	projectCfg, err := s.core.configForWorkspace(scope.EffectiveRoot)
	if err != nil {
		return nil, err
	}
	projectCtx := projectContext{
		config:         projectCfg,
		projectID:      scope.ProjectID,
		projectRoot:    scope.EffectiveRoot,
		projectSession: filepath.Join(projectCfg.PersistenceRoot, "projects", scope.ProjectID, "sessions"),
	}
	return s.core.detachedChatSettingsService(projectCtx), nil
}

func (s *Core) detachedChatSettingsService(projectCtx projectContext) *sessionlaunch.Service {
	if s == nil {
		return nil
	}
	key := "detached\n" + strings.TrimSpace(projectCtx.projectID) + "\n" +
		strings.TrimSpace(projectCtx.projectRoot)
	s.safeBundles().Sessions.mu.Lock()
	defer s.safeBundles().Sessions.mu.Unlock()
	if cached := s.safeBundles().Sessions.sessionServices[key]; cached != nil {
		return cached
	}
	service := sessionlaunch.NewService(launch.Planner{
		Config:                   projectCtx.config,
		ContainerDir:             projectCtx.projectSession,
		StoreOptions:             s.safeBundles().Persistence.metadataStore.AuthoritativeSessionStoreOptions(),
		PersistedSessions:        s.safeBundles().Persistence.metadataStore,
		ExecutionTargets:         s.safeBundles().Persistence.metadataStore,
		ProjectWorkspaceBoundary: s.safeBundles().Persistence.metadataStore,
		ReloadConfig: func() (config.App, error) {
			return s.configForWorkspace(projectCtx.projectRoot)
		},
	}).
		WithAuthStateReader(s.safeBundles().Auth.support.AuthManager).
		WithPromptHistoryReader(s.safeBundles().Persistence.metadataStore).
		WithWorkflowTaskReader(s.safeBundles().Persistence.metadataStore).
		WithRuntimeAuthority(s.safeBundles().Runtime.runtimeAuthority)
	s.safeBundles().Sessions.sessionServices[key] = service
	return service
}

var _ apicontract.ChatSettingsService = chatSettingsService{}
