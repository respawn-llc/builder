package client

import (
	"context"
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
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
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

	if _, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
		t.Fatalf("ListProjects: %v", err)
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

	if _, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
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
	if _, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
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

	resp, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(resp.Projects) != 1 || resp.Projects[0].ProjectID != "tcp-project" {
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

func TestRemoteCanceledUnaryRequestKeepsPersistentControlConnection(t *testing.T) {
	var connectionCount atomic.Int32
	handlerErrs := make(chan error, 8)
	firstRequestSeen := make(chan string, 1)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		connectionCount.Add(1)
		firstRequestID := ""
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			if req.Method == protocol.MethodHandshake {
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
					reportHandlerError(handlerErrs, "send handshake response: %w", err)
					return
				}
				continue
			}
			switch req.Method {
			case protocol.MethodProjectList:
				firstRequestID = req.ID
				firstRequestSeen <- firstRequestID
			case protocol.MethodProjectResolvePath:
				if firstRequestID == "" {
					reportHandlerError(handlerErrs, "expected first request id before second call")
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.ProjectResolvePathResponse{CanonicalRoot: "/tmp/workspace-a"}))); err != nil {
					reportHandlerError(handlerErrs, "send second response: %w", err)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(firstRequestID, serverapi.ProjectListResponse{}))); err != nil {
					reportHandlerError(handlerErrs, "send late first response: %w", err)
					return
				}
				return
			default:
				reportHandlerError(handlerErrs, "unexpected unary method %q", req.Method)
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
		_, err := remote.ListProjects(cancelCtx, serverapi.ProjectListRequest{})
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

	resolveResp, err := remote.ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: "/tmp/workspace-a"})
	if err != nil {
		t.Fatalf("ResolveProjectPath: %v", err)
	}
	if resolveResp.CanonicalRoot != "/tmp/workspace-a" {
		t.Fatalf("CanonicalRoot = %q, want /tmp/workspace-a", resolveResp.CanonicalRoot)
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
				req := event.Frame.Request()
				if !handshaken {
					if req.Method != protocol.MethodHandshake {
						reportHandlerError(handlerErrs, "first method = %q, want handshake", req.Method)
						return
					}
					if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
						reportHandlerError(handlerErrs, "send handshake response: %w", err)
						return
					}
					handshaken = true
					continue
				}
				if req.Method != protocol.MethodProjectList {
					reportHandlerError(handlerErrs, "first method = %q, want %q", req.Method, protocol.MethodProjectList)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.ProjectListResponse{}))); err != nil {
					reportHandlerError(handlerErrs, "send first response: %w", err)
					return
				}
				return
			}
		}
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			if !handshaken {
				if req.Method != protocol.MethodHandshake {
					reportHandlerError(handlerErrs, "second method = %q, want handshake", req.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
					reportHandlerError(handlerErrs, "send handshake response: %w", err)
					return
				}
				handshaken = true
				continue
			}
			if req.Method != protocol.MethodProjectResolvePath {
				reportHandlerError(handlerErrs, "second method = %q, want %q", req.Method, protocol.MethodProjectResolvePath)
				return
			}
			if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.ProjectResolvePathResponse{CanonicalRoot: "/tmp/reconnected"}))); err != nil {
				reportHandlerError(handlerErrs, "send second response: %w", err)
				return
			}
			return
		}
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)

	deadline := time.Now().Add(2 * time.Second)
	for {
		requireNoHandlerError(t, handlerErrs)
		remote.mu.Lock()
		controlDone := remote.control == nil || remote.control.IsDone()
		remote.mu.Unlock()
		if controlDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for dropped control connection")
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := remote.ResolveProjectPath(context.Background(), serverapi.ProjectResolvePathRequest{Path: "/tmp/reconnected"})
	if err != nil {
		t.Fatalf("ResolveProjectPath after reconnect: %v", err)
	}
	if resp.CanonicalRoot != "/tmp/reconnected" {
		t.Fatalf("CanonicalRoot = %q, want /tmp/reconnected", resp.CanonicalRoot)
	}
	if got := connectionCount.Load(); got != 2 {
		t.Fatalf("connectionCount = %d, want 2", got)
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
			req := event.Frame.Request()
			if !handshaken {
				if req.Method != protocol.MethodHandshake {
					reportHandlerError(handlerErrs, "first method = %q, want handshake", req.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
					reportHandlerError(handlerErrs, "send handshake response: %w", err)
					return
				}
				handshaken = true
				continue
			}
			if !attached {
				if req.Method != protocol.MethodAttachProject {
					reportHandlerError(handlerErrs, "second method = %q, want attach project", req.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.AttachResponse{Kind: "project", ProjectID: "project-1", WorkspaceRoot: "/tmp/workspace-a"}))); err != nil {
					reportHandlerError(handlerErrs, "send attach response: %w", err)
					return
				}
				attached = true
				continue
			}
			switch req.Method {
			case protocol.MethodRuntimeSubmitUserMessage:
				select {
				case submitStarted <- struct{}{}:
				default:
				}
				<-releaseSubmit
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.RuntimeSubmitUserMessageResponse{Message: "done"}))); err != nil {
					reportHandlerError(handlerErrs, "send submit response: %w", err)
				}
				return
			case protocol.MethodRuntimeInterrupt:
				select {
				case interruptSeen <- struct{}{}:
				default:
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, struct{}{}))); err != nil {
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
		_, submitErr := remote.SubmitUserMessage(context.Background(), serverapi.RuntimeSubmitUserMessageRequest{ClientRequestID: "submit-1", SessionID: "session-1", Text: "run"})
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
	if err := remote.Interrupt(interruptCtx, serverapi.RuntimeInterruptRequest{ClientRequestID: "interrupt-1", SessionID: "session-1"}); err != nil {
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

func TestRemoteRequireRootValidatesPinnedIdentity(t *testing.T) {
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		serveHandshakeWithRoot(ctx, conn, "root-A", handlerErrs)
	}))
	defer server.Close()

	remote, err := DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if err := remote.RequireRoot("root-A"); err != nil {
		t.Fatalf("RequireRoot matching root: %v", err)
	}
	if err := remote.RequireRoot(""); err != nil {
		t.Fatalf("RequireRoot empty (validation disabled): %v", err)
	}
	if err := remote.RequireRoot("root-B"); !errors.Is(err, ErrServerRootMismatch) {
		t.Fatalf("RequireRoot mismatched root = %v, want ErrServerRootMismatch", err)
	}
	requireNoHandlerError(t, handlerErrs)
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
			req := event.Frame.Request()
			if !handshaken {
				if req.Method != protocol.MethodHandshake {
					reportHandlerError(handlerErrs, "first method = %q, want handshake", req.Method)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1", PersistenceRootID: rootID}}))); err != nil {
					reportHandlerError(handlerErrs, "send handshake response: %w", err)
					return
				}
				handshaken = true
				continue
			}
			if connIndex == 1 {
				if req.Method != protocol.MethodProjectList {
					reportHandlerError(handlerErrs, "first method = %q, want %q", req.Method, protocol.MethodProjectList)
					return
				}
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, serverapi.ProjectListResponse{}))); err != nil {
					reportHandlerError(handlerErrs, "send first response: %w", err)
					return
				}
				return
			}
			reportHandlerError(handlerErrs, "mismatched-root connection should not receive method %q", req.Method)
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
	if _, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{}); err != nil {
		t.Fatalf("first ListProjects: %v", err)
	}
	requireNoHandlerError(t, handlerErrs)

	deadline := time.Now().Add(2 * time.Second)
	for {
		requireNoHandlerError(t, handlerErrs)
		remote.mu.Lock()
		controlDone := remote.control == nil || remote.control.IsDone()
		remote.mu.Unlock()
		if controlDone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for dropped control connection")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := remote.ListProjects(context.Background(), serverapi.ProjectListRequest{}); !errors.Is(err, ErrServerRootMismatch) {
		t.Fatalf("reconnect ListProjects = %v, want ErrServerRootMismatch", err)
	}
	requireNoHandlerError(t, handlerErrs)
}

func serveHandshakeWithRoot(ctx context.Context, conn rpcwire.Conn, rootID string, handlerErrs chan<- error) {
	for event := range conn.Events() {
		if event.Err != nil {
			return
		}
		req := event.Frame.Request()
		if req.Method == protocol.MethodHandshake {
			if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1", PersistenceRootID: rootID}}))); err != nil {
				reportHandlerError(handlerErrs, "send handshake response: %w", err)
				return
			}
			continue
		}
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
		req := event.Frame.Request()
		if req.Method == protocol.MethodHandshake {
			if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}}))); err != nil {
				reportHandlerError(handlerErrs, "send handshake response: %w", err)
				return
			}
			continue
		}
		if req.Method != protocol.MethodProjectList {
			reportHandlerError(handlerErrs, "project list method = %q", req.Method)
			return
		}
		response := serverapi.ProjectListResponse{Projects: []clientui.ProjectSummary{{ProjectID: projectID}}}
		if err := conn.Send(ctx, rpcwire.FrameFromResponse(protocol.NewSuccessResponse(req.ID, response))); err != nil {
			reportHandlerError(handlerErrs, "send project list response: %w", err)
			return
		}
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
