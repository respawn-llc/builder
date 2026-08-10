package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"core/shared/protocol"
	"core/shared/rpcwire"
)

func TestInitialBranchClientStopsAtOldServerHandshake(t *testing.T) {
	handlerErrs := make(chan error, 1)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			if req.Method != protocol.MethodHandshake {
				handlerErrs <- errors.New("client sent a request before protocol handshake succeeded")
				return
			}
			var handshake protocol.HandshakeRequest
			if err := json.Unmarshal(req.Params, &handshake); err != nil {
				handlerErrs <- err
				return
			}
			if handshake.ProtocolVersion != protocol.Version {
				handlerErrs <- errors.New("client handshake did not use the current protocol version")
				return
			}
			_ = conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewErrorResponse(
				req.ID,
				protocol.ErrCodeProtocolVersionMismatch,
				"old server rejects newer client protocol",
			)))
			return
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if remote != nil {
		_ = remote.Close()
		t.Fatal("old server handshake returned a usable remote client")
	}
	if err == nil {
		t.Fatal("old server handshake unexpectedly succeeded")
	}
	select {
	case handlerErr := <-handlerErrs:
		t.Fatal(handlerErr)
	case <-time.After(100 * time.Millisecond):
	}
}
