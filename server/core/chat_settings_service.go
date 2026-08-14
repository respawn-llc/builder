package core

import (
	"context"
	"errors"
	"path/filepath"

	"core/server/sessionlaunch"
	"core/shared/apicontract"
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
	return s.sessionLaunchServiceForScope(sessionLaunchServiceScope{
		kind:    sessionLaunchServiceDetachedScope,
		project: projectCtx,
	})
}

var _ apicontract.ChatSettingsService = chatSettingsService{}
