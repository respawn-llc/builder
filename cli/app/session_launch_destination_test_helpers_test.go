package app

import "testing"

func sessionOpenDestinationForTest(t *testing.T, sessionID string) sessionOpenDestination {
	t.Helper()
	destination, err := newSessionOpenDestination(sessionID)
	if err != nil {
		t.Fatalf("new session-open destination: %v", err)
	}
	return destination
}

func sessionParentReferenceForTest(t *testing.T, sessionID string) *sessionParentReference {
	t.Helper()
	validated, err := newValidatedSessionID(sessionID)
	if err != nil {
		t.Fatalf("validate parent session id: %v", err)
	}
	return &sessionParentReference{sessionID: validated}
}
