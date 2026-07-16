package serverapi

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

func TestInputBearingRuntimeRequestsRequireMatchingOperationRefs(t *testing.T) {
	submitID := runtimeids.NewRuntimeClientRequestID()
	preCompactID := runtimeids.NewRuntimeClientRequestID()
	shellID := runtimeids.NewRuntimeClientRequestID()
	compactID := runtimeids.NewRuntimeClientRequestID()
	submitQueuedID := runtimeids.NewRuntimeClientRequestID()
	queueCreateID := runtimeids.NewRuntimeClientRequestID()
	for name, err := range map[string]error{
		"submit": (RuntimeSubmitUserTurnRequest{
			ClientRequestID:                 submitID.String(),
			SessionID:                       "session-1",
			Text:                            "hello",
			OperationRef:                    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: submitID},
			PreSubmitCompactionOperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindPreSubmitCompact, ClientRequestID: preCompactID},
		}).Validate(),
		"shell": (RuntimeSubmitUserShellCommandRequest{
			ClientRequestID: shellID.String(),
			SessionID:       "session-1",
			Command:         "pwd",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: shellID},
		}).Validate(),
		"compact": (RuntimeCompactContextRequest{
			ClientRequestID: compactID.String(),
			SessionID:       "session-1",
			Args:            "notes",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindCompact, ClientRequestID: compactID},
		}).Validate(),
		"pre-submit compact": (RuntimeCompactContextForPreSubmitRequest{
			ClientRequestID: preCompactID.String(),
			SessionID:       "session-1",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindPreSubmitCompact, ClientRequestID: preCompactID},
		}).Validate(),
		"submit queued": (RuntimeSubmitQueuedUserMessagesRequest{
			ClientRequestID: submitQueuedID.String(),
			SessionID:       "session-1",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmitQueued, ClientRequestID: submitQueuedID},
		}).Validate(),
		"queue user message": (RuntimeQueueUserMessageRequest{
			ClientRequestID: queueCreateID.String(),
			SessionID:       "session-1",
			Text:            "queued",
			OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: queueCreateID},
		}).Validate(),
	} {
		if err != nil {
			t.Fatalf("%s request rejected: %v", name, err)
		}
	}
}

func TestInputBearingRuntimeRequestsRejectHiddenOrMismatchedOperationRefs(t *testing.T) {
	submitID := runtimeids.NewRuntimeClientRequestID()
	shellID := runtimeids.NewRuntimeClientRequestID()
	otherID := runtimeids.NewRuntimeClientRequestID()
	queueCreateID := runtimeids.NewRuntimeClientRequestID()
	queueItemID := runtimeids.NewQueueItemID()
	tests := []struct {
		name string
		req  interface{ Validate() error }
	}{
		{
			name: "missing ref",
			req: RuntimeSubmitUserTurnRequest{
				ClientRequestID: submitID.String(),
				SessionID:       "session-1",
				Text:            "hello",
			},
		},
		{
			name: "wrong kind",
			req: RuntimeSubmitUserTurnRequest{
				ClientRequestID: submitID.String(),
				SessionID:       "session-1",
				Text:            "hello",
				OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: submitID},
			},
		},
		{
			name: "request id mismatch",
			req: RuntimeSubmitUserShellCommandRequest{
				ClientRequestID: shellID.String(),
				SessionID:       "session-1",
				Command:         "pwd",
				OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: otherID},
			},
		},
		{
			name: "queue create with server queue id",
			req: RuntimeQueueUserMessageRequest{
				ClientRequestID: queueCreateID.String(),
				SessionID:       "session-1",
				Text:            "queued",
				OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: queueCreateID, QueueItemID: &queueItemID},
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
	submitID := runtimeids.NewRuntimeClientRequestID()
	interruptID := runtimeids.NewRuntimeClientRequestID()
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: submitID}
	if err := (RuntimeInterruptRequest{
		ClientRequestID:      interruptID.String(),
		SessionID:            "session-1",
		TargetOperationRef:   &ref,
		PendingOperationRefs: []clientui.RuntimeOperationRef{ref},
	}).Validate(); err != nil {
		t.Fatalf("targeted interrupt rejected: %v", err)
	}
	bad := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage}
	if err := (RuntimeInterruptRequest{
		ClientRequestID:    interruptID.String(),
		SessionID:          "session-1",
		TargetOperationRef: &bad,
	}).Validate(); err == nil {
		t.Fatal("expected malformed target ref to be rejected")
	}
}
