package runtimeattach

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

const ReleaseTimeout = 3 * time.Second

type Request struct {
	SessionID      string
	ActiveSettings config.Settings
	EnabledTools   []toolspec.ID
	Source         config.SourceReport
}

type Activation struct {
	OwnerID    string
	Reactivate func(context.Context) error
}

func Activate(ctx context.Context, service servicecontract.SessionRuntimeService, req Request) (Activation, error) {
	if service == nil {
		return Activation{}, errors.New("session runtime service is required")
	}
	ownerID := uuid.NewString()
	if _, err := service.ActivateSessionRuntime(ctx, activateRequest(req, ownerID)); err != nil {
		return Activation{}, err
	}
	return Activation{
		OwnerID: ownerID,
		Reactivate: func(ctx context.Context) error {
			_, err := service.ActivateSessionRuntime(ctx, activateRequest(req, ownerID))
			return err
		},
	}, nil
}

func Release(service servicecontract.SessionRuntimeService, sessionID string, ownerID string) error {
	return ReleaseWithClosePolicy(service, sessionID, ownerID, serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle)
}

func ReleaseWithClosePolicy(service servicecontract.SessionRuntimeService, sessionID string, ownerID string, closePolicy serverapi.SessionRuntimeReleaseClosePolicy) error {
	if service == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), ReleaseTimeout)
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

func activateRequest(req Request, ownerID string) serverapi.SessionRuntimeActivateRequest {
	return serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: uuid.NewString(),
		SessionID:       req.SessionID,
		OwnerID:         ownerID,
		ActiveSettings:  req.ActiveSettings,
		EnabledToolIDs:  toolspec.IDStrings(req.EnabledTools),
		Source:          req.Source,
	}
}
