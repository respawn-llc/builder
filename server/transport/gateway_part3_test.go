package transport

import (
	"context"
	"core/internal/testharness/testsetup"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/metadata"
	"core/server/session"
	remoteclient "core/shared/client"
	"core/shared/protocol"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"encoding/json"
	"errors"
	"golang.org/x/net/websocket"
	"net/http/httptest"
	"path/filepath"
	"testing"
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
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return httptest.NewServer(gateway.Handler())
}

func createGatewayAuthoritativeSession(t *testing.T, appCore *core.Core) *session.Store {
	t.Helper()
	metadataStore, err := metadata.Open(appCore.Config().PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
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
	callGateway(t, conn, "1", protocol.MethodHandshake, protocol.HandshakeRequest{ProtocolVersion: protocol.Version}, nil)
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

func TestDecodeAndHandlePreservesWorkflowTaskListScopeError(t *testing.T) {
	projectID := "project-1"
	missing := serverapi.WorkflowTaskListScopeDimensionWorkflow
	source := &serverapi.WorkflowTaskListScopeError{
		Kind:         serverapi.WorkflowTaskListScopeErrorKindAmbiguous,
		MissingScope: &missing,
		ProjectIDs:   []string{projectID},
		WorkflowIDs:  []string{"workflow-1", "workflow-2"},
	}
	response := decodeAndHandle[serverapi.WorkflowTaskListRequest, struct{}](
		protocol.Request{ID: "scope-error", Params: mustJSON(t, serverapi.WorkflowTaskListRequest{ProjectID: &projectID})},
		func(serverapi.WorkflowTaskListRequest) (struct{}, error) {
			return struct{}{}, source
		},
	)
	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowTaskListScope {
		t.Fatalf("response error = %+v, want task-list scope code", response.Error)
	}
	decoded, ok := serverapi.DecodeWorkflowTaskListScopeError(response.Error.Data, response.Error.Message).(*serverapi.WorkflowTaskListScopeError)
	if !ok || decoded.Kind != source.Kind || decoded.MissingScope == nil || *decoded.MissingScope != missing || len(decoded.WorkflowIDs) != 2 {
		t.Fatalf("decoded scope error = %+v, want %+v", decoded, source)
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
	blankRoot := " "
	response := decodeAndHandle[struct{}, serverapi.WorkflowTaskGetResponse](
		protocol.Request{ID: "invalid-workflow-task-detail-response", Params: mustJSON(t, struct{}{})},
		func(struct{}) (serverapi.WorkflowTaskGetResponse, error) {
			return serverapi.WorkflowTaskGetResponse{Task: serverapi.WorkflowTaskDetail{
				ExecutionTarget: &serverapi.WorkflowExecutionTarget{
					Mode:          serverapi.WorkflowExecutionTargetModeNone,
					EffectiveRoot: &blankRoot,
					Provenance:    serverapi.WorkflowExecutionTargetProvenanceResolved,
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

func TestGatewayGoalRPCDoesNotRequireProjectAttachment(t *testing.T) {
	_, server := newUnboundGatewayTestServer(t)
	defer server.Close()
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	for _, tc := range []struct {
		name   string
		method string
		params any
	}{
		{name: "show", method: protocol.MethodRuntimeGoalShow, params: serverapi.RuntimeGoalShowRequest{SessionID: "missing-session"}},
		{name: "set", method: protocol.MethodRuntimeGoalSet, params: serverapi.RuntimeGoalSetRequest{ClientRequestID: "goal-set", SessionID: "missing-session", Objective: "ship", Actor: "user"}},
		{name: "pause", method: protocol.MethodRuntimeGoalPause, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-pause", SessionID: "missing-session", Actor: "user"}},
		{name: "resume", method: protocol.MethodRuntimeGoalResume, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-resume", SessionID: "missing-session", Actor: "user"}},
		{name: "complete", method: protocol.MethodRuntimeGoalComplete, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-complete", SessionID: "missing-session", Actor: "agent"}},
		{name: "clear", method: protocol.MethodRuntimeGoalClear, params: serverapi.RuntimeGoalClearRequest{ClientRequestID: "goal-clear", SessionID: "missing-session", Actor: "user"}},
	} {
		err := callGatewayExpectError(t, conn, "goal-"+tc.name, tc.method, tc.params)
		if err.Code != protocol.ErrCodeRuntimeUnavailable {
			t.Fatalf("%s error code = %d message=%q, want runtime unavailable", tc.name, err.Code, err.Message)
		}
		if err.Message == "project attachment is required" {
			t.Fatalf("%s required project attachment", tc.name)
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
