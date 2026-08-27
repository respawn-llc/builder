package serverapi

import (
	"encoding/json"
	"errors"

	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

const PendingWorkCapacity = runtimeinput.PendingWorkCapacity

var (
	ErrPendingWorkCapacity   = runtimeinput.ErrPendingWorkCapacity
	ErrPendingWorkNotPending = runtimeinput.ErrPendingWorkNotPending
)

type PendingWorkRemovalError = runtimeinput.PendingWorkRemovalError
type PendingWorkLane = runtimeinput.PendingWorkLane
type PendingWorkItemKind = runtimeinput.PendingWorkItemKind
type PendingWorkItemState = runtimeinput.PendingWorkItemState
type PendingWorkMessage = runtimeinput.PendingWorkMessage
type PendingWorkManualCompaction = runtimeinput.PendingWorkManualCompaction
type PendingWorkWorktreeTransitionKind = runtimeinput.PendingWorkWorktreeTransitionKind
type PendingWorkWorktreeTransition = runtimeinput.PendingWorkWorktreeTransition
type PendingWorkItem = runtimeinput.PendingWorkItem
type PendingWork = runtimeinput.PendingWork
type PendingWorkRestoration = runtimeinput.PendingWorkRestoration
type ManualCompactionAdmission = runtimeinput.ManualCompactionAdmission

type PendingWorkCapacityError struct{}

func (*PendingWorkCapacityError) Error() string { return ErrPendingWorkCapacity.Error() }
func (*PendingWorkCapacityError) Unwrap() error { return ErrPendingWorkCapacity }
func (*PendingWorkCapacityError) RPCErrorCode() int {
	return protocol.ErrCodePendingWorkCapacity
}
func (*PendingWorkCapacityError) RPCErrorData() json.RawMessage {
	return json.RawMessage(`{"reason":"capacity"}`)
}

var _ protocol.StructuredRPCError = (*PendingWorkCapacityError)(nil)

func DecodePendingWorkCapacityError(data json.RawMessage) error {
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Reason != "capacity" {
		return errors.New("Pending Work capacity error has invalid reason")
	}
	return &PendingWorkCapacityError{}
}

type PendingWorkNotPendingError struct {
	ItemID runtimeids.QueueItemID
}

func (e *PendingWorkNotPendingError) Error() string {
	return (&runtimeinput.PendingWorkRemovalError{ItemID: e.ItemID}).Error()
}
func (*PendingWorkNotPendingError) Unwrap() error     { return ErrPendingWorkNotPending }
func (*PendingWorkNotPendingError) RPCErrorCode() int { return protocol.ErrCodePendingWorkNotPending }
func (e *PendingWorkNotPendingError) RPCErrorData() json.RawMessage {
	if e == nil || e.ItemID.IsZero() {
		return marshalRPCErrorData(struct {
			InvalidItemID bool `json:"invalid_item_id"`
		}{true})
	}
	return marshalRPCErrorData(struct {
		ItemID runtimeids.QueueItemID `json:"item_id"`
	}{e.ItemID})
}

func DecodePendingWorkNotPendingError(data json.RawMessage) error {
	var payload struct {
		ItemID runtimeids.QueueItemID `json:"item_id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.ItemID.IsZero() {
		return errors.New("Pending Work not-pending error has invalid item id")
	}
	return &PendingWorkNotPendingError{ItemID: payload.ItemID}
}

const (
	PendingWorkLaneSteer                  = runtimeinput.PendingWorkLaneSteer
	PendingWorkLaneQueue                  = runtimeinput.PendingWorkLaneQueue
	PendingWorkItemKindMessage            = runtimeinput.PendingWorkItemKindMessage
	PendingWorkItemKindManualCompaction   = runtimeinput.PendingWorkItemKindManualCompaction
	PendingWorkItemKindWorktreeTransition = runtimeinput.PendingWorkItemKindWorktreeTransition

	PendingWorkItemStatePending = runtimeinput.PendingWorkItemStatePending

	PendingWorkWorktreeTransitionEnter = runtimeinput.PendingWorkWorktreeTransitionEnter
	PendingWorkWorktreeTransitionLeave = runtimeinput.PendingWorkWorktreeTransitionLeave
)

func PendingWorkItemIDFromCompactionRequest(id runtimeids.CompactionRequestID) (runtimeids.QueueItemID, error) {
	return runtimeids.ParseQueueItemID(id.String())
}

func PendingWorkItemIDFromWorktreeOperation(id WorktreeOperationID) (runtimeids.QueueItemID, error) {
	return runtimeids.ParseQueueItemID(id.String())
}

type RuntimeListPendingWorkRequest struct {
	SessionID string `json:"session_id"`
}

func (r RuntimeListPendingWorkRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

type RuntimeListPendingWorkResponse struct {
	PendingWork PendingWork `json:"pending_work"`
}

func (r RuntimeListPendingWorkResponse) Validate() error {
	return r.PendingWork.Validate()
}

type RuntimeRemovePendingWorkRequest struct {
	SessionID string                 `json:"session_id"`
	ItemID    runtimeids.QueueItemID `json:"item_id"`
}

func (r RuntimeRemovePendingWorkRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if r.ItemID.IsZero() {
		return errors.New("Pending Work item id is required")
	}
	return nil
}

type RuntimeRemovePendingWorkResponse struct {
	Restoration PendingWorkRestoration `json:"restoration"`
}

func (r RuntimeRemovePendingWorkResponse) Validate() error {
	return r.Restoration.Validate()
}
