package registry

import (
	"context"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func subscribeTranscriptForTest(t *testing.T, registry *RuntimeRegistry, sessionID string) serverapi.TranscriptSubscription {
	t.Helper()
	subscription, err := registry.SubscribeSessionTranscript(context.Background(), serverapi.TranscriptSubscribeRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionTranscript: %v", err)
	}
	return subscription
}

func nextTranscriptMessageOfKind(t *testing.T, subscription serverapi.TranscriptSubscription, kind clientui.TranscriptMessageKind) clientui.TranscriptMessage {
	t.Helper()
	for range 8 {
		message := nextTranscriptMessage(t, subscription)
		if message.Kind() == kind {
			return message
		}
	}
	t.Fatalf("did not receive transcript message kind %q", kind)
	return clientui.TranscriptMessage{}
}
