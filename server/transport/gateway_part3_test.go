package transport

import (
	"context"
	"core/internal/testharness/testsetup"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/session"
	remoteclient "core/shared/client"
	"core/shared/protoapi"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	connectionpb "core/shared/protoapi/gen/kent/api/connection"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/net/websocket"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestGatewaySessionAttachEstablishesProjectForUnboundServer(t *testing.T) {
	appCore, server := newUnboundGatewayTestServer(t)
	testsetup.InitializeGitRepository(t, appCore.Config().WorkspaceRoot)
	binding, err := appCore.MetadataStore().RegisterWorkspaceBinding(
		context.Background(),
		appCore.Config().WorkspaceRoot,
	)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	store, err := session.Create(
		filepath.Join(appCore.Config().PersistenceRoot, "projects", binding.ProjectID, "sessions"),
		filepath.Base(appCore.Config().WorkspaceRoot),
		appCore.Config().WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		appCore.MetadataStore().AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	remote, err := remoteclient.DialRemoteURLForSession(
		context.Background(),
		"ws"+server.URL[len("http"):],
		store.Meta().SessionID,
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForSession: %v", err)
	}
	defer func() { _ = remote.Close() }()
	status, err := remote.GetWorktreeStatus(
		context.Background(),
		serverapi.WorktreeStatusRequest{SessionID: store.Meta().SessionID},
	)
	if err != nil {
		t.Fatalf("GetWorktreeStatus: %v", err)
	}
	if status.Target.WorkspaceID != binding.WorkspaceID {
		t.Fatalf("target workspace id = %q, want %q", status.Target.WorkspaceID, binding.WorkspaceID)
	}
}

func TestCompactContextWithoutReadyRuntimePreservesJoinedErrorLoopbackAndRemote(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)

	request := serverapi.RuntimeCompactContextRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       store.Meta().SessionID,
		Args:            "compact without a Ready runtime",
	}
	loopbackErr := appCore.RuntimeControlClient().CompactContext(context.Background(), request)
	requireCompactRuntimeUnavailableNotAccepted(t, loopbackErr)
	if err := appCore.RuntimeControlClient().SetSessionName(context.Background(), serverapi.RuntimeSetSessionNameRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       store.Meta().SessionID,
		Name:            "must remain unavailable",
	}); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("loopback compact created a runtime: SetSessionName error = %v", err)
	}

	remote, err := remoteclient.DialRemoteURLForSession(
		context.Background(),
		"ws"+server.URL[len("http"):],
		store.Meta().SessionID,
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForSession: %v", err)
	}
	defer func() { _ = remote.Close() }()
	request.ClientRequestID = runtimeids.NewRuntimeClientRequestID().String()
	remoteErr := remote.CompactContext(context.Background(), request)
	requireCompactRuntimeUnavailableNotAccepted(t, remoteErr)
	if err := appCore.RuntimeControlClient().SetSessionName(context.Background(), serverapi.RuntimeSetSessionNameRequest{
		ClientRequestID: runtimeids.NewRuntimeClientRequestID().String(),
		SessionID:       store.Meta().SessionID,
		Name:            "must still remain unavailable",
	}); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("remote compact created a runtime: SetSessionName error = %v", err)
	}
}

func requireCompactRuntimeUnavailableNotAccepted(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("CompactContext error = %v, want runtime command not accepted", err)
	}
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("CompactContext error = %v, want runtime unavailable", err)
	}
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

func newGatewayTestCore(t *testing.T, bindWorkspace bool, ready bool) (*core.Core, serverbootstrap.AuthSupport) {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
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
	gateway, err := NewGateway(appCore, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return httptest.NewServer(gateway.Handler())
}

func gatewayTestIdentity() protocol.ServerIdentity {
	return protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
		PID:             os.Getpid(),
	}
}

func serveGatewayRemoteTestHandshake(ctx context.Context, conn rpcwire.Conn, frame rpcwire.Frame) error {
	envelope, err := protoapi.DecodeEnvelope(frame.Payload)
	if err != nil {
		return err
	}
	call := envelope.GetCall()
	if call == nil || call.Correlation == nil {
		return errors.New("correlated Handshake call is required")
	}
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").Methods().ByName("Handshake")
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		return err
	}
	if call.Operation != operation.Name {
		return fmt.Errorf("unexpected binary operation %q", call.Operation)
	}
	payload, err := protoapi.Encode(&connectionpb.HandshakeResult{
		Outcome: &connectionpb.HandshakeResult_Success{
			Success: &connectionpb.HandshakeSuccess{Identity: &connectionpb.ServerIdentity{
				ProtocolVersion: protocol.Version,
				ServerId:        "server-1",
				Pid:             1,
			}},
		},
	})
	if err != nil {
		return err
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Result{Result: &sharedpb.Result{
			Operation: operation.Name, Correlation: call.Correlation, Payload: payload,
		}},
	})
	if err != nil {
		return err
	}
	return conn.Send(ctx, rpcwire.Frame{Kind: rpcwire.FrameBinary, Payload: encoded})
}

