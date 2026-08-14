package serverapi

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/runtimeids"
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
	var request RuntimeGoalShowRequest
	if err := json.Unmarshal([]byte(`{"session_id":"../session"}`), &request); !errors.Is(err, ErrSessionIDNotSingle) {
		t.Fatalf("decode error = %v, want ErrSessionIDNotSingle", err)
	}
}

func TestRuntimeGoalShowCanonicalizesAcceptedWhitespaceOnce(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	var request RuntimeGoalShowRequest
	if err := json.Unmarshal([]byte(`{"session_id":"  `+sessionID.String()+`  "}`), &request); err != nil {
		t.Fatalf("decode padded Session ID: %v", err)
	}
	if request.SessionID != sessionID {
		t.Fatalf("decoded Session ID = %q, want %q", request.SessionID.String(), sessionID.String())
	}
}

func TestRuntimeControlRequestsRejectPathLikeSessionID(t *testing.T) {
	if err := validateRuntimeControlRequest("request-1", "../session"); !errors.Is(err, ErrSessionIDNotSingle) {
		t.Fatalf("validation error = %v, want ErrSessionIDNotSingle", err)
	}
}
