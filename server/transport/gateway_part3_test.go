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
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/net/websocket"
	"net/http/httptest"
	"os"
	"path/filepath"
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
) *authpb.BootstrapStatus {
	t.Helper()
	var result authpb.GetBootstrapStatusResult
	callGatewayDescriptor(t, conn, correlation, gatewayAuthMethod(t, "GetBootstrapStatus"), &emptypb.Empty{}, &result)
	if result.GetSuccess() == nil {
		t.Fatalf("GetBootstrapStatus failed: %+v", result.GetError())
	}
	return result.GetSuccess()
}

func callGatewayAuthCompleteBootstrap(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	request *authpb.CompleteBootstrapRequest,
) *authpb.BootstrapCompletion {
	t.Helper()
	var result authpb.CompleteBootstrapResult
	callGatewayDescriptor(t, conn, correlation, gatewayAuthMethod(t, "CompleteBootstrap"), request, &result)
	if result.GetSuccess() == nil {
		t.Fatalf("CompleteBootstrap failed: %+v", result.GetError())
	}
	return result.GetSuccess()
}

func callGatewayAuthAcknowledgeNoAuth(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
) *authpb.NoAuthAcknowledgement {
	t.Helper()
	var result authpb.AcknowledgeNoAuthResult
	callGatewayDescriptor(t, conn, correlation, gatewayAuthMethod(t, "AcknowledgeNoAuth"), &emptypb.Empty{}, &result)
	if result.GetSuccess() == nil {
		t.Fatalf("AcknowledgeNoAuth failed: %+v", result.GetError())
	}
	return result.GetSuccess()
}

func callGatewayAuthStatus(
	t *testing.T,
	conn *websocket.Conn,
	correlation string,
	request *authpb.GetStatusRequest,
) *authpb.Status {
	t.Helper()
	var result authpb.GetStatusResult
	callGatewayDescriptor(t, conn, correlation, gatewayAuthMethod(t, "GetStatus"), request, &result)
	if result.GetSuccess() == nil {
		t.Fatalf("GetStatus failed: %+v", result.GetError())
	}
	return result.GetSuccess()
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

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return data
}
