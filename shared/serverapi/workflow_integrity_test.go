package serverapi

import (
	"errors"
	"reflect"
	"testing"

	"core/shared/protocol"
)

func TestWorkflowTaskIntegrityErrorRoundTripsStructuredRPCData(t *testing.T) {
	runID := "run-integrity"
	sessionID := "session-integrity"
	generation := int64(3)
	source := &WorkflowTaskIntegrityError{
		Reason:      WorkflowTaskIntegrityReasonExactExecutionMissing,
		TaskID:      "task-integrity",
		PlacementID: "placement-integrity",
		NodeID:      "node-integrity",
		NodeKind:    "agent",
		RunID:       &runID,
		SessionID:   &sessionID,
		Generation:  &generation,
		StatusKind:  WorkflowTaskStatusKindRunning,
		Durable: WorkflowTaskIntegrityDurableFacts{
			RunPresent: true,
			Started:    true,
		},
		Actions: WorkflowTaskIntegrityActionFacts{},
	}
	if source.RPCErrorCode() != protocol.ErrCodeWorkflowTaskIntegrity {
		t.Fatalf("RPC error code = %d", source.RPCErrorCode())
	}

	decodedErr := DecodeWorkflowTaskIntegrityError(source.RPCErrorData(), source.Error())
	var decoded *WorkflowTaskIntegrityError
	if !errors.As(decodedErr, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowTaskIntegrityError", decodedErr, decodedErr)
	}
	if !reflect.DeepEqual(decoded, source) {
		t.Fatalf("decoded error = %+v, want %+v", decoded, source)
	}
}
