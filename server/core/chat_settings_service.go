package core

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"core/server/runtime"
	"core/server/session"
	"core/server/sessionlaunch"
	"core/shared/clientui"
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
	result := serverapi.NewChatSettingsMutationApplied(false)
	switch req.Target.TargetKind {
	case serverapi.ChatSettingsReadTargetLazy:
		projectCtx, err := s.core.resolveProjectContext(ctx, *req.Target.ProjectID, *req.Target.WorkspaceID, "")
		if err != nil {
			return serverapi.ChatSettingsMutationResponse{}, err
		}
		service := s.core.sessionLaunchServiceForProjectContext(projectCtx)
		projected, changed, err := service.MutateWorkspaceChatSettingsAggregate(ctx, req.Operation)
		if err != nil {
			return serverapi.ChatSettingsMutationResponse{}, err
		}
		if projected.Rejection != nil {
			result = serverapi.NewChatSettingsMutationRejected(projected.Rejection.Reason)
		}
		if result.Applied != nil {
			result.Applied.Changed = changed
		}
		settings, err := service.LazyChatSettings(ctx)
		if err != nil {
			return serverapi.ChatSettingsMutationResponse{}, err
		}
		contextFacts, err := service.ReadWorkspaceChatContext(ctx)
		if err != nil {
			return serverapi.ChatSettingsMutationResponse{}, err
		}
		return serverapi.ChatSettingsMutationResponse{Result: result, Settings: settings.Settings, Context: contextFacts}, nil
	case serverapi.ChatSettingsReadTargetSession:
		return s.mutateMaterializedChatSettings(ctx, *req.Target.Session, req.Operation, result)
	}
	return serverapi.ChatSettingsMutationResponse{}, errors.New("Chat settings target kind is invalid")
}
func (s chatSettingsService) mutateMaterializedChatSettings(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	operation serverapi.ChatSettingsMutationOperation,
	result serverapi.ChatSettingsMutationResult,
) (serverapi.ChatSettingsMutationResponse, error) {
	authority := s.core.safeBundles().Runtime.runtimeAuthority
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
			result = serverapi.NewChatSettingsMutationRejected(projected.Rejection.Reason)
			return false, nil
		}
		if engine != nil && projected.State.Agent == input.Raw.Agent {
			if _, err := engine.PrepareReviewerFrequency(projected.Effective.Supervisor); err != nil {
				return false, err
			}
			if projected.Effective.Fast && !engine.FastModeAvailable() {
				return false, errors.New("fast mode is only available for OpenAI-based Responses providers")
			}
		}
		committed, commitErr := sessionStore.CommitChatSettingsState(projected.State)
		if commitErr != nil && !committed.Committed {
			return false, commitErr
		}
		if commitErr != nil {
			slog.ErrorContext(
				runCtx,
				"Chat settings persistence notification failed after durable commit",
				"session_id", sessionID.String(),
				"error", commitErr,
			)
		}
		changed = committed.Changed
		if engine != nil && projected.State.Agent == input.Raw.Agent {
			if err := engine.AcceptPreparedChatSettings(projected.Effective); err != nil {
				return false, err
			}
		}
		return projected.State.Agent != input.Raw.Agent && changed, nil
	})
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	if result.Applied != nil {
		result.Applied.Changed = changed
	}
	responseCtx := context.WithoutCancel(ctx)
	service, err := s.materializedService(responseCtx, sessionID.String())
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	settings, err := service.MaterializedChatSettings(responseCtx, sessionID)
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	contextFacts, err := s.core.safeBundles().Sessions.sessionContextOwner.ReadSessionChatContext(responseCtx, sessionID)
	if err != nil {
		return serverapi.ChatSettingsMutationResponse{}, err
	}
	if result.Kind == serverapi.ChatSettingsMutationApplied {
		registry := s.core.safeBundles().Runtime.runtimeRegistry
		feedback, publishFeedback, feedbackErr := transcriptSessionSettingFeedback(operation, changed, settings.Settings)
		if feedbackErr != nil {
			slog.ErrorContext(
				responseCtx,
				"Chat settings feedback projection failed after durable commit",
				"session_id", sessionID.String(),
				"error", feedbackErr,
			)
			publishFeedback = false
		}
		var publishErr error
		if publishFeedback {
			publishErr = registry.PublishSessionSettingFeedback(sessionID.String(), feedback)
		} else {
			publishErr = registry.PublishSessionStatus(sessionID.String())
		}
		if publishErr != nil {
			slog.ErrorContext(
				responseCtx,
				"Chat settings publication failed after durable commit",
				"session_id", sessionID.String(),
				"error", publishErr,
			)
		}
	}
	return serverapi.ChatSettingsMutationResponse{Result: result, Settings: settings.Settings, Session: settings.Session, Context: contextFacts}, nil
}

func transcriptSessionSettingFeedback(
	operation serverapi.ChatSettingsMutationOperation,
	changed bool,
	settings serverapi.ChatSettings,
) (clientui.TranscriptSessionSettingFeedback, bool, error) {
	feedback := clientui.TranscriptSessionSettingFeedback{Changed: changed}
	switch operation.Kind {
	case serverapi.ChatSettingsMutationAgent:
		return clientui.TranscriptSessionSettingFeedback{}, false, nil
	case serverapi.ChatSettingsMutationSupervisor:
		value := string(settings.Supervisor.Value)
		feedback.Kind = clientui.SessionSettingSupervisor
		feedback.Supervisor = &value
	case serverapi.ChatSettingsMutationThinking:
		value := strings.TrimSpace(settings.SelectedAgent.Thinking)
		feedback.Kind = clientui.SessionSettingThinking
		feedback.Thinking = &value
	case serverapi.ChatSettingsMutationFast:
		if settings.Fast == nil {
			return clientui.TranscriptSessionSettingFeedback{}, false, errors.New("applied Fast setting has no authoritative value")
		}
		feedback.Kind = clientui.SessionSettingFastMode
		feedback.FastMode = &settings.Fast.Value
	case serverapi.ChatSettingsMutationQuestions:
		feedback.Kind = clientui.SessionSettingQuestions
		feedback.Questions = &settings.Questions.Enabled
	case serverapi.ChatSettingsMutationAutoCompaction:
		feedback.Kind = clientui.SessionSettingAutoCompaction
		feedback.AutoCompaction = &settings.AutoCompaction.Stored
	default:
		return clientui.TranscriptSessionSettingFeedback{}, false, errors.New("applied Chat settings operation kind is invalid")
	}
	if err := feedback.Validate(); err != nil {
		return clientui.TranscriptSessionSettingFeedback{}, false, err
	}
	return feedback, true, nil
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
