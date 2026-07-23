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
