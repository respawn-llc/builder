package serverapi

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
)

func TestPendingWorkCapacityStructuredRPCDirectAndNested(t *testing.T) {
	direct := &PendingWorkCapacityError{}
	decoded := DecodePendingWorkCapacityError(direct.RPCErrorData())
	var typed *PendingWorkCapacityError
	if direct.RPCErrorCode() != protocol.ErrCodePendingWorkCapacity ||
		!errors.Is(decoded, ErrPendingWorkCapacity) ||
		!errors.As(decoded, &typed) {
		t.Fatalf("direct capacity error = %T %v", decoded, decoded)
	}

	nested := NewRuntimeCommandNotAcceptedError(&PendingWorkCapacityError{})
	var payload struct {
		Cause protocol.ResponseError `json:"cause"`
	}
	if err := json.Unmarshal(nested.RPCErrorData(), &payload); err != nil {
		t.Fatalf("decode nested capacity error: %v", err)
	}
	if payload.Cause.Code != protocol.ErrCodePendingWorkCapacity {
		t.Fatalf("nested capacity code = %d", payload.Cause.Code)
	}
	decoded = DecodePendingWorkCapacityError(payload.Cause.Data)
	if !errors.Is(decoded, ErrPendingWorkCapacity) || !errors.As(decoded, &typed) {
		t.Fatalf("nested capacity error = %T %v", decoded, decoded)
	}

	if err := DecodePendingWorkCapacityError(json.RawMessage(`{"reason":"other"}`)); err == nil {
		t.Fatal("invalid capacity reason decoded as typed capacity")
	}
}

func TestPendingWorkIdentityConflictStructuredRPCDirectAndNested(t *testing.T) {
	itemID := runtimeids.NewQueueItemID()
	direct := &PendingWorkIdentityConflictError{ItemID: itemID}
	decoded := DecodePendingWorkIdentityConflictError(direct.RPCErrorData())
	var typed *PendingWorkIdentityConflictError
	if direct.RPCErrorCode() != protocol.ErrCodePendingWorkIdentityConflict ||
		!errors.Is(decoded, ErrPendingWorkIdentityConflict) ||
		!errors.As(decoded, &typed) ||
		typed.ItemID != itemID {
		t.Fatalf("direct identity-conflict error = %T %v", decoded, decoded)
	}

	nested := NewRuntimeCommandNotAcceptedError(direct)
	var payload struct {
		Cause protocol.ResponseError `json:"cause"`
	}
	if err := json.Unmarshal(nested.RPCErrorData(), &payload); err != nil {
		t.Fatalf("decode nested identity-conflict error: %v", err)
	}
	if payload.Cause.Code != protocol.ErrCodePendingWorkIdentityConflict {
		t.Fatalf("nested identity-conflict code = %d", payload.Cause.Code)
	}
	decoded = DecodePendingWorkIdentityConflictError(payload.Cause.Data)
	if !errors.Is(decoded, ErrPendingWorkIdentityConflict) ||
		!errors.As(decoded, &typed) ||
		typed.ItemID != itemID {
		t.Fatalf("nested identity-conflict error = %T %v", decoded, decoded)
	}

	if err := DecodePendingWorkIdentityConflictError(json.RawMessage(`{"item_id":""}`)); err == nil {
		t.Fatal("invalid identity-conflict item id decoded as typed conflict")
	}
}

func TestPendingWorkIdentityViewsReuseDomainUUID(t *testing.T) {
	t.Parallel()

	compactionID := runtimeids.NewCompactionRequestID()
	got, err := PendingWorkItemIDFromCompactionRequest(compactionID)
	if err != nil {
		t.Fatalf("compaction Pending Work id: %v", err)
	}
	if got.String() != compactionID.String() {
		t.Fatalf("compaction Pending Work id = %q, want %q", got, compactionID)
	}
	worktreeID := NewWorktreeOperationID()
	got, err = PendingWorkItemIDFromWorktreeOperation(worktreeID)
	if err != nil {
		t.Fatalf("Worktree Pending Work id: %v", err)
	}
	if got.String() != worktreeID.String() {
		t.Fatalf("Worktree Pending Work id = %q, want %q", got, worktreeID)
	}
}

func TestPendingWorkRemovalResponseValidatesTypedCanonicalRestoration(t *testing.T) {
	t.Parallel()

	valid := RuntimeRemovePendingWorkResponse{Restoration: PendingWorkRestoration{
		Kind: PendingWorkItemKindWorktreeTransition, CanonicalInput: "/wt leave",
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	invalid := valid
	invalid.Restoration.CanonicalInput = ""
	if err := invalid.Validate(); err == nil {
		t.Fatal("Validate accepted missing canonical input")
	}
}
