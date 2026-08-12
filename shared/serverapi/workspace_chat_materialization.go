package serverapi

import (
	"errors"

	"core/shared/protocol"
	"core/shared/runtimeids"
)

type WorkspaceChatMaterializeRequest struct{}

func (WorkspaceChatMaterializeRequest) Validate() error {
	return nil
}

func (r *WorkspaceChatMaterializeRequest) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("workspace Chat materialization request is required")
	}
	type wire WorkspaceChatMaterializeRequest
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = WorkspaceChatMaterializeRequest(decoded)
	return nil
}

type WorkspaceChatMaterializeResponse struct {
	SessionID runtimeids.SessionID `json:"session_id"`
}

func (r WorkspaceChatMaterializeResponse) Validate() error {
	if r.SessionID.IsZero() || !r.SessionID.IsCanonicalUUIDv4() {
		return errors.New("materialized Session id must be a canonical UUIDv4")
	}
	return nil
}

func (r *WorkspaceChatMaterializeResponse) UnmarshalJSON(data []byte) error {
	if r == nil {
		return errors.New("workspace Chat materialization response is required")
	}
	type wire WorkspaceChatMaterializeResponse
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = WorkspaceChatMaterializeResponse(decoded)
	return r.Validate()
}
