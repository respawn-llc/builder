package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/config"
	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
	"core/shared/textutil"

	serverpb "core/shared/protoapi/gen/kent/api/server"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestDialConfiguredRemotePrefersLocalUnixSocket(t *testing.T) {
	handlerErrs := make(chan error, 8)
	cfg := config.App{PersistenceRoot: t.TempDir(), Settings: config.Settings{ServerHost: "127.0.0.1", ServerPort: 1}}
	socketPath, ok, err := config.ServerLocalRPCSocketPath(cfg)
	if err != nil {
		t.Fatalf("ServerLocalRPCSocketPath: %v", err)
	}
	if !ok {
		t.Skip("local unix sockets unsupported on this platform")
	}
	shutdown := startUnixWebSocketServer(t, socketPath, func(ctx context.Context, conn rpcwire.Conn) {
		serveProjectListRPC(ctx, conn, handlerErrs)
	})
	defer shutdown()

	remote, err := DialConfiguredRemote(context.Background(), cfg)
	if err != nil {
		t.Fatalf("DialConfiguredRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestRemoteReleaseSessionRuntimePropagatesClosePolicy(t *testing.T) {
	handlerErrs := make(chan error, 8)
	releaseRequests := make(chan serverapi.SessionRuntimeReleaseRequest, 1)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			if _, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{}); handled {
				if err != nil {
					reportHandlerError(handlerErrs, "setup: %v", err)
				}
				continue
			}
			req := event.Frame.Request()
			switch req.Method {
			case protocol.MethodSessionRuntimeRelease:
				var params serverapi.SessionRuntimeReleaseRequest
				if err := json.Unmarshal(req.Params, &params); err != nil {
					reportHandlerError(handlerErrs, "decode release request: %w", err)
					return
				}
				releaseRequests <- params
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.SessionRuntimeReleaseResponse{Released: true}))); err != nil {
					reportHandlerError(handlerErrs, "send release response: %w", err)
					return
				}
				return
			default:
				reportHandlerError(handlerErrs, "unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-1",
		Attachment: serverapi.SessionRuntimeAttachment{
			SessionID:  "session-1",
			Generation: 7,
		},
		DropOwner:   true,
		ClosePolicy: serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
	})
	if err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
	if !resp.Released {
		t.Fatalf("release response = %+v, want released", resp)
	}
	select {
	case req := <-releaseRequests:
		if req.Attachment.SessionID != "session-1" || req.Attachment.Generation != 7 || !req.DropOwner || req.ClosePolicy != serverapi.SessionRuntimeReleaseClosePolicyDetachOnly {
			t.Fatalf("release request = %+v, want exact attachment and detach-only owner drop", req)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for release request")
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestDialConfiguredRemoteFallsBackToTCPWhenLocalUnixSocketMissing(t *testing.T) {
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		serveProjectListRPC(ctx, conn, handlerErrs)
	}))
	defer server.Close()

	cfg := testRemoteConfigFromServerURL(t, t.TempDir(), server.URL)
	socketPath, ok, err := config.ServerLocalRPCSocketPath(cfg)
	if err != nil {
		t.Fatalf("ServerLocalRPCSocketPath: %v", err)
	}
	if ok {
		_ = os.Remove(socketPath)
	}

	remote, err := DialConfiguredRemote(context.Background(), cfg)
	if err != nil {
		t.Fatalf("DialConfiguredRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestDialConfiguredRemoteFallsBackToTCPWhenLocalUnixHandshakeStalls(t *testing.T) {
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		serveProjectListRPC(ctx, conn, handlerErrs)
	}))
	defer server.Close()

	cfg := testRemoteConfigFromServerURL(t, t.TempDir(), server.URL)
	socketPath, ok, err := config.ServerLocalRPCSocketPath(cfg)
	if err != nil {
		t.Fatalf("ServerLocalRPCSocketPath: %v", err)
	}
	if !ok {
		t.Skip("local unix sockets unsupported on this platform")
	}
	stallListener, stallAccepted := startUnixStallingListener(t, socketPath, 5*time.Second)
	defer func() { _ = stallListener.Close() }()
	defer func() { _ = os.Remove(socketPath) }()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	remote, err := DialConfiguredRemote(ctx, cfg)
	if err != nil {
		t.Fatalf("DialConfiguredRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("DialConfiguredRemote elapsed = %v, want < 500ms", elapsed)
	}
	select {
	case <-stallAccepted:
	case <-time.After(time.Second):
		t.Fatal("expected stalled unix listener accept")
	}
	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestDialConfiguredRemoteHonorsExplicitTCPTargetOverDerivedLocalSocket(t *testing.T) {
	tcpHandlerErrs := make(chan error, 8)
	udsHandlerErrs := make(chan error, 8)
	var tcpConnectionCount atomic.Int32
	var udsConnectionCount atomic.Int32

	tcpServer := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		serveProjectListRPCWithProjectID(ctx, conn, "tcp-project", tcpHandlerErrs, &tcpConnectionCount)
	}))
	defer tcpServer.Close()

	cfg := testRemoteConfigFromServerURL(t, t.TempDir(), tcpServer.URL)
	if cfg.Source.Sources == nil {
		cfg.Source.Sources = map[string]string{}
	}
	cfg.Source.Sources["server_host"] = "file"
	cfg.Source.Sources["server_port"] = "file"

	socketPath, ok, err := config.ServerLocalRPCSocketPath(cfg)
	if err != nil {
		t.Fatalf("ServerLocalRPCSocketPath: %v", err)
	}
	if !ok {
		t.Skip("local unix sockets unsupported on this platform")
	}
	shutdown := startUnixWebSocketServer(t, socketPath, func(ctx context.Context, conn rpcwire.Conn) {
		serveProjectListRPCWithProjectID(ctx, conn, "uds-project", udsHandlerErrs, &udsConnectionCount)
	})
	defer shutdown()

	remote, err := DialConfiguredRemote(context.Background(), cfg)
	if err != nil {
		t.Fatalf("DialConfiguredRemote: %v", err)
	}
	defer func() { _ = remote.Close() }()

	resp, err := remote.ListProjects(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(resp.Projects) != 1 || resp.Projects[0].ProjectId != "tcp-project" {
		t.Fatalf("Projects = %+v, want tcp-project from configured TCP target", resp.Projects)
	}
	if got := tcpConnectionCount.Load(); got != 1 {
		t.Fatalf("tcpConnectionCount = %d, want 1", got)
	}
	if got := udsConnectionCount.Load(); got != 0 {
		t.Fatalf("udsConnectionCount = %d, want 0", got)
	}
	requireNoHandlerError(t, tcpHandlerErrs)
	requireNoHandlerError(t, udsHandlerErrs)
}

