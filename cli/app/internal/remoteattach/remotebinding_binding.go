package remoteattach

import (
	"context"
	"errors"
	"strings"

	"core/shared/client"
	"core/shared/config"
)

type projectWorkspaceDialer func(context.Context, string, string) (*client.Remote, error)
type sessionDialer func(context.Context, string) (*client.Remote, error)

func BindProjectWorkspace(ctx context.Context, current *client.Remote, cfg config.App, projectID, workspaceID, rootID string) (*client.Remote, error) {
	return bindProjectWorkspace(ctx, current, projectID, workspaceID, rootID, func(ctx context.Context, projectID, workspaceID string) (*client.Remote, error) {
		if workspaceID != "" {
			return client.DialConfiguredRemoteForProjectWorkspaceID(ctx, cfg, projectID, workspaceID)
		}
		return client.DialConfiguredRemoteForProjectWorkspace(ctx, cfg, projectID, cfg.WorkspaceRoot)
	})
}

func BindSession(ctx context.Context, current *client.Remote, cfg config.App, sessionID, rootID string) (*client.Remote, error) {
	return bindSession(ctx, current, sessionID, rootID, func(ctx context.Context, sessionID string) (*client.Remote, error) {
		return client.DialConfiguredRemoteForSession(ctx, cfg, sessionID)
	})
}

func bindProjectWorkspace(ctx context.Context, current *client.Remote, projectID, workspaceID, rootID string, dial projectWorkspaceDialer) (*client.Remote, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("project id is required")
	}
	workspaceID = strings.TrimSpace(workspaceID)
	return replaceRemoteAttachment(ctx, current, rootID, func(ctx context.Context) (*client.Remote, error) {
		return dial(ctx, projectID, workspaceID)
	})
}

func bindSession(ctx context.Context, current *client.Remote, sessionID, rootID string, dial sessionDialer) (*client.Remote, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	return replaceRemoteAttachment(ctx, current, rootID, func(ctx context.Context) (*client.Remote, error) {
		return dial(ctx, sessionID)
	})
}

func replaceRemoteAttachment(
	ctx context.Context,
	current *client.Remote,
	rootID string,
	dial func(context.Context) (*client.Remote, error),
) (*client.Remote, error) {
	if current == nil {
		return nil, errors.New("remote server is required")
	}
	if dial == nil {
		return nil, errors.New("remote attachment dialer is required")
	}
	nextRemote, err := dial(ctx)
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
