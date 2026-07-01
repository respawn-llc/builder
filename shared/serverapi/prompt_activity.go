package serverapi

import (
	"context"

	"core/shared/clientui"
)

type PromptActivitySubscribeRequest struct {
	SessionID             string
	AfterReadModelVersion clientui.ReadModelVersion
}

type PromptActivitySubscription interface {
	Next(ctx context.Context) (clientui.PendingPromptEvent, error)
	Close() error
}

func (r PromptActivitySubscribeRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if r.AfterReadModelVersion == (clientui.ReadModelVersion{}) {
		return nil
	}
	return r.AfterReadModelVersion.Validate()
}
