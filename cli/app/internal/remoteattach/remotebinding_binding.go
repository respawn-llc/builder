package remoteattach

import (
	"context"
	"errors"
	"strings"

	"core/shared/client"
	"core/shared/config"
)

type projectWorkspaceDialer func(context.Context, string, string) (*client.Remote, error)

func BindProjectWorkspace(ctx context.Context, current *client.Remote, cfg config.App, projectID, workspaceID, rootID string) (*client.Remote, error) {
	return bindProjectWorkspace(ctx, current, projectID, workspaceID, rootID, func(ctx context.Context, projectID, workspaceID string) (*client.Remote, error) {
		if workspaceID != "" {
			return client.DialConfiguredRemoteForProjectWorkspaceID(ctx, cfg, projectID, workspaceID)
		}
		return client.DialConfiguredRemoteForProjectWorkspace(ctx, cfg, projectID, cfg.WorkspaceRoot)
	})
}

func bindProjectWorkspace(ctx context.Context, current *client.Remote, projectID, workspaceID, rootID string, dial projectWorkspaceDialer) (*client.Remote, error) {
	if current == nil {
		return nil, errors.New("remote server is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	nextRemote, err := dial(ctx, projectID, workspaceID)
	if err != nil {
		return nil, err
	}
	if err := nextRemote.RequireRoot(rootID); err != nil {
		return nil, errors.Join(err, nextRemote.Close())
	}
	if current.NoAuthBootstrapAcknowledgementEnabled() {
		if err := nextRemote.EnableNoAuthBootstrapAcknowledgement(ctx); err != nil {
			return nil, errors.Join(err, nextRemote.Close())
		}
	}
	// The successor is fully established at this point. Remote.Close marks the
	// superseded connection closed before its transport teardown can fail, so a
	// teardown error cannot roll the binding back to a usable current remote.
	_ = current.Close()
	return nextRemote, nil
}
