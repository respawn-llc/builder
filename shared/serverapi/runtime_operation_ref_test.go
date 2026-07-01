package serverapi

import (
	"testing"

	"core/shared/clientui"
)

func TestInputBearingRuntimeRequestsRequireMatchingOperationRefs(t *testing.T) {
	for name, err := range map[string]error{
		"submit": (RuntimeSubmitUserTurnRequest{
			ClientRequestID:                 "submit-1",
			SessionID:                       "session-1",
			Text:                            "hello",
			OperationRef:                    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"},
			PreSubmitCompactionOperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindPreSubmitCompact, ClientRequestID: "pre-compact-1"},
		}).Validate(),
		"shell": (RuntimeSubmitUserShellCommandRequest{
			ClientRequestID: "shell-1",
			SessionID:       "session-1",
			Command:         "pwd",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: "shell-1"},
		}).Validate(),
		"compact": (RuntimeCompactContextRequest{
			ClientRequestID: "compact-1",
			SessionID:       "session-1",
			Args:            "notes",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindCompact, ClientRequestID: "compact-1"},
		}).Validate(),
		"pre-submit compact": (RuntimeCompactContextForPreSubmitRequest{
			ClientRequestID: "pre-compact-1",
			SessionID:       "session-1",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindPreSubmitCompact, ClientRequestID: "pre-compact-1"},
		}).Validate(),
		"submit queued": (RuntimeSubmitQueuedUserMessagesRequest{
			ClientRequestID: "submit-queued-1",
			SessionID:       "session-1",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmitQueued, ClientRequestID: "submit-queued-1"},
		}).Validate(),
		"queue user message": (RuntimeQueueUserMessageRequest{
			ClientRequestID: "queue-create-1",
			SessionID:       "session-1",
			Text:            "queued",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "queue-create-1"},
		}).Validate(),
	} {
		if err != nil {
			t.Fatalf("%s request rejected: %v", name, err)
		}
	}
}

func TestInputBearingRuntimeRequestsRejectHiddenOrMismatchedOperationRefs(t *testing.T) {
	tests := []struct {
		name string
		req  interface{ Validate() error }
	}{
		{
			name: "missing ref",
			req: RuntimeSubmitUserTurnRequest{
				ClientRequestID: "submit-1",
				SessionID:       "session-1",
				Text:            "hello",
			},
		},
		{
			name: "wrong kind",
			req: RuntimeSubmitUserTurnRequest{
				ClientRequestID: "submit-1",
				SessionID:       "session-1",
				Text:            "hello",
				OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: "submit-1"},
			},
		},
		{
			name: "request id mismatch",
			req: RuntimeSubmitUserShellCommandRequest{
				ClientRequestID: "shell-1",
				SessionID:       "session-1",
				Command:         "pwd",
				OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: "other"},
			},
		},
		{
			name: "queue create with server queue id",
			req: RuntimeQueueUserMessageRequest{
				ClientRequestID: "queue-create-1",
				SessionID:       "session-1",
				Text:            "queued",
				OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, QueueItemID: "queue-1"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected request to be rejected")
			}
		})
	}
}

func TestRuntimeInterruptRequestValidatesOptionalTargetOperationRef(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	if err := (RuntimeInterruptRequest{
		ClientRequestID:      "interrupt-1",
		SessionID:            "session-1",
		TargetOperationRef:   &ref,
		PendingOperationRefs: []clientui.RuntimeOperationRef{ref},
	}).Validate(); err != nil {
		t.Fatalf("targeted interrupt rejected: %v", err)
	}
	bad := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage}
	if err := (RuntimeInterruptRequest{
		ClientRequestID:    "interrupt-1",
		SessionID:          "session-1",
		TargetOperationRef: &bad,
	}).Validate(); err == nil {
		t.Fatal("expected malformed target ref to be rejected")
	}
}