func createGatewayAuthoritativeSession(t *testing.T, appCore *core.Core) *session.Store {
	t.Helper()
	metadataStore := appCore.MetadataStore()
	if metadataStore == nil {
		t.Fatal("core metadata store is required")
	}
	store, err := session.Create(
		filepath.Join(filepath.Join(appCore.Config().PersistenceRoot, "projects"), appCore.ProjectID(), "sessions"),
		filepath.Base(appCore.Config().WorkspaceRoot),
		appCore.Config().WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
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
	wsURL := "ws" + server.URL[len("http"):]
	conn, err := websocket.Dial(wsURL, "", server.URL)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return conn
}

func handshakeGateway(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	result := handshakeGatewayVersion(t, conn, protocol.Version)
	if result.GetSuccess() == nil {
		t.Fatalf("handshake failed: %+v", result.GetError())
	}
}

func handshakeGatewayVersion(
	t *testing.T,
	conn *websocket.Conn,
	version string,
) *connectionpb.HandshakeResult {
	t.Helper()
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").
		Methods().
		ByName("Handshake")
	result := &connectionpb.HandshakeResult{}
	callGatewayDescriptor(
		t,
		conn,
		"1",
		method,
		&connectionpb.HandshakeRequest{ProtocolVersion: version},
		result,
	)
	return result
}

func attachGatewayProject(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	request *connectionpb.AttachProjectRequest,
) *connectionpb.AttachProjectResult {
	t.Helper()
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").
		Methods().
		ByName("AttachProject")
	result := &connectionpb.AttachProjectResult{}
	callGatewayDescriptor(t, conn, correlation, method, request, result)
	return result
}

func attachGatewaySession(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	sessionID string,
) *connectionpb.AttachSessionResult {
	t.Helper()
	method := connectionpb.File_kent_api_connection_connection_proto.Services().
		ByName("ConnectionService").
		Methods().
		ByName("AttachSession")
	result := &connectionpb.AttachSessionResult{}
	callGatewayDescriptor(
		t,
		conn,
		correlation,
		method,
		&connectionpb.AttachSessionRequest{SessionId: sessionID},
		result,
	)
	return result
}

func requireGatewayProjectAttachment(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	request *connectionpb.AttachProjectRequest,
) {
	t.Helper()
	if result := attachGatewayProject(t, conn, correlation, request); result.GetSuccess() == nil {
		t.Fatalf("%s failed: %+v", correlation, result.GetError())
	}
}

func requireGatewaySessionAttachment(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	sessionID string,
) {
	t.Helper()
	if result := attachGatewaySession(t, conn, correlation, sessionID); result.GetSuccess() == nil {
		t.Fatalf("%s failed: %+v", correlation, result.GetError())
	}
}

func callGatewayDescriptor(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	method protoreflect.MethodDescriptor,
	request proto.Message,
	result proto.Message,
) {
	t.Helper()
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("operation descriptor: %v", err)
	}
	payload, err := protoapi.Encode(request)
	if err != nil {
		t.Fatalf("encode %s request: %v", operation.Name, err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
			Operation:   operation.Name,
			Correlation: &correlation,
			Payload:     payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode %s call: %v", operation.Name, err)
	}
	if err := websocket.Message.Send(conn, encoded); err != nil {
		t.Fatalf("send %s: %v", operation.Name, err)
	}
	var responseFrame []byte
	if err := websocket.Message.Receive(conn, &responseFrame); err != nil {
		t.Fatalf("receive %s: %v", operation.Name, err)
	}
	envelope, err := protoapi.DecodeEnvelope(responseFrame)
	if err != nil {
		t.Fatalf("decode %s response envelope: %v", operation.Name, err)
	}
	if failure := envelope.GetTransportFailure(); failure != nil {
		t.Fatalf("%s transport failure: %+v", operation.Name, failure)
	}
	response := envelope.GetResult()
	if response == nil {
		t.Fatalf("%s result is required", operation.Name)
	}
	if response.Operation != operation.Name || response.GetCorrelation() != correlation {
		t.Fatalf("%s result identity = %+v", operation.Name, response)
	}
	if err := protoapi.Decode(response.Payload, result); err != nil {
		t.Fatalf("decode %s result: %v", operation.Name, err)
	}
}

func callGatewayDescriptorPayload(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	method protoreflect.MethodDescriptor,
	payload []byte,
) *sharedpb.Envelope {
	t.Helper()
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("operation descriptor: %v", err)
	}
	encoded, err := protoapi.EncodeEnvelope(&sharedpb.Envelope{
		Frame: &sharedpb.Envelope_Call{Call: &sharedpb.Call{
			Operation:   operation.Name,
			Correlation: &correlation,
			Payload:     payload,
		}},
	})
	if err != nil {
		t.Fatalf("encode %s call: %v", operation.Name, err)
	}
	if err := websocket.Message.Send(conn, encoded); err != nil {
		t.Fatalf("send %s: %v", operation.Name, err)
	}
	var responseFrame []byte
	if err := websocket.Message.Receive(conn, &responseFrame); err != nil {
		t.Fatalf("receive %s: %v", operation.Name, err)
	}
	envelope, err := protoapi.DecodeEnvelope(responseFrame)
	if err != nil {
		t.Fatalf("decode %s response envelope: %v", operation.Name, err)
	}
	return envelope
}

