package core

import (
	"context"
	"errors"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/metadata"
	"core/shared/config"
)

type sessionChatSettingsPreparationResolver struct {
	metadataStore   *metadata.Store
	authManager     *auth.Manager
	persistenceRoot string
}

func (r sessionChatSettingsPreparationResolver) PrepareSessionChatSettings(
	ctx context.Context,
	sessionID string,
	agent string,
) (launch.PreparedChatSettings, error) {
	if r.metadataStore == nil {
		return launch.PreparedChatSettings{}, errors.New("metadata store is required")
	}
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
	return launch.PrepareChatSettingsForAgent(cfg, authState, agent)
}
