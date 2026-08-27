package app

import (
	"context"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
)

func TestRuntimeClientInputRequestUsesCallerRequestIdentity(t *testing.T) {
	controls := &reconnectRetryRuntimeControlClient{}
	runtimeClient := newUIRuntimeClientWithReads("session-1", &countingSessionViewClient{}, controls, unavailableChatSettingsService{}).(*sessionRuntimeClient)
	requestID := runtimeids.NewRuntimeClientRequestID()

	if _, err := runtimeClient.SubmitRuntimeInput(context.Background(), clientui.RuntimeSubmitRequest{
		ClientRequestID: requestID,
		Input:           runtimeinput.Text("hello"),
	}); err != nil {
		t.Fatalf("SubmitRuntimeInput: %v", err)
	}
	if got := controls.submitRequestIDs(); len(got) != 1 || got[0] != requestID.String() {
		t.Fatalf("request ids = %+v, want %q", got, requestID.String())
	}
}
