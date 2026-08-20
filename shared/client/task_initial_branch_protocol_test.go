package client

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"core/shared/rpcwire"
)

func TestInitialBranchClientStopsAtOldServerHandshake(t *testing.T) {
	handlerErrs := make(chan error, 1)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			if err := rejectRemoteTestHandshake(ctx, conn, event.Frame, "125"); err != nil {
				handlerErrs <- err
			}
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
