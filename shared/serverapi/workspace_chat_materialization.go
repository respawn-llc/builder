package serverapi

import (
	"errors"

	"core/shared/runtimeids"
)

type WorkspaceChatMaterializeRequest struct{}

func (WorkspaceChatMaterializeRequest) Validate() error {
	return nil
}

type WorkspaceChatMaterializeResponse struct {
	SessionID runtimeids.SessionID
}

func (r WorkspaceChatMaterializeResponse) Validate() error {
	if r.SessionID.IsZero() || !r.SessionID.IsCanonicalUUIDv4() {
		return errors.New("materialized Session id must be a canonical UUIDv4")
	}
	return nil
}
