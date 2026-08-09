package remoteattach

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/client"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
)

func TestBindProjectWorkspaceFailuresKeepCurrentUsableAndCloseCreatedSuccessor(t *testing.T) {
	tests := []struct {
		name       string
		rootID     string
		dialErr    error
		next       remoteBindingServerOptions
		enableAuth bool
		wantRoot   bool
	}{
		{name: "dial", dialErr: errors.New("dial failed")},
		{name: "root pin", rootID: "required-root", next: remoteBindingServerOptions{rootID: "other-root"}, wantRoot: true},
		{name: "no-auth acknowledgement", rootID: "next-root", next: remoteBindingServerOptions{rootID: "next-root", ackErr: errors.New("ack failed")}, enableAuth: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			currentServer := newRemoteBindingServer(t, remoteBindingServerOptions{rootID: "current-root"})
			current := currentServer.dial(t)
			t.Cleanup(func() { _ = current.Close() })
			if test.enableAuth {
				if err := current.EnableNoAuthBootstrapAcknowledgement(context.Background()); err != nil {
					t.Fatalf("enable no-auth acknowledgement: %v", err)
				}
			}

			var nextServer *remoteBindingServer
			var next *client.Remote
			if test.dialErr == nil {
				nextServer = newRemoteBindingServer(t, test.next)
				next = nextServer.dial(t)
			}
			_, err := bindProjectWorkspace(context.Background(), current, "project-1", "", test.rootID, func(context.Context, string, string) (*client.Remote, error) {
				return next, test.dialErr
			})
			if test.wantRoot {
				if !errors.Is(err, client.ErrServerRootMismatch) {
					t.Fatalf("error = %v, want ErrServerRootMismatch", err)
				}
			} else if err == nil {
				t.Fatal("expected binding failure")
			}
			requireRemoteUsable(t, current)
			if nextServer != nil {
				nextServer.requireClosed(t)
			}
		})
	}
}

func TestBindProjectWorkspaceAcknowledgesBeforeSwitchAndFinalCloseIsIdempotent(t *testing.T) {
	currentServer := newRemoteBindingServer(t, remoteBindingServerOptions{rootID: "current-root"})
	current := currentServer.dial(t)
	if err := current.EnableNoAuthBootstrapAcknowledgement(context.Background()); err != nil {
		t.Fatalf("enable no-auth acknowledgement: %v", err)
	}

	ackStarted := make(chan struct{})
	releaseAck := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case releaseAck <- struct{}{}:
		default:
		}
	})
	nextServer := newRemoteBindingServer(t, remoteBindingServerOptions{
		rootID: "next-root", ackStarted: ackStarted, releaseAck: releaseAck,
	})
	next := nextServer.dial(t)

	type result struct {
		remote *client.Remote
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		remote, err := bindProjectWorkspace(context.Background(), current, "project-1", "", "next-root", func(context.Context, string, string) (*client.Remote, error) {
			return next, nil
		})
		resultCh <- result{remote: remote, err: err}
	}()

	select {
	case <-ackStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("successor acknowledgement did not start")
	}
	requireRemoteUsable(t, current)
	releaseAck <- struct{}{}

	var bound result
	select {
	case bound = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("binding did not finish")
	}
	if bound.err != nil {
		t.Fatalf("bind project workspace: %v", bound.err)
	}
	if bound.remote != next {
		t.Fatal("binding did not return the acknowledged successor")
	}
	currentServer.requireClosed(t)

	if err := errors.Join(bound.remote.Close(), bound.remote.Close()); err != nil {
		t.Fatalf("close final remote twice: %v", err)
	}
	nextServer.requireClosed(t)
}

type remoteBindingServerOptions struct {
	rootID     string
	ackErr     error
	ackStarted chan struct{}
	releaseAck <-chan struct{}
}

type remoteBindingServer struct {
	*httptest.Server
	closed     chan struct{}
	closeCount atomic.Int32
}

func newRemoteBindingServer(t *testing.T, options remoteBindingServerOptions) *remoteBindingServer {
	t.Helper()
	server := &remoteBindingServer{closed: make(chan struct{})}
	server.Server = httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		defer func() {
			server.closeCount.Add(1)
			close(server.closed)
		}()
		handshaken := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			request := event.Frame.Request()
			var response protocol.Response
			switch {
			case !handshaken && request.Method == protocol.MethodHandshake:
				handshaken = true
				response = protocol.NewSuccessResponse(request.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{
					ProtocolVersion: protocol.Version, ServerID: "binding-test", PersistenceRootID: options.rootID,
				}})
			case handshaken && request.Method == protocol.MethodServerReadinessGet:
				response = protocol.NewSuccessResponse(request.ID, serverapi.ServerReadinessResponse{Ready: true})
			case handshaken && request.Method == protocol.MethodAuthAcknowledgeNoAuth:
				if options.ackStarted != nil {
					close(options.ackStarted)
				}
				if options.releaseAck != nil {
					<-options.releaseAck
				}
				if options.ackErr != nil {
					response = protocol.NewErrorResponse(request.ID, protocol.ErrCodeInternalError, options.ackErr.Error())
				} else {
					response = protocol.NewSuccessResponse(request.ID, serverapi.AuthAcknowledgeNoAuthResponse{NoAuthSelected: true})
				}
			default:
				response = protocol.NewErrorResponse(request.ID, protocol.ErrCodeMethodNotFound, "unexpected test method")
			}
			if err := conn.Send(ctx, rpcwire.FrameFromResponse(response)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Server.Close)
	return server
}

func (s *remoteBindingServer) dial(t *testing.T) *client.Remote {
	t.Helper()
	remote, err := client.DialRemoteURL(context.Background(), "ws"+s.URL[len("http"):])
	if err != nil {
		t.Fatalf("dial remote: %v", err)
	}
	return remote
}

func (s *remoteBindingServer) requireClosed(t *testing.T) {
	t.Helper()
	select {
	case <-s.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("underlying connection did not close")
	}
	if count := s.closeCount.Load(); count != 1 {
		t.Fatalf("underlying connection close count = %d, want 1", count)
	}
}

func requireRemoteUsable(t *testing.T, remote *client.Remote) {
	t.Helper()
	response, err := remote.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("remote is not usable: %v", err)
	}
	if !response.Ready {
		t.Fatal("remote readiness = false")
	}
}
