package transport

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestPromptFollowUpSubscriptionInstallsBeforeSubscribeResponse(t *testing.T) {
	stepID, err := runtimeids.ParseStepID("22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatalf("parse Step ID: %v", err)
	}
	route := apicontract.Route{
		EventMethod: protocol.MethodPromptFollowUpEvent, CompleteMethod: protocol.MethodPromptFollowUpComplete,
	}
	conn := &promptFollowUpRegistrationConn{}

	serveGatewaySubscription(
		conn,
		context.Background(),
		route,
		protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "watch",
			Method:  protocol.MethodPromptFollowUpWatch,
			Params: mustJSON(t, serverapi.PromptFollowUpWatchRequest{
				SessionID: runtimeids.NewSessionID(), StepID: stepID, PromptID: "prompt-1",
			}),
		},
		func(context.Context, serverapi.PromptFollowUpWatchRequest) (*scriptedPromptFollowUpSubscription, error) {
			conn.installed = true
			return &scriptedPromptFollowUpSubscription{}, nil
		},
		func(event serverapi.PromptFollowUpEvent) protocol.PromptFollowUpEventParams {
			return protocol.PromptFollowUpEventParams{Event: protocol.PromptFollowUpEvent{Kind: string(event.Kind)}}
		},
	)

	if conn.sent != 2 {
		t.Fatalf("frames = %d, want SubscribeResponse and completion", conn.sent)
	}
}

type scriptedPromptFollowUpSubscription struct{}

func (*scriptedPromptFollowUpSubscription) Next(context.Context) (serverapi.PromptFollowUpEvent, error) {
	return serverapi.PromptFollowUpEvent{}, io.EOF
}

func (*scriptedPromptFollowUpSubscription) Close() error { return nil }

type promptFollowUpRegistrationConn struct {
	installed bool
	sent      int
}

func (c *promptFollowUpRegistrationConn) Send(context.Context, rpcwire.Frame) error {
	if c.sent == 0 && !c.installed {
		return errors.New("SubscribeResponse sent before watcher installation")
	}
	c.sent++
	return nil
}

func (*promptFollowUpRegistrationConn) Events() <-chan rpcwire.Event { return nil }

func (*promptFollowUpRegistrationConn) Closed() <-chan struct{} { return nil }

func (*promptFollowUpRegistrationConn) Close() error { return nil }

func TestLegacyTranscriptSubscriptionSuppressesLiveRunTerminalAndRenumbers(t *testing.T) {
	inner := &scriptedGatewayTranscriptSubscription{
		messages: []clientui.TranscriptMessage{
			clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(clientui.TranscriptHydration{})),
			clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptLiveRunResult{})),
			clientui.NewTranscriptMessage(3, clientui.NewTranscriptEvent(clientui.TranscriptOperationalDiagnostic{})),
		},
	}
	subscription := &legacyTranscriptSubscription{inner: inner}

	first, err := subscription.Next(context.Background())
	if err != nil {
		t.Fatalf("read hydration: %v", err)
	}
	if first.Sequence != 1 || first.Kind() != clientui.TranscriptMessageHydration {
		t.Fatalf("hydration = %+v", first)
	}

	second, err := subscription.Next(context.Background())
	if err != nil {
		t.Fatalf("read diagnostic: %v", err)
	}
	if second.Sequence != 2 || second.Kind() != clientui.TranscriptMessageOperationalDiagnostic {
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
			clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(clientui.TranscriptLiveRunResult{})),
			clientui.NewTranscriptMessage(0, clientui.NewTranscriptEvent(clientui.TranscriptOperationalDiagnostic{})),
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