func gatewayOperationName(t *testing.T, method protoreflect.MethodDescriptor) string {
	t.Helper()
	operation, err := protoapi.OperationFromDescriptor(method)
	if err != nil {
		t.Fatalf("operation descriptor: %v", err)
	}
	return operation.Name
}

func gatewayAuthMethod(t *testing.T, name protoreflect.Name) protoreflect.MethodDescriptor {
	t.Helper()
	method := authpb.File_kent_api_auth_auth_proto.Services().ByName("AuthService").Methods().ByName(name)
	if method == nil {
		t.Fatalf("AuthService.%s descriptor is required", name)
	}
	return method
}

func callGatewayAuthBootstrapStatus(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
) serverapi.AuthGetBootstrapStatusResponse {
	t.Helper()
	var result authpb.GetBootstrapStatusResult
	callGatewayDescriptor(t, conn, correlation, gatewayAuthMethod(t, "GetBootstrapStatus"), &emptypb.Empty{}, &result)
	if result.GetSuccess() == nil {
		t.Fatalf("GetBootstrapStatus failed: %+v", result.GetError())
	}
	status, err := protoapi.AuthBootstrapStatusFromProto(result.GetSuccess())
	if err != nil {
		t.Fatalf("decode GetBootstrapStatus success: %v", err)
	}
	return status
}

func callGatewayAuthCompleteBootstrap(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	request serverapi.AuthCompleteBootstrapRequest,
) serverapi.AuthCompleteBootstrapResponse {
	t.Helper()
	message, err := protoapi.AuthCompleteBootstrapRequestToProto(request)
	if err != nil {
		t.Fatalf("encode CompleteBootstrap request: %v", err)
	}
	var result authpb.CompleteBootstrapResult
	callGatewayDescriptor(t, conn, correlation, gatewayAuthMethod(t, "CompleteBootstrap"), message, &result)
	if result.GetSuccess() == nil {
		t.Fatalf("CompleteBootstrap failed: %+v", result.GetError())
	}
	response, err := protoapi.AuthBootstrapCompletionFromProto(result.GetSuccess())
	if err != nil {
		t.Fatalf("decode CompleteBootstrap success: %v", err)
	}
	return response
}

func callGatewayAuthAcknowledgeNoAuth(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
) serverapi.AuthAcknowledgeNoAuthResponse {
	t.Helper()
	var result authpb.AcknowledgeNoAuthResult
	callGatewayDescriptor(t, conn, correlation, gatewayAuthMethod(t, "AcknowledgeNoAuth"), &emptypb.Empty{}, &result)
	if result.GetSuccess() == nil {
		t.Fatalf("AcknowledgeNoAuth failed: %+v", result.GetError())
	}
	response, err := protoapi.AuthNoAuthAcknowledgementFromProto(result.GetSuccess())
	if err != nil {
		t.Fatalf("decode AcknowledgeNoAuth success: %v", err)
	}
	return response
}

func callGatewayAuthStatus(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	request serverapi.AuthStatusRequest,
) serverapi.AuthStatusResponse {
	t.Helper()
	message, err := protoapi.AuthStatusRequestToProto(request)
	if err != nil {
		t.Fatalf("encode GetStatus request: %v", err)
	}
	var result authpb.GetStatusResult
	callGatewayDescriptor(t, conn, correlation, gatewayAuthMethod(t, "GetStatus"), message, &result)
	if result.GetSuccess() == nil {
		t.Fatalf("GetStatus failed: %+v", result.GetError())
	}
	response, err := protoapi.AuthStatusFromProto(result.GetSuccess())
	if err != nil {
		t.Fatalf("decode GetStatus success: %v", err)
	}
	return response
}

