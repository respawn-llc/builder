package serverapi

import (
	"context"

	"core/shared/clientui"
)

type SessionActivitySubscribeRequest struct {
	SessionID     string
	AfterSequence uint64
}

type SessionActivitySubscription interface {
	Next(ctx context.Context) (clientui.Event, error)
	Close() error
}

type TranscriptSubscribeRequest struct {
	SessionID string
}

type TranscriptSubscription interface {
	Next(ctx context.Context) (clientui.TranscriptMessage, error)
	Close() error
}

func (r SessionActivitySubscribeRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

func (r TranscriptSubscribeRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

type SessionTranscriptSubscribeRequest = TranscriptSubscribeRequest
type SessionTranscriptSubscription = TranscriptSubscription
