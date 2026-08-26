package core

import (
	"context"
	"errors"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
)

type sessionChatSettingsPreparationResolver struct {
	metadataStore   *metadata.Store
	authManager     *auth.Manager
	persistenceRoot string
}

func (r sessionChatSettingsPreparationResolver) PrepareSessionChatSettings(
	ctx context.Context,
	store *session.Store,
	agent string,
) (launch.PreparedChatSettings, error) {
	if r.metadataStore == nil {
		return launch.PreparedChatSettings{}, errors.New("metadata store is required")
	}
	if store == nil {
		return launch.PreparedChatSettings{}, errors.New("Session store is required")
	}
	sessionID := strings.TrimSpace(store.Meta().SessionID)
	target, err := r.metadataStore.ResolveSessionExecutionTarget(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return launch.PreparedChatSettings{}, err
	}
	cfg, err := config.Load(target.WorkspaceRoot, config.LoadOptions{ConfigRoot: r.persistenceRoot})
	if err != nil {
		return launch.PreparedChatSettings{}, err
	}
	authState := auth.EmptyState()
	if r.authManager != nil {
		authState, err = r.authManager.CurrentState(ctx)
		if err != nil {
			return launch.PreparedChatSettings{}, err
		}
	}
	promptFacing, err := (launch.Planner{Config: cfg}).SelectedSessionPromptFacingTargetFromMeta(store.Meta())
	if err != nil {
		return launch.PreparedChatSettings{}, err
	}
	return launch.PrepareSessionChatSettingsForAgent(cfg, authState, agent, promptFacing)
}
