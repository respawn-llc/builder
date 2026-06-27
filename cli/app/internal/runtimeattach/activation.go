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
	SessionID          string
	ActiveSettings     config.Settings
	EnabledTools       []toolspec.ID
	Source             config.SourceReport
	NewClientRequestID func() string
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

func Release(service servicecontract.SessionRuntimeService, sessionID string, ownerID string) {
	if service == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), ReleaseTimeout)
	defer cancel()
	_, _ = service.ReleaseSessionRuntime(ctx, serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: newClientRequestID(nil),
		SessionID:       sessionID,
		OnlyIfIdle:      true,
		DropOwner:       true,
		OwnerID:         ownerID,
	})
}

func activateRequest(req Request, ownerID string) serverapi.SessionRuntimeActivateRequest {
	return serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: newClientRequestID(req.NewClientRequestID),
		SessionID:       req.SessionID,
		OwnerID:         ownerID,
		ActiveSettings:  req.ActiveSettings,
		EnabledToolIDs:  toolIDs(req.EnabledTools),
		Source:          req.Source,
	}
}

func toolIDs(enabledTools []toolspec.ID) []string {
	ids := make([]string, 0, len(enabledTools))
	for _, id := range enabledTools {
		ids = append(ids, string(id))
	}
	return ids
}

func newClientRequestID(newID func() string) string {
	if newID == nil {
		return uuid.NewString()
	}
	return newID()
}
