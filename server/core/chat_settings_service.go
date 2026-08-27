package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"core/server/runtime"
	"core/server/session"
	"core/server/sessionlaunch"
	"core/shared/runtimeids"
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

func (s chatSettingsService) MutateChatSettings(
	ctx context.Context,
	req serverapi.ChatSettingsMutationRequest,
) (serverapi.ChatSettingsMutationResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	operation, err := chatSettingsOperation(req.Operation)
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	result := serverapi.ChatSettingsMutationResult{Kind: serverapi.ChatSettingsMutationApplied}
	switch req.Target.TargetKind {
	case serverapi.ChatSettingsReadTargetLazy:
		projectCtx, err := s.core.resolveProjectContext(ctx, *req.Target.ProjectID, *req.Target.WorkspaceID, "")
		if err != nil {
			return serverapi.ChatSettingsMutationResponse{}, err
		}
		service := s.core.sessionLaunchServiceForProjectContext(projectCtx)
		projected, err := service.MutateWorkspaceChatSettingsAggregate(ctx, operation)
		if err != nil {
			return serverapi.ChatSettingsMutationResponse{}, err
		}
		if projected.Rejection != nil {
			result.Kind = serverapi.ChatSettingsMutationRejected
			result.Rejection = &serverapi.ChatSettingsMutationRejection{Reason: string(projected.Rejection.Reason)}
		}
		result.Changed = projected.Changed
		settings, contextFacts, err := s.readLazySettingsAndContext(ctx, service)
		if err != nil {
			return serverapi.ChatSettingsMutationResponse{}, err
		}
		return serverapi.ChatSettingsMutationResponse{Result: result, Settings: settings, Context: contextFacts}, nil
	case serverapi.ChatSettingsReadTargetSession:
		return s.mutateMaterializedChatSettings(ctx, *req.Target.Session, operation, result)
	default:
		return serverapi.ChatSettingsMutationResponse{}, errors.New("Chat settings target kind is invalid")
	}
}

func chatSettingsOperation(operation serverapi.ChatSettingsMutationOperation) (sessionlaunch.ChatSettingsOperation, error) {
	switch operation.Kind {
	case serverapi.ChatSettingsMutationAgent:
		return sessionlaunch.ChatSettingsOperation{Kind: sessionlaunch.ChatSettingsOperationAgent, Value: *operation.Role}, nil
	case serverapi.ChatSettingsMutationSupervisor:
		return sessionlaunch.ChatSettingsOperation{Kind: sessionlaunch.ChatSettingsOperationSupervisor, Value: *operation.Value}, nil
	case serverapi.ChatSettingsMutationThinking:
		return sessionlaunch.ChatSettingsOperation{Kind: sessionlaunch.ChatSettingsOperationThinking, Value: *operation.Value}, nil
	case serverapi.ChatSettingsMutationFast:
		return sessionlaunch.ChatSettingsOperation{Kind: sessionlaunch.ChatSettingsOperationFast, Enabled: *operation.Enabled}, nil
	case serverapi.ChatSettingsMutationQuestions:
		return sessionlaunch.ChatSettingsOperation{Kind: sessionlaunch.ChatSettingsOperationQuestions, Enabled: *operation.Enabled}, nil
	case serverapi.ChatSettingsMutationAutoCompaction:
		return sessionlaunch.ChatSettingsOperation{Kind: sessionlaunch.ChatSettingsOperationAutoCompaction, Enabled: *operation.Enabled}, nil
	default:
		return sessionlaunch.ChatSettingsOperation{}, fmt.Errorf("Chat settings operation kind %q is invalid", operation.Kind)
	}
}

func (s chatSettingsService) readLazySettingsAndContext(
	ctx context.Context,
	service *sessionlaunch.Service,
) (serverapi.ChatSettings, serverapi.ChatContext, error) {
	settings, err := service.LazyChatSettings(ctx)
	if err != nil {
		return serverapi.ChatSettings{}, serverapi.ChatContext{}, err
	}
	contextFacts, err := service.ReadWorkspaceChatContext(ctx)
	if err != nil {
		return serverapi.ChatSettings{}, serverapi.ChatContext{}, err
	}
	return settings.Settings, contextFacts, nil
}

func (s chatSettingsService) mutateMaterializedChatSettings(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	operation sessionlaunch.ChatSettingsOperation,
	result serverapi.ChatSettingsMutationResult,
) (serverapi.ChatSettingsMutationResponse, error) {
	store := s.core.safeBundles().Persistence.metadataStore
	authority := s.core.safeBundles().Runtime.runtimeAuthority
	if store == nil || authority == nil {
		return serverapi.ChatSettingsMutationResponse{}, errors.New("Chat settings runtime authority is required")
	}
	var changed bool
	err := authority.WithSessionChatSettings(ctx, sessionID.String(), func(
		runCtx context.Context,
		sessionStore *session.Store,
		engine *runtime.Engine,
	) (bool, error) {
		service, err := s.materializedService(runCtx, sessionID.String())
		if err != nil {
			return false, err
		}
		input, err := service.PrepareMaterializedChatSettingsOperation(runCtx, sessionStore)
		if err != nil {
			return false, err
		}
		projected, err := sessionlaunch.ProjectPreparedChatSettingsOperation(input, operation)
		if err != nil {
			return false, err
		}
		if projected.Rejection != nil {
			result.Kind = serverapi.ChatSettingsMutationRejected
			result.Rejection = &serverapi.ChatSettingsMutationRejection{Reason: string(projected.Rejection.Reason)}
			return false, nil
		}
		if engine != nil {
			if _, err := engine.PrepareReviewerFrequency(projected.Effective.Supervisor); err != nil {
				return false, err
			}
			if projected.Effective.Fast && !engine.FastModeAvailable() {
				return false, errors.New("fast mode is only available for OpenAI-based Responses providers")
			}
		}
		committed, err := sessionStore.CommitChatSettingsState(projected.State)
		if err != nil && !committed.Committed {
			return false, err
		}
		changed = committed.Changed
		if engine != nil && projected.State.Agent == input.Raw.Agent {
			if err := engine.SetThinkingLevel(projected.Effective.Thinking); err != nil {
				return false, err
			}
			if _, err := engine.SetFastModeEnabled(projected.Effective.Fast); err != nil {
				return false, err
			}
			engine.SetReviewerFrequency(projected.Effective.Supervisor)
			engine.SetQuestionsEnabled(projected.Effective.Questions)
			engine.SetAutoCompactionEnabled(projected.Effective.AutoCompaction)
		}
		return projected.State.Agent != input.Raw.Agent && changed, err
	})
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	result.Changed = changed
	service, err := s.materializedService(ctx, sessionID.String())
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	settings, err := service.MaterializedChatSettings(ctx, sessionID)
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	contextFacts, err := s.core.safeBundles().Sessions.sessionContextOwner.ReadSessionChatContext(ctx, sessionID)
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	if result.Kind == serverapi.ChatSettingsMutationApplied {
		if err := s.core.safeBundles().Runtime.runtimeRegistry.PublishSessionStatus(sessionID.String()); err != nil {
			return serverapi.ChatSettingsMutationResponse{}, err
		}
	}
	return serverapi.ChatSettingsMutationResponse{
		Result: result, Settings: settings.Settings, Session: settings.Session, Context: contextFacts,
	}, nil
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
