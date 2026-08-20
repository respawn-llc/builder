package serverapi

import (
	"errors"
	"fmt"

	"core/shared/clientui"
)

type WorkspaceChatDraftOperationKind string

const (
	WorkspaceChatDraftReadMessage   WorkspaceChatDraftOperationKind = "read_message"
	WorkspaceChatDraftUpdateMessage WorkspaceChatDraftOperationKind = "update_message"
	WorkspaceChatDraftClear         WorkspaceChatDraftOperationKind = "clear"
)

type WorkspaceChatDraftOperation struct {
	Kind    WorkspaceChatDraftOperationKind
	Message *string
}

func (o WorkspaceChatDraftOperation) Validate() error {
	switch o.Kind {
	case WorkspaceChatDraftReadMessage, WorkspaceChatDraftClear:
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

type WorkspaceChatDraftRequest struct {
	Operation WorkspaceChatDraftOperation
}

type WorkspaceChatDraftResponse struct {
	Message          string
	GoalAvailability clientui.GoalAvailability
}

func (r WorkspaceChatDraftResponse) Validate() error {
	return r.GoalAvailability.Validate()
}
