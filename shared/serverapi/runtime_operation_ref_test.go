package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

func TestRuntimeInputRequestsHaveNoOperationRefs(t *testing.T) {
	requests := []struct {
		name      string
		request   interface{ Validate() error }
		forbidden []string
	}{
		{
			name: "submit",
			request: RuntimeSubmitUserTurnRequest{
				SessionID: "session-1",
				Input:     runtimeinput.Text("hello"),
			},
			forbidden: []string{"operation_ref", "pre_submit_compaction_operation_ref"},
		},
		{
			name: "shell",
			request: RuntimeSubmitUserShellCommandRequest{
				SessionID: "session-1",
				Command:   "pwd",
			},
			forbidden: []string{"operation_ref"},
		},
		{
			name: "compact",
			request: RuntimeCompactContextRequest{
				SessionID: "session-1",
				RequestID: runtimeids.NewCompactionRequestID(),
				Admission: ManualCompactionAdmission{RestorationInput: "/compact"},
			},
			forbidden: []string{"operation_ref"},
		},
		{
			name: "interrupt",
			request: RuntimeInterruptRequest{
				SessionID: "session-1",
			},
			forbidden: []string{"target_operation_ref", "pending_operation_refs"},
		},
	}
	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.request.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			wire, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(wire, &fields); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			for _, field := range tt.forbidden {
				if _, exists := fields[field]; exists {
					t.Fatalf("wire request unexpectedly contains %q: %s", field, wire)
				}
			}
		})
	}
}

func TestRuntimeCompactContextRequiresRequestIdentity(t *testing.T) {
	err := (RuntimeCompactContextRequest{SessionID: "session-1"}).Validate()
	if err == nil {
		t.Fatal("RuntimeCompactContextRequest accepted a missing request identity")
	}
}
