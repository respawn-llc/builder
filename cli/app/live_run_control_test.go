package app

import (
	"testing"
	"time"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestRuntimeLiveWaitResultKeepsTargetSessionWhenResponseIsEmpty(t *testing.T) {
	targetSessionID, err := runtimeids.ParseSessionID("018fdd67-89ab-4cde-8123-456789abcdef")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}

	result := runtimeLiveWaitResult(targetSessionID, serverapi.RuntimeLiveWaitResponse{})

	if result.SessionID != targetSessionID.String() {
		t.Fatalf("SessionID = %q, want target session", result.SessionID)
	}
}

func TestRuntimeLiveWaitResultUsesSuccessfulResponseFields(t *testing.T) {
	targetSessionID, err := runtimeids.ParseSessionID("018fdd67-89ab-4cde-8123-456789abcdef")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	resultText := "done"

	result := runtimeLiveWaitResult(targetSessionID, serverapi.RuntimeLiveWaitResponse{
		SessionID:      "018fdd67-89ab-4cde-8123-456789abcdea",
		SessionName:    "live session",
		Result:         &resultText,
		DurationMillis: 2500,
	})

	if result.SessionID != "018fdd67-89ab-4cde-8123-456789abcdea" || result.SessionName != "live session" || result.Result != resultText || result.Duration != 2500*time.Millisecond {
		t.Fatalf("unexpected live wait result: %+v", result)
	}
}

func TestLiveSteerCallerSessionIDParsesOptionalEnvironmentContext(t *testing.T) {
	t.Setenv("KENT_SESSION_ID", "018fdd67-89ab-4cde-8123-456789abcdef")
	callerID, err := liveSteerCallerSessionID()
	if err != nil {
		t.Fatalf("liveSteerCallerSessionID: %v", err)
	}
	if callerID == nil || *callerID != "018fdd67-89ab-4cde-8123-456789abcdef" {
		t.Fatalf("caller ID = %v, want configured Session ID", callerID)
	}
}

func TestLiveSteerCallerSessionIDRejectsMalformedPresentContext(t *testing.T) {
	t.Setenv("KENT_SESSION_ID", "/invalid/session")
	if _, err := liveSteerCallerSessionID(); err == nil {
		t.Fatal("liveSteerCallerSessionID unexpectedly accepted malformed context")
	}
}

func TestLiveSteerCallerSessionIDOmittedForBlankContext(t *testing.T) {
	t.Setenv("KENT_SESSION_ID", " \t ")
	callerID, err := liveSteerCallerSessionID()
	if err != nil {
		t.Fatalf("liveSteerCallerSessionID: %v", err)
	}
	if callerID != nil {
		t.Fatalf("caller ID = %v, want nil", callerID)
	}
}
