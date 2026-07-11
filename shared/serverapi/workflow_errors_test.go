package serverapi

import (
	"errors"
	"testing"

	"core/shared/protocol"
)

func TestWorkflowTaskLegacyExecutionTargetMissingErrorRoundTripsRPC(t *testing.T) {
	original := &WorkflowTaskLegacyExecutionTargetMissingError{TaskID: "task-legacy"}
	var structured protocol.StructuredRPCError = original
	if structured.RPCErrorCode() != protocol.ErrCodeWorkflowTaskLegacyExecutionTargetMissing {
		t.Fatalf("RPC error code = %d, want legacy target missing code", structured.RPCErrorCode())
	}

	decoded := DecodeWorkflowTaskLegacyExecutionTargetMissingError(structured.RPCErrorData(), original.Error())
	if !errors.Is(decoded, ErrWorkflowTaskLegacyExecutionTargetMissing) {
		t.Fatalf("decoded error = %v, want legacy target missing sentinel", decoded)
	}
	var typed *WorkflowTaskLegacyExecutionTargetMissingError
	if !errors.As(decoded, &typed) || typed.TaskID != original.TaskID {
		t.Fatalf("decoded error = %v, want task-scoped typed error", decoded)
	}
}