func TestRemoteControlConnectionIsolatesCancellationAndMalformedFrames(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErrs := make(chan error, 8)
	firstRequestSeen := make(chan string, 1)
	method := serverpb.File_kent_api_server_server_proto.Services().ByName("ServerService").Methods().ByName("GetReadiness")
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("GetReadiness operation: %v", err)
	}
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connectionCount.Add(1)
		var firstCorrelation *string
		malformedResponseSent := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			if _, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{}); handled {
				if err != nil {
					reportHandlerError(handlerErrs, "setup: %v", err)
				}
				continue
			}
			switch event.Frame.Kind {
			case rpcwire.FrameBinary:
				envelope, err := protoapi.DecodeEnvelope(event.Frame.Payload)
				if err != nil {
					reportHandlerError(handlerErrs, "decode binary envelope: %v", err)
					return
				}
				call := envelope.GetCall()
				if call == nil {
					reportHandlerError(handlerErrs, "binary frame is not a call")
					return
				}
				if firstCorrelation == nil {
					projectList := projectpb.File_kent_api_project_project_proto.Services().
						ByName("ProjectCatalogService").Methods().ByName("List")
					projectListOperation, operationErr := protoapi.OperationFromDescriptor(projectList)
					if operationErr != nil {
						reportHandlerError(handlerErrs, "Project List operation: %v", operationErr)
						return
					}
					if call.Operation != projectListOperation.Name || call.Correlation == nil {
						reportHandlerError(handlerErrs, "first binary operation = %q, want %q", call.Operation, projectListOperation.Name)
						return
					}
					firstCorrelation = call.Correlation
					firstRequestSeen <- call.GetCorrelation()
					continue
				}
				if call.Operation != operation.Name {
					reportHandlerError(handlerErrs, "binary operation = %q, want %q", call.Operation, operation.Name)
					return
				}
				if err := protoapi.Decode(call.Payload, &emptypb.Empty{}); err != nil {
					reportHandlerError(handlerErrs, "decode GetReadiness request: %v", err)
					return
				}
				if !malformedResponseSent {
					malformedResponseSent = true
					if err := conn.Send(ctx, rpcwire.Frame{
						Kind:    rpcwire.FrameBinary,
						Payload: []byte{0xff},
					}); err != nil {
						reportHandlerError(handlerErrs, "send uncorrelated malformed response: %v", err)
						return
					}
					invalidEnvelope, err := proto.Marshal(&sharedpb.Envelope{
						Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
							Correlation: call.Correlation,
						}},
					})
					if err != nil {
						reportHandlerError(handlerErrs, "encode correlated malformed response: %v", err)
						return
					}
					if err := conn.Send(ctx, rpcwire.Frame{
						Kind:    rpcwire.FrameBinary,
						Payload: invalidEnvelope,
					}); err != nil {
						reportHandlerError(handlerErrs, "send correlated malformed response: %v", err)
						return
					}
					continue
				}
				result := &serverpb.GetReadinessResult{
					Outcome: &serverpb.GetReadinessResult_Success{Success: &serverpb.GetReadinessSuccess{
						Readiness: &serverpb.Readiness{
							Ready:           true,
							ServerId:        "server-1",
							ServerVersion:   "test",
							ServerBuild:     "test",
							ProtocolVersion: protocol.Version,
						},
					}},
				}
				payload, err := protoapi.Encode(result)
				if err != nil {
					reportHandlerError(handlerErrs, "encode binary result: %v", err)
					return
				}
				encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
					Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
						Operation:   call.Operation,
						Correlation: call.Correlation,
						Payload:     payload,
					}},
				})
				if err != nil {
					reportHandlerError(handlerErrs, "encode binary result envelope: %v", err)
					return
				}
				if err := conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded}); err != nil {
					reportHandlerError(handlerErrs, "send binary response: %v", err)
					return
				}
				lateFrame, err := remoteProjectListResultFrame("project-1", firstCorrelation)
				if err != nil {
					reportHandlerError(handlerErrs, "encode late first response: %v", err)
					return
				}
				if err := conn.Send(ctx, lateFrame); err != nil {
					reportHandlerError(handlerErrs, "send late first response: %w", err)
					return
				}
				return
			case rpcwire.FrameText:
				reportHandlerError(handlerErrs, "unexpected frame kind %d", event.Frame.Kind)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	cancelCtx, cancel := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := remote.ListProjects(cancelCtx, &emptypb.Empty{})
		firstErr <- err
	}()

	select {
	case <-firstRequestSeen:
	case err := <-handlerErrs:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first unary request")
	}
	requireNoHandlerError(t, handlerErrs)
	cancel()
	if err := <-firstErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("ListProjects error = %v, want context canceled", err)
	}

	readiness := &serverpb.GetReadinessResult{}
	if err := remote.callBinary(context.Background(), method, &emptypb.Empty{}, readiness); err == nil {
		t.Fatal("malformed correlated GetReadiness response succeeded")
	}
	if err := remote.callBinary(context.Background(), method, &emptypb.Empty{}, readiness); err != nil {
		t.Fatalf("binary GetReadiness: %v", err)
	}
	if readiness.GetSuccess().GetReadiness().GetServerId() != "server-1" {
		t.Fatalf("readiness = %+v, want server-1", readiness)
	}
	if got := connectionCount.Load(); got != 1 {
		t.Fatalf("connectionCount = %d, want 1", got)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestRemoteReconnectsUnaryControlConnectionAfterDrop(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connIndex := connectionCount.Add(1)
		handshaken := false
		if connIndex == 1 {
			for event := range conn.Events() {
				if event.Err != nil {
					return
				}
				if kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{}); handled {
					if err != nil {
						reportHandlerError(handlerErrs, "setup: %v", err)
					}
					handshaken = handshaken || kind == remoteTestSetupHandshake
					continue
				}
				if !handshaken {
					reportHandlerError(handlerErrs, "application frame arrived before handshake")
					return
				}
				if handled, err := handleRemoteProjectListFrame(ctx, conn, event.Frame, "project-1"); handled {
					if err != nil {
						reportHandlerError(handlerErrs, "send first Project List: %v", err)
					}
					return
				}
				reportHandlerError(handlerErrs, "unexpected first application frame")
				return
			}
		}
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			if kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{}); handled {
				if err != nil {
					reportHandlerError(handlerErrs, "setup: %v", err)
				}
				handshaken = handshaken || kind == remoteTestSetupHandshake
				continue
			}
			if !handshaken {
				reportHandlerError(handlerErrs, "application frame arrived before handshake")
				return
			}
			if handled, err := handleRemoteProjectListFrame(ctx, conn, event.Frame, "project-1"); handled {
				if err != nil {
					reportHandlerError(handlerErrs, "send second Project List: %v", err)
				}
				return
			}
			reportHandlerError(handlerErrs, "unexpected second application frame")
			return
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)
	waitForRemoteControlDisconnect(t, remote, handlerErrs)

	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("ListProjects after reconnect: %v", err)
	}
	if got := connectionCount.Load(); got != 2 {
		t.Fatalf("connectionCount = %d, want 2", got)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestRemoteSessionAttachmentSurvivesUnaryControlReconnect(t *testing.T) {
	var connectionCount atomic.Int32
	var attachCount atomic.Int32
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connIndex := connectionCount.Add(1)
		handshaken := false
		attached := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{
				projectID: "project-1", workspaceID: "workspace-1", workspaceRoot: "/workspace",
			})
			if handled {
				if err != nil {
					reportHandlerError(handlerErrs, "connection %d setup: %v", connIndex, err)
					return
				}
				handshaken = handshaken || kind == remoteTestSetupHandshake
				attached = attached || kind == remoteTestSetupSession
				if kind == remoteTestSetupSession {
					attachCount.Add(1)
				}
				continue
			}
			req := event.Frame.Request()
			if !handshaken || !attached {
				reportHandlerError(handlerErrs, "connection %d sent %q before binary setup completed", connIndex, req.Method)
				return
			}
			if req.Method != protocol.MethodWorktreeStatus {
				reportHandlerError(handlerErrs, "connection %d method = %q, want worktree status", connIndex, req.Method)
				return
			}
			if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.WorktreeStatusResponse{}))); err != nil {
				reportHandlerError(handlerErrs, "send worktree status response: %w", err)
			}
			return
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURLForSession(
		context.Background(),
		"ws"+server.URL[len("http"):],
		"session-1",
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForSession: %v", err)
	}
	defer func() { _ = remote.Close() }()

	request := serverapi.WorktreeStatusRequest{SessionID: "session-1"}
	if _, err := remote.GetWorktreeStatus(context.Background(), request); err != nil {
		t.Fatalf("first GetWorktreeStatus: %v", err)
	}
	waitForRemoteControlDisconnect(t, remote, handlerErrs)
	if _, err := remote.GetWorktreeStatus(context.Background(), request); err != nil {
		t.Fatalf("GetWorktreeStatus after reconnect: %v", err)
	}
	if got := connectionCount.Load(); got != 2 {
		t.Fatalf("connectionCount = %d, want 2", got)
	}
	if got := attachCount.Load(); got != 2 {
		t.Fatalf("attachCount = %d, want 2", got)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestRemoteProjectRootAttachmentRejectsDifferentWorkspaceOnReconnect(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connIndex := connectionCount.Add(1)
		handshaken := false
		attached := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			workspaceID := "workspace-a"
			workspaceRoot := "/canonical/workspace-a"
			if connIndex >= 2 {
				workspaceID = "workspace-b"
				workspaceRoot = "/canonical/workspace-b"
			}
			kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{
				projectID: "project-1", workspaceID: workspaceID, workspaceRoot: workspaceRoot,
			})
			if handled {
				if err != nil {
					reportHandlerError(handlerErrs, "connection %d setup: %v", connIndex, err)
					return
				}
				handshaken = handshaken || kind == remoteTestSetupHandshake
				attached = attached || kind == remoteTestSetupProject
				if kind == remoteTestSetupProject && connIndex >= 2 {
					return
				}
				continue
			}
			if !handshaken || !attached {
				reportHandlerError(handlerErrs, "connection %d sent application traffic before binary setup completed", connIndex)
				return
			}
			if handled, err := handleRemoteProjectListFrame(ctx, conn, event.Frame, "project-1"); handled {
				if err != nil {
					reportHandlerError(handlerErrs, "send Project List response: %v", err)
				}
				return
			}
			reportHandlerError(handlerErrs, "connection %d received unexpected application traffic", connIndex)
			return
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURLForProjectWorkspace(
		context.Background(),
		"ws"+server.URL[len("http"):],
		"project-1",
		"/workspace-a",
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForProjectWorkspace: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("first ListProjects: %v", err)
	}
	waitForRemoteControlDisconnect(t, remote, handlerErrs)
	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); err == nil {
		t.Fatal("reconnect with substituted workspace unexpectedly succeeded")
	}
	if got := remote.WorkspaceID(); got != "workspace-a" {
		t.Fatalf("authoritative WorkspaceID = %q, want workspace-a", got)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestRemoteInterruptUsesDedicatedConnWhileSubmitIsInFlight(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErrs := make(chan error, 8)
	submitStarted := make(chan struct{}, 1)
	interruptSeen := make(chan struct{}, 1)
	releaseSubmit := make(chan struct{})
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connectionCount.Add(1)
		handshaken := false
		attached := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{
				projectID: "project-1", workspaceID: "workspace-1", workspaceRoot: "/tmp/workspace-a",
			})
			if handled {
				if err != nil {
					reportHandlerError(handlerErrs, "setup: %v", err)
					return
				}
				handshaken = handshaken || kind == remoteTestSetupHandshake
				attached = attached || kind == remoteTestSetupProject
				continue
			}
			req := event.Frame.Request()
			if !handshaken {
				reportHandlerError(handlerErrs, "application method %q arrived before handshake", req.Method)
				return
			}
			if !attached {
				reportHandlerError(handlerErrs, "application method %q arrived before Project attachment", req.Method)
				return
			}
			switch req.Method {
			case protocol.MethodRuntimeSubmitUserTurn:
				select {
				case submitStarted <- struct{}{}:
				default:
				}
				<-releaseSubmit
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserTurnResponse{
					Message:    textutil.Value("done"),
					ResultKind: clientui.UserTurnResultKindAssistantFinal,
				}))); err != nil {
					reportHandlerError(handlerErrs, "send submit response: %w", err)
				}
				return
			case protocol.MethodRuntimeInterrupt:
				select {
				case interruptSeen <- struct{}{}:
				default:
				}
				version, versionErr := clientui.NewReadModelVersion("epoch-1", 1, 1)
				if versionErr != nil {
					reportHandlerError(handlerErrs, "build interrupt version: %w", versionErr)
					return
				}
				response := serverapi.RuntimeInterruptResponse{
					Version:  version,
					Activity: clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle},
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, response))); err != nil {
					reportHandlerError(handlerErrs, "send interrupt response: %w", err)
				}
				return
			default:
				reportHandlerError(handlerErrs, "unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], "project-1")
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	submitDone := make(chan error, 1)
	go func() {
		_, submitErr := remote.SubmitUserTurn(context.Background(), runtimeSubmitUserTurnRequestForTest("session-1", "run"))
		submitDone <- submitErr
	}()

	select {
	case <-submitStarted:
	case err := <-handlerErrs:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for submit start")
	}

	interruptCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := remote.Interrupt(interruptCtx, serverapi.RuntimeInterruptRequest{ClientRequestID: "interrupt-1", SessionID: "session-1"}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-interruptSeen:
	case err := <-handlerErrs:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("expected interrupt on dedicated connection")
	}
	if got := connectionCount.Load(); got < 3 {
		t.Fatalf("connectionCount = %d, want >= 3", got)
	}
	close(releaseSubmit)
	if err := <-submitDone; err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)
}

