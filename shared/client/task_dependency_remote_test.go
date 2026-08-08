package client

import (
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestProtocolErrorDecodesWorkflowTaskDependencyError(t *testing.T) {
	currentCount := 50
	limit := 50
	source := &serverapi.WorkflowTaskDependencyError{
		Reason:        serverapi.WorkflowTaskDependencyErrorReasonBlockerLimit,
		BlockerTaskID: "task-blocker",
		BlockedTaskID: "task-blocked",
		CurrentCount:  &currentCount,
		Limit:         &limit,
	}
	decoded := protocolError(&protocol.ResponseError{
		Code:    source.RPCErrorCode(),
		Message: source.Error(),
		Data:    source.RPCErrorData(),
	})
	var typed *serverapi.WorkflowTaskDependencyError
	if !errors.As(decoded, &typed) {
		t.Fatalf("decoded error = %T, want *WorkflowTaskDependencyError", decoded)
	}
	if typed.Reason != source.Reason {
		t.Fatalf("decoded reason = %q, want %q", typed.Reason, source.Reason)
	}
}
