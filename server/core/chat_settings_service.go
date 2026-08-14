package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

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
	record, err := s.core.safeBundles().Persistence.metadataStore.ResolvePersistedSession(
		ctx,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	if record.Meta == nil {
		return nil, errors.New("persisted Session metadata is required")
	}
	boundary, err := s.core.safeBundles().Persistence.metadataStore.ResolveSessionProjectWorkspaceBoundary(
		ctx,
		sessionID,
	)
	if err != nil {
		return nil, err
	}
	effectiveRoot := strings.TrimSpace(record.Meta.WorkspaceRoot)
	if effectiveRoot == "" {
		return nil, errors.New("session effective workspace root is required")
	}
	projectCfg, err := s.core.configForWorkspace(effectiveRoot)
	if err != nil {
		return nil, err
	}
	projectCtx := projectContext{
		config:         projectCfg,
		projectID:      boundary.ProjectID,
		projectRoot:    effectiveRoot,
		projectSession: filepath.Join(projectCfg.PersistenceRoot, "projects", boundary.ProjectID, "sessions"),
	}
	return s.core.detachedChatSettingsService(projectCtx), nil
}

func (s *Core) detachedChatSettingsService(projectCtx projectContext) *sessionlaunch.Service {
	return s.newSessionLaunchService(projectCtx)
}

var _ apicontract.ChatSettingsService = chatSettingsService{}