func TestValidateIdentityRoot(t *testing.T) {
	cases := []struct {
		name     string
		expected string
		identity protocol.ServerIdentity
		wantErr  bool
	}{
		{name: "empty disables", expected: "", identity: protocol.ServerIdentity{PersistenceRootID: "root-A"}, wantErr: false},
		{name: "match", expected: "root-A", identity: protocol.ServerIdentity{PersistenceRootID: "root-A"}, wantErr: false},
		{name: "mismatch", expected: "root-A", identity: protocol.ServerIdentity{PersistenceRootID: "root-B"}, wantErr: true},
		{name: "missing reported root", expected: "root-A", identity: protocol.ServerIdentity{}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateIdentityRoot(tc.expected, tc.identity)
			if tc.wantErr {
				if !errors.Is(err, ErrServerRootMismatch) {
					t.Fatalf("validateIdentityRoot = %v, want ErrServerRootMismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateIdentityRoot = %v, want nil", err)
			}
		})
	}
}

// TestRemoteReconnectRejectsChangedPersistenceRoot guards the P1 reconnect
// regression: a root-pinned client must not silently reattach to a different
// instance that takes over the configured endpoint after the original drops.
func TestRemoteReconnectRejectsChangedPersistenceRoot(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connIndex := connectionCount.Add(1)
		rootID := "root-A"
		if connIndex >= 2 {
			rootID = "root-B"
		}
		handshaken := false
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			if kind, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{rootID: rootID}); handled {
				if err != nil {
					reportHandlerError(handlerErrs, "setup: %v", err)
				}
				handshaken = handshaken || kind == remoteTestSetupHandshake
				continue
			}
			if !handshaken {
				reportHandlerError(handlerErrs, "application traffic arrived before handshake")
				return
			}
			if connIndex == 1 {
				if handled, err := handleRemoteProjectListFrame(ctx, conn, event.Frame, "project-1"); handled {
					if err != nil {
						reportHandlerError(handlerErrs, "send first Project List: %v", err)
					}
					return
				}
				reportHandlerError(handlerErrs, "unexpected first application traffic")
				return
			}
			reportHandlerError(handlerErrs, "mismatched-root connection should not receive application traffic")
			return
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if err := remote.RequireRoot("root-A"); err != nil {
		t.Fatalf("RequireRoot: %v", err)
	}
	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("first ListProjects: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)
	waitForRemoteControlDisconnect(t, remote, handlerErrs)

	if _, err := remote.ListProjects(context.Background(), &emptypb.Empty{}); !errors.Is(err, ErrServerRootMismatch) {
		t.Fatalf("reconnect ListProjects = %v, want ErrServerRootMismatch", err)
	}
	requireNoHandlerError(t, handlerErrs)
}

