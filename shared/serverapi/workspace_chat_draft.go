package serverapi

import (
	"errors"
	"fmt"

	"core/shared/clientui"
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
	type wire WorkspaceChatDraftOperation
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*o = WorkspaceChatDraftOperation(decoded)
	return o.Validate()
}

type WorkspaceChatDraftRequest struct {
	Operation WorkspaceChatDraftOperation `json:"operation"`
}

func (r *WorkspaceChatDraftRequest) UnmarshalJSON(data []byte) error {
	type wire WorkspaceChatDraftRequest
	var decoded wire
	if err := protocol.DecodeStrictJSON(data, &decoded); err != nil {
		return err
	}
	*r = WorkspaceChatDraftRequest(decoded)
	return r.Operation.Validate()
}

type WorkspaceChatDraftResponse struct {
	Message          string                    `json:"message"`
	GoalAvailability clientui.GoalAvailability `json:"goal_availability"`
}

func (r WorkspaceChatDraftResponse) Validate() error {
	return r.GoalAvailability.Validate()
}
