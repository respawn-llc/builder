package app

import (
	"context"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeinput"
)

func TestRuntimeClientInputMakesOneExplicitCall(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls, nil).(*sessionRuntimeClient)

	if _, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		Input: runtimeinput.Text("hello"),
	}); err != nil {
		t.Fatalf("SubmitRuntimeInput: %v", err)
	}
	if controls.submitCalls != 1 {
		t.Fatalf("submit calls = %d, want 1", controls.submitCalls)
	}
}
