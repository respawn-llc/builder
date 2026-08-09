package serverapi

import (
	"errors"
	"fmt"

	"core/shared/protocol"
)

type WorkspaceChatDraftOperationKind string

const (
	WorkspaceChatDraftReadMessage   WorkspaceChatDraftOperationKind = "read_message"
	WorkspaceChatDraftUpdateMessage WorkspaceChatDraftOperationKind = "update_message"
	WorkspaceChatDraftClear         WorkspaceChatDraftOperationKind = "clear"
	WorkspaceChatDraftConsume       WorkspaceChatDraftOperationKind = "consume"
)

type WorkspaceChatDraftOperation struct {
	Kind    WorkspaceChatDraftOperationKind `json:"kind"`
	Message *string                         `json:"message,omitempty"`
}

func (o WorkspaceChatDraftOperation) Validate() error {
	switch o.Kind {
	case WorkspaceChatDraftReadMessage, WorkspaceChatDraftClear, WorkspaceChatDraftConsume:
		if o.Message != nil {
			return fmt.Errorf("%s operation must not contain message", o.Kind)
		}
	case WorkspaceChatDraftUpdateMessage:
		if o.Message == nil {
			return errors.New("update_message operation requires message")
		}
	default:
		return fmt.Errorf("workspace Chat draft operation kind %q is invalid", o.Kind)
	}
	return nil
}

func (o *WorkspaceChatDraftOperation) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Kind    WorkspaceChatDraftOperationKind `json:"kind"`
		Message *string                         `json:"message"`
	}
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	o.Kind, o.Message = decoded.Kind, decoded.Message
	return o.Validate()
}

type WorkspaceChatDraftRequest struct {
	Operation WorkspaceChatDraftOperation `json:"operation"`
}

func (r WorkspaceChatDraftRequest) Validate() error {
	return r.Operation.Validate()
}

func (r *WorkspaceChatDraftRequest) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Operation WorkspaceChatDraftOperation `json:"operation"`
	}
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	r.Operation = decoded.Operation
	return r.Validate()
}

type WorkspaceChatDraftResponse struct {
	Message string `json:"message"`
}
