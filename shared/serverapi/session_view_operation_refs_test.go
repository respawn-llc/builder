package serverapi

import (
	"errors"
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

func TestSessionTranscriptPageRequestRejectsAmbiguousDirection(t *testing.T) {
	cursor := int64(10)
	newerCursor := int64(20)
	req := SessionTranscriptPageRequest{
		SessionID:   "session-1",
		Cursor:      &cursor,
		NewerCursor: &newerCursor,
	}
	if err := req.Validate(); !errors.Is(err, ErrTranscriptCursorDirectionAmbiguous) {
		t.Fatalf("Validate error = %v, want ambiguous cursor direction", err)
	}
}

func TestSessionTranscriptPageRequestRejectsZeroCursor(t *testing.T) {
	cursor := int64(0)
	req := SessionTranscriptPageRequest{SessionID: "session-1", Cursor: &cursor}
	if err := req.Validate(); !errors.Is(err, ErrTranscriptCursorInvalid) {
		t.Fatalf("Validate error = %v, want invalid cursor", err)
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
