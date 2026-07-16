package serverapi

import (
	"context"

	"core/shared/clientui"
)

type TranscriptSubscribeRequest struct {
	SessionID string
}

type TranscriptSubscription interface {
	Next(ctx context.Context) (clientui.TranscriptMessage, error)
	Close() error
}

func (r TranscriptSubscribeRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