func serveHandshakeWithRoot(ctx context.Context, conn rpcwire.Conn, rootID string, handlerErrs chan<- error) {
	for event := range conn.Events() {
		if event.Err != nil {
			return
		}
		if _, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{rootID: rootID}); handled {
			if err != nil {
				reportHandlerError(handlerErrs, "setup: %v", err)
			}
			continue
		}
		req := event.Frame.Request()
		reportHandlerError(handlerErrs, "unexpected method %q", req.Method)
		return
	}
}

func startUnixWebSocketServer(t *testing.T, socketPath string, handler func(context.Context, rpcwire.Conn)) func() {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen unix: %v", err)
	}
	httpServer := &http.Server{Handler: rpcwire.NewWebSocketTransport().Handler(handler)}
	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(listener) }()
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Fatalf("unix websocket server: %v", err)
			}
		default:
		}
		_ = os.Remove(socketPath)
	}
}

func startUnixStallingListener(t *testing.T, socketPath string, stall time.Duration) (net.Listener, <-chan struct{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = os.Remove(socketPath)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen unix: %v", err)
	}
	accepted := make(chan struct{}, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				time.Sleep(stall)
			}(conn)
		}
	}()
	return listener, accepted
}

func testRemoteConfigFromServerURL(t *testing.T, persistenceRoot string, serverURL string) config.App {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("Parse server URL: %v", err)
	}
	host, portValue, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	port, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatalf("Atoi port: %v", err)
	}
	return config.App{PersistenceRoot: persistenceRoot, Settings: config.Settings{ServerHost: host, ServerPort: port}}
}

