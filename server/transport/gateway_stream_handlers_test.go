package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

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
	conn := &promptFollowUpRegistrationConn{}
	serveGatewaySubscription(
		conn,
		context.Background(),
		apicontract.Route{EventMethod: protocol.MethodPromptFollowUpEvent, CompleteMethod: protocol.MethodPromptFollowUpComplete},
		protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "watch",
			Method:  protocol.MethodPromptFollowUpWatch,
			Params: mustJSON(t, serverapi.PromptFollowUpWatchRequest{
				SessionID: runtimeids.NewSessionID(), StepID: stepID, PromptID: "prompt-1",
			}),
		},
		func(context.Context, serverapi.PromptFollowUpWatchRequest) (*promptFollowUpRegistrationConn, error) {
			conn.installed = true
			return conn, nil
		},
		func(event serverapi.PromptFollowUpEvent) protocol.PromptFollowUpEventParams {
			return protocol.PromptFollowUpEventParams{Event: protocol.PromptFollowUpEvent{Kind: string(event.Kind)}}
		},
	)
	if conn.sent != 2 {
		t.Fatalf("frames = %d, want SubscribeResponse and completion", conn.sent)
	}
}

func (*promptFollowUpRegistrationConn) Next(context.Context) (serverapi.PromptFollowUpEvent, error) {
	return serverapi.PromptFollowUpEvent{}, io.EOF
}

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
func (*promptFollowUpRegistrationConn) Closed() <-chan struct{}      { return nil }
func (*promptFollowUpRegistrationConn) Close() error                 { return nil }
func TestSessionTranscriptSubscriptionPublishesLiveRunFinishedWithoutRewritingSequence(t *testing.T) {
	answer := "done"
	now := time.Unix(1, 0).UTC()
	subscription := &scriptedGatewayTranscriptSubscription{
		messages: []clientui.TranscriptMessage{
			clientui.NewTranscriptMessage(7, clientui.NewTranscriptEvent(clientui.TranscriptLiveRunResult{
				Status:        clientui.LiveRunStatusCompleted,
				ResultKind:    clientui.LiveRunResultAssistantFinalAnswer,
				WorkPerformed: true,
				FinalAnswer:   &answer,
				StartedAt:     now,
				FinishedAt:    now,
			})),
			clientui.NewTranscriptMessage(8, clientui.NewTranscriptEvent(clientui.TranscriptOperationalDiagnostic{
				Code:   clientui.OperationalDiagnosticSleepGuardFailed,
				Detail: "sleep guard failed",
			})),
		},
	}
	conn := &recordingGatewayConn{}
	route, ok := apicontract.RouteByMethod(protocol.MethodSessionSubscribeTranscript)
	if !ok {
		t.Fatal("session transcript route is not registered")
	}
	gateway := &Gateway{deps: &gatewayTranscriptDependencies{
		transcript: gatewayTranscriptServiceFunc(func(context.Context, serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
			return subscription, nil
		}),
	}}
	gateway.serveSessionTranscriptSubscription(
		conn,
		context.Background(),
		&connectionState{},
		route,
		protocol.Request{
			JSONRPC: protocol.JSONRPCVersion,
			ID:      "subscribe",
			Method:  protocol.MethodSessionSubscribeTranscript,
			Params:  mustJSON(t, serverapi.TranscriptSubscribeRequest{SessionID: "session-1"}),
		},
	)

	var messages []clientui.TranscriptMessage
	for _, frame := range conn.frames {
		request := frame.Request()
		if request.Method != protocol.MethodSessionTranscriptEvent {
			continue
		}
		var params protocol.SessionTranscriptEventParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode transcript event: %v", err)
		}
		messages = append(messages, params.Message)
	}
	if len(messages) != 2 {
		t.Fatalf("transcript messages = %+v, want live-run-finished and diagnostic", messages)
	}
	if messages[0].Sequence != 7 || messages[0].Kind() != clientui.TranscriptMessageLiveRunFinished {
		t.Fatalf("first transcript message = %+v, want seq=7 live-run-finished", messages[0])
	}
	if messages[1].Sequence != 8 || messages[1].Kind() != clientui.TranscriptMessageOperationalDiagnostic {
		t.Fatalf("second transcript message = %+v, want seq=8 operational diagnostic", messages[1])
	}
	if !subscription.closed {
		t.Fatal("subscription was not closed")
	}
}

type gatewayTranscriptServiceFunc func(context.Context, serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error)

func (f gatewayTranscriptServiceFunc) SubscribeSessionTranscript(ctx context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
	return f(ctx, req)
}

type gatewayTranscriptDependencies struct {
	GatewayDependencies
	transcript apicontract.SessionTranscriptService
}

func (d *gatewayTranscriptDependencies) SessionTranscriptClient() apicontract.SessionTranscriptService {
	return d.transcript
}

type recordingGatewayConn struct {
	frames []rpcwire.Frame
}

func (c *recordingGatewayConn) Send(_ context.Context, frame rpcwire.Frame) error {
	c.frames = append(c.frames, frame)
	return nil
}

func (*recordingGatewayConn) Events() <-chan rpcwire.Event { return nil }
func (*recordingGatewayConn) Closed() <-chan struct{}      { return nil }
func (*recordingGatewayConn) Close() error                 { return nil }

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