func callGateway(t *testing.T, conn *websocket.Conn, id string, method string, params any, out any) {
	t.Helper()
	if err := websocket.JSON.Send(conn, protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: id, Method: method, Params: mustJSON(t, params)}); err != nil {
		t.Fatalf("send %s: %v", method, err)
	}
	var resp protocol.Response
	if err := websocket.JSON.Receive(conn, &resp); err != nil {
		t.Fatalf("receive %s: %v", method, err)
	}
	if resp.Error != nil {
		t.Fatalf("%s error: %+v", method, resp.Error)
	}
	if out != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			t.Fatalf("decode %s: %v", method, err)
		}
	}
}

func callGatewayExpectError(t *testing.T, conn *websocket.Conn, id string, method string, params any) *protocol.ResponseError {
	t.Helper()
	if err := websocket.JSON.Send(conn, protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: id, Method: method, Params: mustJSON(t, params)}); err != nil {
		t.Fatalf("send %s: %v", method, err)
	}
	var resp protocol.Response
	if err := websocket.JSON.Receive(conn, &resp); err != nil {
		t.Fatalf("receive %s: %v", method, err)
	}
	if resp.Error == nil {
		t.Fatalf("%s unexpectedly succeeded", method)
	}
	return resp.Error
}

func callGatewayRaw(t *testing.T, conn *websocket.Conn, id string, method string, params json.RawMessage) protocol.Response {
	t.Helper()
	if err := websocket.JSON.Send(conn, protocol.Request{JSONRPC: protocol.JSONRPCVersion, ID: id, Method: method, Params: params}); err != nil {
		t.Fatalf("send raw %s: %v", method, err)
	}
	var resp protocol.Response
	if err := websocket.JSON.Receive(conn, &resp); err != nil {
		t.Fatalf("receive raw %s: %v", method, err)
	}
	return resp
}

func TestGatewayRunPromptValidatesTypedIntentCallerAndSelector(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})

	valid := map[string]json.RawMessage{
		"omitted caller": []byte(`{"client_request_id":"raw-omitted","intent":{"kind":"open_existing","session_id":"missing-session"},"prompt":"hello"}`),
		"null caller":    []byte(`{"client_request_id":"raw-null","intent":{"kind":"open_existing","session_id":"missing-session"},"caller_session_id":null,"prompt":"hello","overrides":{"agent_role":null}}`),
	}
	var validCode int
	for name, params := range valid {
		t.Run(name, func(t *testing.T) {
			resp := callGatewayRaw(t, conn, "raw-valid-"+name, protocol.MethodRunPrompt, params)
			if resp.Error == nil {
				t.Fatal("raw request unexpectedly succeeded")
			}
			if resp.Error.Code == protocol.ErrCodeInvalidParams {
				t.Fatalf("raw %s request rejected as invalid params: %+v", name, resp.Error)
			}
			if validCode == 0 {
				validCode = resp.Error.Code
			} else if resp.Error.Code != validCode {
				t.Fatalf("raw %s code = %d, want omitted/null equivalence code %d", name, resp.Error.Code, validCode)
			}
		})
	}

	for name, params := range map[string]json.RawMessage{
		"empty caller":        []byte(`{"client_request_id":"raw-empty-caller","caller_session_id":"","prompt":"hello"}`),
		"whitespace caller":   []byte(`{"client_request_id":"raw-space-caller","caller_session_id":" \t ","prompt":"hello"}`),
		"legacy selected":     []byte(`{"client_request_id":"raw-legacy-selected","selected_session_id":"missing-session","prompt":"hello"}`),
		"legacy parent":       []byte(`{"client_request_id":"raw-legacy-parent","parent_session_id":"parent-session","prompt":"hello"}`),
		"empty selector":      []byte(`{"client_request_id":"raw-empty-selector","prompt":"hello","overrides":{"agent_role":""}}`),
		"whitespace selector": []byte(`{"client_request_id":"raw-space-selector","prompt":"hello","overrides":{"agent_role":" \t "}}`),
	} {
		t.Run(name, func(t *testing.T) {
			resp := callGatewayRaw(t, conn, "raw-invalid-"+name, protocol.MethodRunPrompt, params)
			if resp.Error == nil || resp.Error.Code != protocol.ErrCodeInvalidParams {
				t.Fatalf("raw %s response = %+v, want invalid params", name, resp)
			}
		})
	}
}

func TestGatewaySessionExecutionEnvironmentRejectsExtraRequestField(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	response := callGatewayRaw(
		t,
		conn,
		"session-execution-extra",
		protocol.MethodSessionGetExecutionEnvironment,
		json.RawMessage(`{"session_id":"environment-session","extra":true}`),
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeInvalidParams {
		t.Fatalf("Session execution response = %+v, want invalid params", response)
	}
}

func TestGatewayRunPromptRejectsMixedTypedAndLegacyLaunchFields(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})

	raw := json.RawMessage(`{"client_request_id":"mixed-launch","intent":{"kind":"open_existing","session_id":"target"},"selected_session_id":"legacy","prompt":"hello"}`)
	resp := callGatewayRaw(t, conn, "mixed-launch", protocol.MethodRunPrompt, raw)
	if resp.Error == nil || resp.Error.Code != protocol.ErrCodeInvalidParams {
		t.Fatalf("mixed launch response = %+v, want invalid params", resp)
	}
}

