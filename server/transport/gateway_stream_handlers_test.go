package transport

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/apicontract"
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