func serveProjectListRPC(ctx context.Context, conn rpcwire.Conn, handlerErrs chan<- error) {
	serveProjectListRPCWithProjectID(ctx, conn, "server-1-project", handlerErrs, nil)
}

func serveProjectListRPCWithProjectID(ctx context.Context, conn rpcwire.Conn, projectID string, handlerErrs chan<- error, connectionCount *atomic.Int32) {
	if connectionCount != nil {
		connectionCount.Add(1)
	}
	for event := range conn.Events() {
		if event.Err != nil {
			return
		}
		if _, handled, err := handleRemoteTestSetupFrame(ctx, conn, event.Frame, remoteTestSetupResponse{}); handled {
			if err != nil {
				reportHandlerError(handlerErrs, "setup: %v", err)
			}
			continue
		}
		if handled, err := handleRemoteProjectListFrame(ctx, conn, event.Frame, projectID); handled {
			if err != nil {
				reportHandlerError(handlerErrs, "send Project List response: %w", err)
			}
			return
		}
		reportHandlerError(handlerErrs, "unexpected Project List frame kind %d", event.Frame.Kind)
		return
	}
}

func reportHandlerError(handlerErrs chan<- error, format string, args ...any) {
	select {
	case handlerErrs <- fmt.Errorf(format, args...):
	default:
	}
}

func requireNoHandlerError(t *testing.T, handlerErrs <-chan error) {
	t.Helper()
	select {
	case err := <-handlerErrs:
		t.Fatal(err)
	default:
	}
}

func waitForRemoteControlDisconnect(t *testing.T, remote *Remote, handlerErrs <-chan error) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		requireNoHandlerError(t, handlerErrs)
		remote.mu.Lock()
		controlDone := remote.control == nil || remote.control.IsDone()
		remote.mu.Unlock()
		if controlDone {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for dropped control connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