func TestGatewayWorkflowProjectLabelsRoundTrip(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	requireGatewayProjectAttachment(t, conn, "attach-project-labels", &connectionpb.AttachProjectRequest{ProjectId: appCore.ProjectID()})

	var created serverapi.WorkflowProjectLabelCreateResponse
	callGateway(t, conn, "create-project-label", protocol.MethodWorkflowProjectLabelCreate, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: appCore.ProjectID(),
		Name:      "Priority",
	}, &created)
	if created.Label.ID == "" || created.Label.Name != "Priority" {
		t.Fatalf("created label = %+v", created.Label)
	}

	var listed serverapi.WorkflowProjectLabelCatalogResponse
	callGateway(t, conn, "list-project-labels", protocol.MethodWorkflowProjectLabelList, serverapi.WorkflowProjectLabelCatalogRequest{
		ProjectID: appCore.ProjectID(),
	}, &listed)
	if listed.Catalog.ProjectID != appCore.ProjectID() ||
		len(listed.Catalog.Labels) != 1 ||
		listed.Catalog.Labels[0] != created.Label {
		t.Fatalf("listed catalog = %+v, want created label", listed.Catalog)
	}

	duplicate := callGatewayExpectError(t, conn, "duplicate-project-label", protocol.MethodWorkflowProjectLabelCreate, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: appCore.ProjectID(),
		Name:      "priority",
	})
	if duplicate.Code != protocol.ErrCodeWorkflowLabel {
		t.Fatalf("duplicate label error = %+v, want workflow label code", duplicate)
	}
	decoded, ok := serverapi.DecodeWorkflowLabelError(duplicate.Data, duplicate.Message).(*serverapi.WorkflowLabelError)
	if !ok ||
		decoded.Reason != serverapi.WorkflowLabelErrorReasonNameConflict ||
		decoded.ProjectID == nil ||
		*decoded.ProjectID != appCore.ProjectID() {
		t.Fatalf("decoded duplicate label error = %+v", decoded)
	}

	invalidRename := callGatewayExpectError(t, conn, "invalid-project-label-rename", protocol.MethodWorkflowProjectLabelRename, serverapi.WorkflowProjectLabelRenameRequest{
		ProjectID: appCore.ProjectID(),
		LabelID:   created.Label.ID,
		Name:      " ",
	})
	assertWorkflowLabelGatewayError(t, invalidRename, serverapi.WorkflowLabelErrorReasonInvalidName, "name")

	invalidDelete := callGatewayExpectError(t, conn, "invalid-project-label-delete", protocol.MethodWorkflowProjectLabelDelete, serverapi.WorkflowProjectLabelDeleteRequest{
		ProjectID: appCore.ProjectID(),
		LabelID:   "not-a-label-id",
	})
	assertWorkflowLabelGatewayError(t, invalidDelete, serverapi.WorkflowLabelErrorReasonInvalidMutation, "label_id")
}

func TestDecodeAndHandleMapsMalformedWorkflowLabelFilters(t *testing.T) {
	projectID := "project-1"
	limit := 25
	requests := []struct {
		name     string
		response protocol.Response
	}{
		{
			name: "board",
			response: decodeAndHandle[serverapi.WorkflowBoardRequest, struct{}](
				protocol.Request{ID: "invalid-board-filter", Params: mustJSON(t, serverapi.WorkflowBoardRequest{ProjectID: projectID})},
				func(serverapi.WorkflowBoardRequest) (struct{}, error) {
					t.Fatal("invalid board filter reached handler")
					return struct{}{}, nil
				},
			),
		},
		{
			name: "board cards",
			response: decodeAndHandle[serverapi.WorkflowBoardNodeCardsListRequest, struct{}](
				protocol.Request{ID: "invalid-board-card-filter", Params: mustJSON(t, serverapi.WorkflowBoardNodeCardsListRequest{
					ProjectID:  projectID,
					WorkflowID: runtimeids.NewWorkflowID(),
					NodeID:     runtimeids.NewGraphEntityID(),
				})},
				func(serverapi.WorkflowBoardNodeCardsListRequest) (struct{}, error) {
					t.Fatal("invalid board-card filter reached handler")
					return struct{}{}, nil
				},
			),
		},
		{
			name: "task list",
			response: decodeAndHandle[serverapi.WorkflowTaskListRequest, struct{}](
				protocol.Request{ID: "invalid-task-list-filter", Params: mustJSON(t, serverapi.WorkflowTaskListRequest{
					ProjectID: &projectID,
					Limit:     &limit,
				})},
				func(serverapi.WorkflowTaskListRequest) (struct{}, error) {
					t.Fatal("invalid task-list filter reached handler")
					return struct{}{}, nil
				},
			),
		},
	}
	for _, tt := range requests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.response.Error == nil {
				t.Fatal("malformed label filter returned success")
			}
			assertWorkflowLabelGatewayError(t, tt.response.Error, serverapi.WorkflowLabelErrorReasonInvalidFilter, "label_filter.kind")
		})
	}
}

