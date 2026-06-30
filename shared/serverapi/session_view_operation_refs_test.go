package serverapi

import (
	"testing"

	"core/shared/clientui"
)

func TestSessionMainViewRequestCarriesPendingOperationRefs(t *testing.T) {
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-1"}
	req := SessionMainViewRequest{SessionID: "session-1", PendingOperationRefs: []clientui.RuntimeOperationRef{ref}}

	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(req.PendingOperationRefs) != 1 || req.PendingOperationRefs[0] != ref {
		t.Fatalf("pending refs = %+v, want %+v", req.PendingOperationRefs, ref)
	}
}

func TestSessionMainViewRequestRejectsInvalidPendingOperationRefs(t *testing.T) {
	req := SessionMainViewRequest{
		SessionID: "session-1",
		PendingOperationRefs: []clientui.RuntimeOperationRef{
			{Kind: clientui.RuntimeOperationKindSubmit},
		},
	}
	if err := req.Validate(); err == nil {
		t.Fatal("expected invalid pending ref to be rejected")
	}
}
