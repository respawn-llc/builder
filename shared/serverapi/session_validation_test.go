package serverapi

import (
	"errors"
	"testing"
)

func TestValidateRequiredSessionID(t *testing.T) {
	if err := validateRequiredSessionID("session-1"); err != nil {
		t.Fatalf("expected non-empty session id to validate, got %v", err)
	}
	if err := validateRequiredSessionID(" \t "); !errors.Is(err, ErrSessionIDRequired) {
		t.Fatalf("expected ErrSessionIDRequired, got %v", err)
	}
}

func TestValidateScopedSessionID(t *testing.T) {
	valid := []string{"session-1", "session_2", "session.3"}
	for _, sessionID := range valid {
		if err := validateScopedSessionID(sessionID); err != nil {
			t.Fatalf("expected %q to validate, got %v", sessionID, err)
		}
	}

	invalid := []string{"", " ", ".", "..", "/tmp/session", "nested/session", `nested\\session`, "session/../other"}
	for _, sessionID := range invalid {
		if err := validateScopedSessionID(sessionID); err == nil {
			t.Fatalf("expected %q to fail validation", sessionID)
		}
	}
}

func TestRuntimeGoalShowRejectsPathLikeSessionID(t *testing.T) {
	if err := (RuntimeGoalShowRequest{SessionID: "../session"}).Validate(); !errors.Is(err, ErrSessionIDNotSingle) {
		t.Fatalf("Validate error = %v, want ErrSessionIDNotSingle", err)
	}
}

func TestRuntimeControlRequestsRejectPathLikeSessionID(t *testing.T) {
	if err := validateRuntimeControlRequest("request-1", "../session"); !errors.Is(err, ErrSessionIDNotSingle) {
		t.Fatalf("validation error = %v, want ErrSessionIDNotSingle", err)
	}
}