func assertWorkflowLabelGatewayError(t *testing.T, response *protocol.ResponseError, reason serverapi.WorkflowLabelErrorReason, field string) {
	t.Helper()
	if response == nil {
		t.Fatal("workflow label error response is missing")
	}
	if response.Code != protocol.ErrCodeWorkflowLabel {
		t.Fatalf("workflow label error = %+v, want code %d", response, protocol.ErrCodeWorkflowLabel)
	}
	decoded, ok := serverapi.DecodeWorkflowLabelError(response.Data, response.Message).(*serverapi.WorkflowLabelError)
	if !ok || decoded.Reason != reason || decoded.Field == nil || *decoded.Field != field {
		t.Fatalf("decoded workflow label error = %+v, want reason %q field %q", decoded, reason, field)
	}
}

func TestDecodeAndHandlePreservesWorkflowTaskListScopeError(t *testing.T) {
	projectID := "project-1"
	workflowID := runtimeids.NewWorkflowID()
	source := &serverapi.WorkflowTaskListScopeError{
		Reason:     serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked,
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
	}
	response := decodeAndHandle[serverapi.WorkflowTaskListRequest, struct{}](
		protocol.Request{ID: "scope-error", Params: mustJSON(t, serverapi.WorkflowTaskListRequest{
			LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &projectID})},
		func(serverapi.WorkflowTaskListRequest) (struct{}, error) {
			return struct{}{}, source
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowTaskListScope {
		t.Fatalf("response error = %+v, want task-list scope code", response.Error)
	}
	decoded, ok := serverapi.DecodeWorkflowTaskListScopeError(response.Error.Data, response.Error.Message).(*serverapi.WorkflowTaskListScopeError)
	if !ok || decoded.Reason != source.Reason || decoded.ProjectID == nil || *decoded.ProjectID != projectID || decoded.WorkflowID == nil || *decoded.WorkflowID != workflowID {
		t.Fatalf("decoded scope error = %+v, want %+v", decoded, source)
	}
}

func TestDecodeAndHandlePreservesWorkflowTaskCreateSelectionError(t *testing.T) {
	workflowID := runtimeids.NewWorkflowID()
	source := &serverapi.WorkflowTaskCreateSelectionError{
		Reason:     serverapi.WorkflowTaskCreateSelectionReasonWorkflowNotLinked,
		ProjectID:  "project-1",
		WorkflowID: &workflowID,
	}
	response := decodeAndHandle[serverapi.WorkflowTaskCreateRequest, struct{}](
		protocol.Request{ID: "selection-error", Params: mustJSON(t, serverapi.WorkflowTaskCreateRequest{ProjectID: "project-1", Title: "Task"})},
		func(serverapi.WorkflowTaskCreateRequest) (struct{}, error) {
			return struct{}{}, source
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowTaskCreateSelection {
		t.Fatalf("response error = %+v, want task-create selection code", response.Error)
	}
	decoded, ok := serverapi.DecodeWorkflowTaskCreateSelectionError(response.Error.Data, response.Error.Message).(*serverapi.WorkflowTaskCreateSelectionError)
	if !ok ||
		decoded.Reason != source.Reason ||
		decoded.ProjectID != source.ProjectID ||
		decoded.WorkflowID == nil ||
		*decoded.WorkflowID != *source.WorkflowID {
		t.Fatalf("decoded selection error = %+v, want %+v", decoded, source)
	}
}

func TestDecodeAndHandlePreservesWorkflowTaskCreateConflictError(t *testing.T) {
	source := &serverapi.WorkflowTaskCreateConflictError{
		Reason: serverapi.WorkflowTaskCreateConflictReasonSerialization,
	}
	response := decodeAndHandle[serverapi.WorkflowTaskCreateRequest, struct{}](
		protocol.Request{ID: "create-conflict", Params: mustJSON(t, serverapi.WorkflowTaskCreateRequest{ProjectID: "project-1", Title: "Task"})},
		func(serverapi.WorkflowTaskCreateRequest) (struct{}, error) {
			return struct{}{}, source
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowTaskCreateConflict {
		t.Fatalf("response error = %+v, want task-create conflict code", response.Error)
	}
	decoded, ok := serverapi.DecodeWorkflowTaskCreateConflictError(response.Error.Data, response.Error.Message).(*serverapi.WorkflowTaskCreateConflictError)
	if !ok || decoded.Reason != source.Reason {
		t.Fatalf("decoded conflict error = %+v, want %+v", decoded, source)
	}
}

func TestDecodeAndHandlePreservesWorkflowLabelError(t *testing.T) {
	projectID := "project-1"
	source := &serverapi.WorkflowLabelError{
		Reason:    serverapi.WorkflowLabelErrorReasonNameConflict,
		ProjectID: &projectID,
	}
	response := decodeAndHandle[serverapi.WorkflowProjectLabelCreateRequest, struct{}](
		protocol.Request{ID: "label-conflict", Params: mustJSON(t, serverapi.WorkflowProjectLabelCreateRequest{
			ProjectID: "project-1",
			Name:      "Priority",
		})},
		func(serverapi.WorkflowProjectLabelCreateRequest) (struct{}, error) {
			return struct{}{}, source
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowLabel {
		t.Fatalf("response error = %+v, want workflow label code", response.Error)
	}
	decoded, ok := serverapi.DecodeWorkflowLabelError(response.Error.Data, response.Error.Message).(*serverapi.WorkflowLabelError)
	if !ok || !reflect.DeepEqual(decoded, source) {
		t.Fatalf("decoded label error = %+v, want %+v", decoded, source)
	}
}

func TestDecodeAndHandleMapsMalformedTaskLabelMutationToTypedError(t *testing.T) {
	raw101 := make([]string, serverapi.WorkflowLabelMaxIDs+1)
	for index := range raw101 {
		raw101[index] = "not-a-uuid"
	}
	response := decodeAndHandle[serverapi.WorkflowTaskLabelsUpdateRequest, struct{}](
		protocol.Request{ID: "invalid-label-mutation", Params: mustJSON(t, serverapi.WorkflowTaskLabelsUpdateRequest{
			TaskID:      "task-1",
			AddLabelIDs: raw101,
		})},
		func(serverapi.WorkflowTaskLabelsUpdateRequest) (struct{}, error) {
			t.Fatal("handler called for malformed task label mutation")
			return struct{}{}, nil
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowLabel {
		t.Fatalf("response error = %+v, want workflow label code", response.Error)
	}
	decoded, ok := serverapi.DecodeWorkflowLabelError(response.Error.Data, response.Error.Message).(*serverapi.WorkflowLabelError)
	if !ok ||
		decoded.Reason != serverapi.WorkflowLabelErrorReasonInvalidMutation ||
		decoded.Field == nil ||
		*decoded.Field != "add_label_ids" {
		t.Fatalf("decoded mutation error = %+v", decoded)
	}
}

func TestDecodeAndHandlePreservesWorktreeStructuredErrors(t *testing.T) {
	source := &serverapi.WorktreeSelectorError{
		Kind:  serverapi.WorktreeSelectorErrorKindAmbiguous,
		Input: "feature",
		Candidates: []serverapi.WorktreeSelectorCandidate{{
			Variant:          serverapi.WorktreeTopologyVariantRegistered,
			Selector:         "feature-a",
			FallbackIdentity: "c4aaf0cf-4c50-4560-b6a2-6c294d0b1495",
		}},
	}
	response := decodeAndHandle[serverapi.WorktreeSelectorPreviewRequest, struct{}](
		protocol.Request{
			ID:     "worktree-selector-error",
			Params: mustJSON(t, serverapi.WorktreeSelectorPreviewRequest{SessionID: "session", Selector: "feature"}),
		},
		func(serverapi.WorktreeSelectorPreviewRequest) (struct{}, error) {
			return struct{}{}, source
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorktreeSelector {
		t.Fatalf("response error = %+v, want structured worktree selector error", response.Error)
	}
	decoded := serverapi.DecodeWorktreeRPCError(response.Error.Data, response.Error.Message)
	var selector *serverapi.WorktreeSelectorError
	if !errors.As(decoded, &selector) || selector.Kind != source.Kind || selector.Input != source.Input || len(selector.Candidates) != 1 || selector.Candidates[0].FallbackIdentity != source.Candidates[0].FallbackIdentity {
		t.Fatalf("decoded selector error = %+v (%v), want %+v", selector, decoded, source)
	}
}

func TestDecodeAndHandlePreservesWorktreeCreateOwnership(t *testing.T) {
	source := &serverapi.WorktreeCreateError{
		Owner: serverapi.WorktreeCreateErrorOwnerBaseRef,
		Diagnostic: errors.Join(
			errors.New("base ref object disappeared"),
			errors.New("cleanup removed no worktree"),
		).Error(),
	}
	response := decodeAndHandle[serverapi.WorktreeCreateRequest, serverapi.WorktreeCreateResponse](
		protocol.Request{
			ID: "worktree-create-error",
			Params: mustJSON(t, serverapi.WorktreeCreateRequest{
				SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
				SessionID:        "session",
				BaseRef:          "HEAD",
				CreateBranch:     true,
				BranchName:       "feature",
			}),
		},
		func(serverapi.WorktreeCreateRequest) (serverapi.WorktreeCreateResponse, error) {
			return serverapi.WorktreeCreateResponse{}, source
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorktreeCreate {
		t.Fatalf("response error = %+v, want worktree create code", response.Error)
	}
	if !reflect.DeepEqual(response.Error.Data, source.RPCErrorData()) {
		t.Fatalf("response data = %s, want %s", response.Error.Data, source.RPCErrorData())
	}
}

func TestDecodeAndHandleRejectsInvalidWorkflowActionResponse(t *testing.T) {
	response := decodeAndHandle[struct{}, serverapi.WorkflowTaskStartResponse](
		protocol.Request{ID: "invalid-workflow-action-response", Params: mustJSON(t, struct{}{})},
		func(struct{}) (serverapi.WorkflowTaskStartResponse, error) {
			return serverapi.WorkflowTaskStartResponse{}, nil
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeInternalError || len(response.Result) != 0 {
		t.Fatalf("response = %+v, want internal error without result", response)
	}
}

func TestDecodeAndHandleRejectsInvalidWorkflowTaskDetailResponse(t *testing.T) {
	response := decodeAndHandle[struct{}, serverapi.WorkflowTaskGetResponse](
		protocol.Request{ID: "invalid-workflow-task-detail-response", Params: mustJSON(t, struct{}{})},
		func(struct{}) (serverapi.WorkflowTaskGetResponse, error) {
			return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{
				ExecutionTarget: &serverapi.WorkflowExecutionTarget{
					Mode:       serverapi.WorkflowExecutionTargetModeNone,
					Provenance: serverapi.WorkflowExecutionTargetProvenanceLegacyObserved,
				},
			}}, nil
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeInternalError || len(response.Result) != 0 {
		t.Fatalf("response = %+v, want internal error without result", response)
	}
}

func receiveGatewayNotification(t *testing.T, conn *websocket.Conn, method string, label string, out any) {
	t.Helper()
	var notif protocol.Request
	if err := websocket.JSON.Receive(conn, &notif); err != nil {
		t.Fatalf("receive %s: %v", label, err)
	}
	if notif.Method != method {
		t.Fatalf("%s method = %q", label, notif.Method)
	}
	if out != nil {
		if err := json.Unmarshal(notif.Params, out); err != nil {
			t.Fatalf("decode %s params: %v", label, err)
		}
	}
}

func TestGatewayGoalRPCWithoutProjectAttachmentReturnsServiceErrors(t *testing.T) {
	_, server := newUnboundGatewayTestServer(t)
	defer server.Close()
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	for _, tc := range []struct {
		name   string
		method string
		params any
		code   int
	}{
		{name: "show", method: protocol.MethodRuntimeGoalShow, params: serverapi.RuntimeGoalShowRequest{SessionID: "missing-session"}, code: protocol.ErrCodeInternalError},
		{name: "set", method: protocol.MethodRuntimeGoalSet, params: serverapi.RuntimeGoalSetRequest{ClientRequestID: "goal-set", SessionID: "missing-session", Objective: "ship", Actor: "user"}, code: protocol.ErrCodeInternalError},
		{name: "pause", method: protocol.MethodRuntimeGoalPause, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-pause", SessionID: "missing-session", Actor: "user"}, code: protocol.ErrCodeInternalError},
		{name: "resume", method: protocol.MethodRuntimeGoalResume, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-resume", SessionID: "missing-session", Actor: "user"}, code: protocol.ErrCodeInternalError},
		{name: "complete", method: protocol.MethodRuntimeGoalComplete, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-complete", SessionID: "missing-session", Actor: "agent"}, code: protocol.ErrCodeInternalError},
		{name: "clear", method: protocol.MethodRuntimeGoalClear, params: serverapi.RuntimeGoalClearRequest{ClientRequestID: "goal-clear", SessionID: "missing-session", Actor: "user"}, code: protocol.ErrCodeInternalError},
	} {
		err := callGatewayExpectError(t, conn, "goal-"+tc.name, tc.method, tc.params)
		if err.Code != tc.code {
			t.Fatalf("%s error code = %d, want %d", tc.name, err.Code, tc.code)
		}
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return data
}
