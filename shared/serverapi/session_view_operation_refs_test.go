package serverapi

import (
	"errors"
	"testing"

	"core/shared/clientui"
)

func TestSessionMainViewRequestUsesSessionIdentityOnly(t *testing.T) {
	req := SessionMainViewRequest{SessionID: "session-1"}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	availability := clientui.GoalAvailabilityAvailable
	if missing := (SessionMainViewResponse{MainView: clientui.RuntimeMainView{Status: clientui.RuntimeStatus{Goal: &clientui.RuntimeGoal{}}}}).Validate(); missing == nil || (SessionMainViewResponse{MainView: clientui.RuntimeMainView{Status: clientui.RuntimeStatus{Goal: &clientui.RuntimeGoal{Goal: &clientui.Goal{}, Availability: &availability}}}}).Validate() == nil {
		t.Fatal("accepted malformed main-view Goal availability or core")
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

func TestSessionLatestCommittedAssistantFinalAnswerRequestRequiresSessionID(t *testing.T) {
	if err := (SessionLatestCommittedAssistantFinalAnswerRequest{}).Validate(); !errors.Is(err, ErrSessionIDRequired) {
		t.Fatalf("Validate error = %v, want ErrSessionIDRequired", err)
	}
	if err := (SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: "session-1"}).Validate(); err != nil {
		t.Fatalf("Validate valid request: %v", err)
	}
}
