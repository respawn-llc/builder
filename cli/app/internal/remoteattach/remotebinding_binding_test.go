package remoteattach

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/client"
	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/rpcwire"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
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
			if event.Frame.Kind == rpcwire.FrameBinary {
				handshakeCompleted, err := serveRemoteBindingBinary(ctx, conn, event.Frame, handshaken, options)
				if err != nil {
					return
				}
				handshaken = handshaken || handshakeCompleted
				continue
			}
			request, err := event.Frame.DecodeRequest()
			if err != nil {
				return
			}
			var response protocol.Response
			switch {
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

func serveRemoteBindingBinary(
	ctx context.Context,
	conn rpcwire.Conn,
	frame rpcwire.Frame,
	handshaken bool,
	options remoteBindingServerOptions,
) (bool, error) {
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil {
		return false, err
	}
	call := envelope.GetCall()
	if call == nil || call.Correlation == nil {
		return false, errors.New("correlated binary call is required")
	}
	handshakeMethod := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").Methods().ByName("Handshake")
	handshakeOperation, err := protoapi.OperationFromDescriptor(handshakeMethod)
	if err != nil {
		return false, err
	}
	if call.Operation == handshakeOperation.Name {
		if handshaken {
			return false, errors.New("Handshake may only be called once")
		}
		var request connectionpb.HandshakeRequest
		if err := protoapi.Decode(call.Payload, &request); err != nil {
			return false, err
		}
		identity := &connectionpb.ServerIdentity{
			ProtocolVersion: protocol.Version,
			ServerId:        "binding-test",
			Pid:             1,
		}
		if options.rootID != "" {
			identity.PersistenceRootId = &options.rootID
		}
		result := &connectionpb.HandshakeResult{
			Outcome: &connectionpb.HandshakeResult_Success{
				Success: &connectionpb.HandshakeSuccess{Identity: identity},
			},
		}
		return true, sendRemoteBindingBinaryResult(ctx, conn, call, result)
	}
	if !handshaken {
		return false, errors.New("Handshake must be the first binary operation")
	}

	readinessMethod := serverpb.File_kent_api_server_server_proto.Services().
		ByName("ServerService").Methods().ByName("GetReadiness")
	readinessOperation, err := protoapi.OperationFromDescriptor(readinessMethod)
	if err != nil {
		return false, err
	}
	if call.Operation == readinessOperation.Name {
		result := &serverpb.GetReadinessResult{
			Outcome: &serverpb.GetReadinessResult_Success{Success: &serverpb.GetReadinessSuccess{
				Readiness: &serverpb.Readiness{
					Ready:           true,
					ServerId:        "binding-test",
					ServerVersion:   "test",
					ServerBuild:     "test",
					ProtocolVersion: protocol.Version,
				},
			}},
		}
		return false, sendRemoteBindingBinaryResult(ctx, conn, call, result)
	}

	ackMethod := authpb.File_kent_api_auth_auth_proto.Services().
		ByName("AuthService").Methods().ByName("AcknowledgeNoAuth")
	ackOperation, err := protoapi.OperationFromDescriptor(ackMethod)
	if err != nil {
		return false, err
	}
	if call.Operation == ackOperation.Name {
		if options.ackStarted != nil {
			close(options.ackStarted)
		}
		if options.releaseAck != nil {
			<-options.releaseAck
		}
		var result *authpb.AcknowledgeNoAuthResult
		if options.ackErr != nil {
			cause := options.ackErr.Error()
			result = &authpb.AcknowledgeNoAuthResult{
				Outcome: &authpb.AcknowledgeNoAuthResult_Error{Error: &authpb.AcknowledgeNoAuthError{
					Code: "internal_failure",
					Detail: &authpb.AcknowledgeNoAuthError_InternalFailure{
						InternalFailure: &sharedpb.InternalFailureDetails{Cause: &cause},
					},
				}},
			}
		} else {
			result = &authpb.AcknowledgeNoAuthResult{
				Outcome: &authpb.AcknowledgeNoAuthResult_Success{
					Success: &authpb.NoAuthAcknowledgement{NoAuthSelected: true},
				},
			}
		}
		return false, sendRemoteBindingBinaryResult(ctx, conn, call, result)
	}
	return false, errors.New("unexpected binary test operation")
}

func sendRemoteBindingBinaryResult(ctx context.Context, conn rpcwire.Conn, call *sharedpb.Call, result proto.Message) error {
	payload, err := protoapi.Encode(result)
	if err != nil {
		return err
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation: call.Operation, Correlation: call.Correlation, Payload: payload,
		}},
	})
	if err != nil {
		return err
	}
	return conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded})
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
	response, err := remote.GetReadiness(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("remote is not usable: %v", err)
	}
	if !response.GetReadiness().GetReady() {
		t.Fatal("remote readiness = false")
	}
}
