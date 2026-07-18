package app

import (
	"context"
	"errors"
	"time"

	servicecontract "core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/toolspec"

	"github.com/google/uuid"
)

const runtimeReleaseTimeout = 3 * time.Second

type runtimeActivationRequest struct {
	SessionID      string
	ActiveSettings config.Settings
	EnabledTools   []toolspec.ID
	Source         config.SourceReport
}

type runtimeActivation struct {
	OwnerID    string
	Reactivate func(context.Context) error
}

func activateRuntime(ctx context.Context, service servicecontract.SessionRuntimeService, req runtimeActivationRequest) (runtimeActivation, error) {
	if service == nil {
		return runtimeActivation{}, errors.New("session runtime service is required")
	}
	ownerID := uuid.NewString()
	if _, err := service.ActivateSessionRuntime(ctx, runtimeActivationRPCRequest(req, ownerID)); err != nil {
		return runtimeActivation{}, err
	}
	return runtimeActivation{
		OwnerID: ownerID,
		Reactivate: func(ctx context.Context) error {
			_, err := service.ActivateSessionRuntime(ctx, runtimeActivationRPCRequest(req, ownerID))
			return err
		},
	}, nil
}

func releaseRuntime(service servicecontract.SessionRuntimeService, sessionID string, ownerID string) error {
	return releaseRuntimeWithClosePolicy(service, sessionID, ownerID, serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle)
}

func releaseRuntimeWithClosePolicy(service servicecontract.SessionRuntimeService, sessionID string, ownerID string, closePolicy serverapi.SessionRuntimeReleaseClosePolicy) error {
	if service == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeReleaseTimeout)
	defer cancel()
	_, err := service.ReleaseSessionRuntime(ctx, serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       sessionID,
		OnlyIfIdle:      true,
		DropOwner:       true,
		ClosePolicy:     closePolicy,
		OwnerID:         ownerID,
	})
	return err
}

func runtimeActivationRPCRequest(req runtimeActivationRequest, ownerID string) serverapi.SessionRuntimeActivateRequest {
	return serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       req.SessionID,
		OwnerID:         ownerID,
		ActiveSettings:  req.ActiveSettings,
		EnabledToolIDs:  toolspec.IDStrings(req.EnabledTools),
		Source:          req.Source,
	}
}
