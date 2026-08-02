package registry

import (
	"testing"

	"core/shared/clientui"
)

func transcriptPayload[T any](t *testing.T, message clientui.TranscriptMessage) T {
	t.Helper()
	payload, ok := message.Payload().(T)
	if !ok {
		t.Fatalf("transcript payload type = %T, want %T", message.Payload(), *new(T))
	}
	return payload
}
