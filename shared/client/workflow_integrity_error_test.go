package client

import (
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestProtocolErrorDecodesWorkflowTaskIntegrityError(t *testing.T) {
	runID := "run-integrity"
	generation := int64(1)
	source := &serverapi.WorkflowTaskIntegrityError{
		Reason:      serverapi.WorkflowTaskIntegrityReasonExactExecutionMissing,
		TaskID:      "task-integrity",
		PlacementID: "placement-integrity",
		NodeID:      "node-integrity",
		NodeKind:    "agent",
		RunID:       &runID,
		Generation:  &generation,
		StatusKind:  serverapi.WorkflowTaskStatusKindRunning,
		Durable: serverapi.WorkflowTaskIntegrityDurableFacts{
			RunPresent: true,
			Started:    true,
		},
	}
	decodedErr := protocolError(&protocol.ResponseError{
		Code:    source.RPCErrorCode(),
		Message: source.Error(),
		Data:    source.RPCErrorData(),
	})
	var decoded *serverapi.WorkflowTaskIntegrityError
	if !errors.As(decodedErr, &decoded) {
		t.Fatalf("decoded error = %T %v, want WorkflowTaskIntegrityError", decodedErr, decodedErr)
	}
	if decoded.TaskID != source.TaskID ||
		decoded.RunID == nil ||
		*decoded.RunID != runID ||
		decoded.Reason != source.Reason {
		t.Fatalf("decoded error = %+v", decoded)
	}
}
