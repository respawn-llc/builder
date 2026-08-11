package runtimeattach

import (
	"context"
	"errors"
	"sync"
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
	mu         sync.Mutex
	service    servicecontract.SessionRuntimeService
	request    Request
	ownerID    string
	attachment serverapi.SessionRuntimeAttachment
}

func Activate(ctx context.Context, service servicecontract.SessionRuntimeService, req Request) (*Activation, error) {
	if service == nil {
		return nil, errors.New("session runtime service is required")
	}
	ownerID := uuid.NewString()
	attachment, err := activate(ctx, service, req, ownerID)
	if err != nil {
		return nil, err
	}
	return &Activation{
		service:    service,
		request:    req,
		ownerID:    ownerID,
		attachment: attachment,
	}, nil
}

func (a *Activation) Reactivate(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	attachment, err := activate(ctx, a.service, a.request, a.ownerID)
	if err != nil {
		return err
	}
	a.attachment = attachment
	return nil
}

func (a *Activation) Release() error {
	return a.ReleaseWithClosePolicy(serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle)
}

func (a *Activation) ReleaseWithClosePolicy(closePolicy serverapi.SessionRuntimeReleaseClosePolicy) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), ReleaseTimeout)
	defer cancel()
	_, err := a.service.ReleaseSessionRuntime(ctx, serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: uuid.NewString(),
		Attachment:      a.attachment,
		DropOwner:       true,
		ClosePolicy:     closePolicy,
		OwnerID:         a.ownerID,
	})
	return err
}

func activate(ctx context.Context, service servicecontract.SessionRuntimeService, req Request, ownerID string) (serverapi.SessionRuntimeAttachment, error) {
	response, err := service.ActivateSessionRuntime(ctx, activateRequest(req, ownerID))
	if err != nil {
		return serverapi.SessionRuntimeAttachment{}, err
	}
	if err := response.ValidateForSession(req.SessionID); err != nil {
		return serverapi.SessionRuntimeAttachment{}, err
	}
	return response.Attachment, nil
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
