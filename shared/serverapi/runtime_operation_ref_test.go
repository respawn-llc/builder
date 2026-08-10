package serverapi

import (
	"reflect"
	"testing"

	"core/shared/runtimeinput"
)

func TestRuntimeRequestsUseOnlyCommandSpecificInputs(t *testing.T) {
	requests := []interface{ Validate() error }{
		RuntimeSubmitUserTurnRequest{SessionID: "session-1", Input: runtimeinput.Text("hello")},
		RuntimeSubmitUserShellCommandRequest{SessionID: "session-1", Command: "pwd"},
		RuntimeCompactContextRequest{SessionID: "session-1", Args: "notes"},
		RuntimeInterruptRequest{SessionID: "session-1"},
	}
	for _, request := range requests {
		if err := request.Validate(); err != nil {
			t.Fatalf("%T rejected: %v", request, err)
		}
		typ := reflect.TypeOf(request)
		for _, field := range []string{"ClientRequestID", "OperationRef", "PendingOperationRefs", "TargetOperationRef"} {
			if _, present := typ.FieldByName(field); present {
				t.Fatalf("%T still exposes %s", request, field)
			}
		}
	}
}
