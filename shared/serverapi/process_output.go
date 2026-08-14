package serverapi

import (
	"context"
	"errors"
	"strings"

	"core/shared/clientui"
)

type ProcessOutputSubscribeRequest struct {
	ProcessID   string
	OffsetBytes int64
}

type ProcessOutputSubscription interface {
	Next(ctx context.Context) (clientui.ProcessOutputChunk, error)
	Close() error
}

func (r ProcessOutputSubscribeRequest) Validate() error {
	if strings.TrimSpace(r.ProcessID) == "" {
		return errors.New("process_id is required")
	}
	if r.OffsetBytes < 0 {
		return errors.New("offset_bytes must be >= 0")
	}
	return nil
}
