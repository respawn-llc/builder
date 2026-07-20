package transport

import (
	"context"
	"core/internal/testharness/testsetup"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
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
		store.Metadata().SessionID,
	)
	if err != nil {
		t.Fatalf("DialRemoteURLForSession: %v", err)
	}
	defer func() { _ = remote.Close() }()
	status, err := remote.GetWorktreeStatus(
		context.Background(),
		serverapi.WorktreeStatusRequest{SessionID: store.Metadata().SessionID},
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
	callGateway(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()}, nil)

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

func TestGatewayRunPromptRejectsMixedTypedAndLegacyLaunchFields(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()}, nil)

	raw := json.RawMessage(`{"client_request_id":"mixed-launch","intent":{"kind":"open_existing","session_id":"target"},"selected_session_id":"legacy","prompt":"hello"}`)
	resp := callGatewayRaw(t, conn, "mixed-launch", protocol.MethodRunPrompt, raw)
	if resp.Error == nil || resp.Error.Code != protocol.ErrCodeInvalidParams {
		t.Fatalf("mixed launch response = %+v, want invalid params", resp)
	}
}

func TestDecodeAndHandlePreservesWorkflowTaskListScopeError(t *testing.T) {
	projectID := "project-1"
	workflowID := "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
	source := &serverapi.WorkflowTaskListScopeError{
		Reason:     serverapi.WorkflowTaskListScopeReasonWorkflowNotLinked,
		ProjectID:  &projectID,
		WorkflowID: &workflowID,
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
	if !ok || decoded.Reason != source.Reason || decoded.ProjectID == nil || *decoded.ProjectID != projectID || decoded.WorkflowID == nil || *decoded.WorkflowID != workflowID {
		t.Fatalf("decoded scope error = %+v, want %+v", decoded, source)
	}
}

func TestDecodeAndHandlePreservesWorkflowTaskCreateSelectionError(t *testing.T) {
	workflowID := "workflow-7e8d24d2-8a98-4dcf-a197-6214db1cb3c0"
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
		{name: "set", method: protocol.MethodRuntimeGoalSet, params: serverapi.RuntimeGoalSetRequest{ClientRequestID: "goal-set", SessionID: "missing-session", Objective: "ship", Actor: "user"}, code: protocol.ErrCodeRuntimeUnavailable},
		{name: "pause", method: protocol.MethodRuntimeGoalPause, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-pause", SessionID: "missing-session", Actor: "user"}, code: protocol.ErrCodeRuntimeUnavailable},
		{name: "resume", method: protocol.MethodRuntimeGoalResume, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-resume", SessionID: "missing-session", Actor: "user"}, code: protocol.ErrCodeRuntimeUnavailable},
		{name: "complete", method: protocol.MethodRuntimeGoalComplete, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "goal-complete", SessionID: "missing-session", Actor: "agent"}, code: protocol.ErrCodeRuntimeUnavailable},
		{name: "clear", method: protocol.MethodRuntimeGoalClear, params: serverapi.RuntimeGoalClearRequest{ClientRequestID: "goal-clear", SessionID: "missing-session", Actor: "user"}, code: protocol.ErrCodeRuntimeUnavailable},
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
