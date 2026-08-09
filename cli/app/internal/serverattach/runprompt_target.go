package serverattach

import (
	"context"
	"errors"
	"strings"
	"time"

	"core/cli/app/internal/remoteattach"
	"core/shared/apicontract"
	"core/shared/client"
	"core/shared/config"
)

type AttachRunPromptRequest struct {
	Config           config.App
	AttachTimeout    time.Duration
	DiscoveryTimeout time.Duration
	DialProjectView  remoteattach.DialProjectView
	DialWorkspace    remoteattach.DialWorkspace
	EnsureAuthReady  func(context.Context, apicontract.AuthBootstrapService) error
}

func AttachRunPrompt(ctx context.Context, req AttachRunPromptRequest) (apicontract.RunPromptService, func() error, error) {
	remote, err := attachRunPromptRemote(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if err := validateRunPromptRemote(ctx, req, remote); err != nil {
		return nil, nil, closeRunPromptValidationFailure(err, remote.Close)
	}
	return remote, remote.Close, nil
}

func validateRunPromptRemote(ctx context.Context, req AttachRunPromptRequest, remote *client.Remote) error {
	if req.EnsureAuthReady != nil {
		if err := req.EnsureAuthReady(ctx, remote); err != nil {
			return err
		}
	}
	if strings.TrimSpace(remote.ProjectID()) == "" {
		return remoteattach.HeadlessWorkspaceRegistrationError(req.Config.WorkspaceRoot)
	}
	return nil
}

func closeRunPromptValidationFailure(validationErr error, closeRemote func() error) error {
	return errors.Join(validationErr, closeRemote())
}
