package transport

import (
	"context"
	"errors"
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

func TestLegacyTranscriptSubscriptionRejectsSequenceBelowSuppressedCount(t *testing.T) {
	inner := &scriptedGatewayTranscriptSubscription{
		messages: []clientui.TranscriptMessage{
			{Sequence: 1, Kind: clientui.TranscriptMessageLiveRunFinished},
			{Sequence: 0, Kind: clientui.TranscriptMessageOperationalDiagnostic},
		},
	}
	subscription := &legacyTranscriptSubscription{inner: inner}

	_, err := subscription.Next(context.Background())
	var sequenceErr *legacyTranscriptSequenceError
	if !errors.As(err, &sequenceErr) {
		t.Fatalf("Next error = %T, want legacyTranscriptSequenceError", err)
	}
	if sequenceErr.Sequence != 0 || sequenceErr.Suppressed != 1 {
		t.Fatalf("sequence error = %+v", sequenceErr)
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
