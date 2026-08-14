package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/metadata"
	"core/server/session"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/sessioncontract"

	"golang.org/x/net/websocket"
)

func newGatewayTestServerForConfig(t *testing.T, cfg config.App) (*core.Core, *httptest.Server) {
	t.Helper()
	authSupport := newGatewayTestAuthSupport(t, true)
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	appCore, err := core.New(cfg, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	server := newGatewayHTTPTestServer(t, appCore)
	t.Cleanup(server.Close)
	return appCore, server
}

func resolveGatewayTestConfig(t *testing.T, workspace string) serverbootstrap.ConfigPlan {
	t.Helper()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	return resolved
}

func registerGatewayTestBinding(t *testing.T, cfg config.App) metadata.Binding {
	t.Helper()
	binding, err := metadata.RegisterBinding(context.Background(), cfg.PersistenceRoot, cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	return binding
}

func newGatewayTestServer(t *testing.T) (*core.Core, *httptest.Server) {
	t.Helper()
	appCore, server, _ := newGatewayTestServerWithAuth(t, true)
	return appCore, server
}

func newGatewayTestServerWithAuth(t *testing.T, ready bool) (*core.Core, *httptest.Server, serverbootstrap.AuthSupport) {
	t.Helper()
	appCore, authSupport := newGatewayTestCore(t, true, ready)
	return appCore, newGatewayHTTPTestServer(t, appCore), authSupport
}

func newUnboundGatewayTestServer(t *testing.T) (*core.Core, *httptest.Server) {
	t.Helper()
	appCore, _ := newGatewayTestCore(t, false, true)
	if appCore.ProjectID() != "" {
		t.Fatalf("unbound core project id = %q, want empty", appCore.ProjectID())
	}
	return appCore, newGatewayHTTPTestServer(t, appCore)
}

func newGatewayTestCore(t *testing.T, bindWorkspace, ready bool) (*core.Core, serverbootstrap.AuthSupport) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	if bindWorkspace {
		registerGatewayWorkspace(t, workspace)
	} else {
		configureGatewayTestServerPort(t)
	}
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupport := newGatewayTestAuthSupport(t, ready)
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	appCore, err := core.New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	return appCore, authSupport
}

func newGatewayHTTPTestServer(t *testing.T, appCore *core.Core) *httptest.Server {
	t.Helper()
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return httptest.NewServer(gateway.Handler())
}

func createGatewayAuthoritativeSession(t *testing.T, appCore *core.Core) *session.Store {
	t.Helper()
	metadataStore := appCore.MetadataStore()
	if metadataStore == nil {
		t.Fatal("core metadata store is required")
	}
	store, err := session.Create(
		filepath.Join(appCore.Config().PersistenceRoot, "projects", appCore.ProjectID(), "sessions"),
		filepath.Base(appCore.Config().WorkspaceRoot),
		appCore.Config().WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	return store
}

func dialGateway(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	conn, err := websocket.Dial("ws"+server.URL[len("http"):], "", server.URL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return conn
}

func handshakeGateway(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	handshakeGatewayWithCapabilities(t, conn, &protocol.ClientCapabilities{TranscriptLiveRunFinished: true})
}

func handshakeGatewayWithCapabilities(t *testing.T, conn *websocket.Conn, capabilities *protocol.ClientCapabilities) {
	t.Helper()
	callGateway(t, conn, "1", protocol.MethodHandshake, protocol.HandshakeRequest{
		ProtocolVersion: protocol.Version, ClientCapabilities: capabilities,
	}, nil)
}

func callGateway(t *testing.T, conn *websocket.Conn, id, method string, params, out any) {
	t.Helper()
	if err := websocket.JSON.Send(conn, protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: id, Method: method, Params: mustJSON(t, params)}); err != nil {
		t.Fatalf("send %s: %v", method, err)
	}
	var response protocol.Response
	if err := websocket.JSON.Receive(conn, &response); err != nil {
		t.Fatalf("receive %s: %v", method, err)
	}
	if response.Error != nil {
		t.Fatalf("%s error: %+v", method, response.Error)
	}
	if out != nil && len(response.Result) > 0 {
		if err := json.Unmarshal(response.Result, out); err != nil {
			t.Fatalf("decode %s: %v", method, err)
		}
	}
}

func callGatewayExpectError(t *testing.T, conn *websocket.Conn, id, method string, params any) *protocol.ResponseError {
	t.Helper()
	if err := websocket.JSON.Send(conn, protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: id, Method: method, Params: mustJSON(t, params)}); err != nil {
		t.Fatalf("send %s: %v", method, err)
	}
	var response protocol.Response
	if err := websocket.JSON.Receive(conn, &response); err != nil {
		t.Fatalf("receive %s: %v", method, err)
	}
	if response.Error == nil {
		t.Fatalf("%s unexpectedly succeeded", method)
	}
	return response.Error
}

func callGatewayRaw(t *testing.T, conn *websocket.Conn, id, method string, params json.RawMessage) protocol.Response {
	t.Helper()
	if err := websocket.JSON.Send(conn, protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: id, Method: method, Params: params}); err != nil {
		t.Fatalf("send raw %s: %v", method, err)
	}
	var response protocol.Response
	if err := websocket.JSON.Receive(conn, &response); err != nil {
		t.Fatalf("receive raw %s: %v", method, err)
	}
	return response
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return data
}
