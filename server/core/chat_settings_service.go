package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"core/server/sessionlaunch"
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
	switch req.Target.TargetKind {
	case serverapi.ChatSettingsReadTargetLazy:
		projectCtx, err := s.core.resolveProjectContext(ctx, *req.Target.ProjectID, *req.Target.WorkspaceID, "")
		if err != nil {
			return serverapi.ChatSettingsReadResponse{}, err
		}
		return s.core.sessionLaunchServiceForProjectContext(projectCtx).LazyChatSettings(ctx)
	case serverapi.ChatSettingsReadTargetSession:
		sessionID := *req.Target.Session
		service, err := s.materializedService(ctx, sessionID.String())
		if err != nil {
			return serverapi.ChatSettingsReadResponse{}, err
		}
		return service.MaterializedChatSettings(ctx, sessionID)
	default:
		return serverapi.ChatSettingsReadResponse{}, errors.New("Chat settings target kind is invalid")
	}
}

func (s chatSettingsService) materializedService(
	ctx context.Context,
	sessionID string,
) (*sessionlaunch.Service, error) {
	store := s.core.safeBundles().Persistence.metadataStore
	record, err := store.ResolvePersistedSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	boundary, err := store.ResolveSessionProjectWorkspaceBoundary(ctx, sessionID)
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
	return s.core.newSessionLaunchService(projectContext{
		config:         projectCfg,
		projectID:      boundary.ProjectID,
		projectRoot:    effectiveRoot,
		projectSession: filepath.Join(projectCfg.PersistenceRoot, "projects", boundary.ProjectID, "sessions"),
	}), nil
}
