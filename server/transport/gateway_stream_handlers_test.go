package transport

import (
	"context"
	"encoding/json"
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
	request := serverapi.PromptFollowUpWatchRequest{
		SessionID: runtimeids.NewSessionID(),
		StepID:    stepID,
		PromptID:  "prompt-1",
	}
	params, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	route, ok := apicontract.RouteByMethod(protocol.MethodPromptFollowUpWatch)
	if !ok {
		t.Fatal("prompt follow-up route missing")
	}
	installed := false
	conn := newPromptFollowUpRegistrationConn(&installed)

	serveGatewaySubscription(
		conn,
		context.Background(),
		route,
		protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "watch",
			Method:  protocol.MethodPromptFollowUpWatch,
			Params:  params,
		},
		func(context.Context, serverapi.PromptFollowUpWatchRequest) (*scriptedPromptFollowUpSubscription, error) {
			installed = true
			return &scriptedPromptFollowUpSubscription{}, nil
		},
		func(event serverapi.PromptFollowUpEvent) protocol.PromptFollowUpEventParams {
			return protocol.PromptFollowUpEventParams{Event: protocol.PromptFollowUpEvent{Kind: string(event.Kind)}}
		},
	)

	if len(conn.frames) != 3 {
		t.Fatalf("frames = %d, want SubscribeResponse, one terminal event, and completion", len(conn.frames))
	}
	if conn.frames[0].ID != "watch" || conn.frames[0].Method != "" {
		t.Fatalf("first frame = %+v, want canonical SubscribeResponse", conn.frames[0])
	}
	if conn.frames[1].Method != protocol.MethodPromptFollowUpEvent ||
		conn.frames[2].Method != protocol.MethodPromptFollowUpComplete {
		t.Fatalf("stream frames = %+v", conn.frames)
	}
}

type scriptedPromptFollowUpSubscription struct {
	delivered bool
}

func (s *scriptedPromptFollowUpSubscription) Next(context.Context) (serverapi.PromptFollowUpEvent, error) {
	if s.delivered {
		return serverapi.PromptFollowUpEvent{}, io.EOF
	}
	s.delivered = true
	return serverapi.PromptFollowUpEvent{Kind: serverapi.PromptFollowUpSuccessorReady}, nil
}

func (*scriptedPromptFollowUpSubscription) Close() error {
	return nil
}

type promptFollowUpRegistrationConn struct {
	installed *bool
	frames    []rpcwire.Frame
	events    chan rpcwire.Event
	closed    chan struct{}
}

func newPromptFollowUpRegistrationConn(installed *bool) *promptFollowUpRegistrationConn {
	return &promptFollowUpRegistrationConn{
		installed: installed,
		events:    make(chan rpcwire.Event),
		closed:    make(chan struct{}),
	}
}

func (c *promptFollowUpRegistrationConn) Send(_ context.Context, frame rpcwire.Frame) error {
	if len(c.frames) == 0 && !*c.installed {
		return errors.New("SubscribeResponse sent before watcher installation")
	}
	c.frames = append(c.frames, frame)
	return nil
}

func (c *promptFollowUpRegistrationConn) Events() <-chan rpcwire.Event {
	return c.events
}

func (c *promptFollowUpRegistrationConn) Closed() <-chan struct{} {
	return c.closed
}

func (c *promptFollowUpRegistrationConn) Close() error {
	return nil
}

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
