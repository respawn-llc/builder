package serverapi

import (
	"errors"
	"testing"
)

func TestWorkflowTaskResumeConflictErrorRoundTripsItsLifecycleState(t *testing.T) {
	source := &WorkflowTaskResumeConflictError{
		TaskID: "task-123",
		State:  WorkflowTaskResumeConflictMovedCurrentNode,
	}
	if err := source.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	decoded := DecodeWorkflowTaskResumeConflictError(source.RPCErrorData(), source.Error())
	var conflict *WorkflowTaskResumeConflictError
	if !errors.As(decoded, &conflict) {
		t.Fatalf("decoded error = %T %v, want typed conflict", decoded, decoded)
	}
	if conflict.TaskID != source.TaskID || conflict.State != source.State {
		t.Fatalf("decoded conflict = %+v, want %+v", conflict, source)
	}
}

func TestWorkflowTaskResumeConflictErrorRequiresValidLifecycleState(t *testing.T) {
	err := (&WorkflowTaskResumeConflictError{TaskID: "task-123"}).Validate()
	if err == nil {
		t.Fatal("Validate succeeded without a lifecycle state")
	}
}
