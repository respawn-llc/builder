package serverapi

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
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
