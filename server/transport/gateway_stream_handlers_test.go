package transport

import (
	"context"
	"io"
	"testing"

	"core/shared/clientui"
)

func TestLegacyTranscriptSubscriptionSuppressesLiveRunTerminalAndRenumbers(t *testing.T) {
	inner := &scriptedGatewayTranscriptSubscription{
		messages: []clientui.TranscriptMessage{
			{Sequence: 1, Kind: clientui.TranscriptMessageHydration},
			{Sequence: 2, Kind: clientui.TranscriptMessageLiveRunFinished},
			{Sequence: 3, Kind: clientui.TranscriptMessageOperationalDiagnostic},
		},
	}
	subscription := &legacyTranscriptSubscription{inner: inner}

	first, err := subscription.Next(context.Background())
	if err != nil {
		t.Fatalf("read hydration: %v", err)
	}
	if first.Sequence != 1 || first.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("hydration = %+v", first)
	}

	second, err := subscription.Next(context.Background())
	if err != nil {
		t.Fatalf("read diagnostic: %v", err)
	}
	if second.Sequence != 2 || second.Kind != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("renumbered diagnostic = %+v", second)
	}
	if err := subscription.Close(); err != nil {
		t.Fatalf("close subscription: %v", err)
	}
	if !inner.closed {
		t.Fatal("legacy wrapper did not close the underlying subscription")
	}
}

type scriptedGatewayTranscriptSubscription struct {
	messages []clientui.TranscriptMessage
	closed   bool
}

func (s *scriptedGatewayTranscriptSubscription) Next(context.Context) (clientui.TranscriptMessage, error) {
	if len(s.messages) == 0 {
		return clientui.TranscriptMessage{}, io.EOF
	}
	message := s.messages[0]
	s.messages = s.messages[1:]
	return message, nil
}

func (s *scriptedGatewayTranscriptSubscription) Close() error {
	s.closed = true
	return nil
}
