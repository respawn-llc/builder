package serverapi

import (
	"context"

	"core/shared/clientui"
)

type PromptActivitySubscribeRequest struct {
	SessionID     string
	AfterSequence uint64
}

type PromptActivitySubscription interface {
	Next(ctx context.Context) (clientui.PendingPromptEvent, error)
	Close() error
}

func (r PromptActivitySubscribeRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
