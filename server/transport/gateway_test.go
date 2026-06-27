package transport

import (
	"context"
	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/metadata"
	"core/server/session"
	shelltool "core/server/tools/shell"
	rpccontract "core/shared/apicontract"
	remoteclient "core/shared/client"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/serverapi"
	"errors"
	"fmt"
	"golang.org/x/net/websocket"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func registerGatewayWorkspace(t *testing.T, workspace string) {
	t.Helper()
	configureGatewayTestServerPort(t)
	resolved := resolveGatewayTestConfig(t, workspace)
	registerGatewayTestBinding(t, resolved.Config)
}

func configureGatewayTestServerPort(t *testing.T) {
	t.Helper()
	port := 56000 + int(gatewayTestPortCounter.Add(1))
	t.Setenv("KENT_SERVER_HOST", "127.0.0.1")
	t.Setenv("KENT_SERVER_PORT", strconv.Itoa(port))
}

var gatewayTestPortCounter atomic.Uint32

func reportGatewayHandlerError(errs chan<- error, format string, args ...any) {
	select {
	case errs <- fmt.Errorf(format, args...):
	default:
	}
}

func requireNoGatewayHandlerError(t *testing.T, errs <-chan error) {
	t.Helper()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
}

func TestProtocolErrorMapsRuntimeUnavailable(t *testing.T) {
	code, _ := protocolError(serverapi.ErrRuntimeUnavailable)
	if code != protocol.ErrCodeRuntimeUnavailable {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeRuntimeUnavailable)
	}
}

func TestProtocolErrorMapsWorkflowTaskNotFound(t *testing.T) {
	code, _ := protocolError(serverapi.ErrWorkflowTaskNotFound)
	if code != protocol.ErrCodeWorkflowTaskNotFound {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeWorkflowTaskNotFound)
	}
}

func TestProtocolErrorMapsContextCanceled(t *testing.T) {
	code, message := protocolError(context.Canceled)
	if code != protocol.ErrCodeRequestCanceled {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeRequestCanceled)
	}
	if message != canceledByClientMessage {
		t.Fatalf("protocol error message = %q, want %q", message, canceledByClientMessage)
	}
}

func TestStreamCompleteParamsMapsTerminalErrors(t *testing.T) {
	for _, err := range []error{nil, io.EOF, context.Canceled, context.DeadlineExceeded} {
		params := streamCompleteParams(err)
		if params.Code != 0 || params.Message != "" {
			t.Fatalf("streamCompleteParams(%v) = %+v, want empty completion", err, params)
		}
	}
	params := streamCompleteParams(serverapi.ErrStreamFailed)
	if params.Code != protocol.ErrCodeStreamFailed || params.Message != serverapi.ErrStreamFailed.Error() {
		t.Fatalf("streamCompleteParams(stream failed) = %+v, want stream-failed code/message", params)
	}
}

func TestNewGatewayRejectsTypedNilDependencies(t *testing.T) {
	var appCore *core.Core

	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err == nil {
		t.Fatal("expected typed nil dependencies to be rejected")
	}
	if gateway != nil {
		t.Fatalf("gateway = %+v, want nil", gateway)
	}
	if !errors.Is(err, ErrGatewayDependenciesRequired) {
		t.Fatalf("error = %q, want ErrGatewayDependenciesRequired", err.Error())
	}
}

func TestCancellationMessageRoundTripsThroughRemoteClient(t *testing.T) {
	code, message := protocolError(&shelltool.PollingCanceledError{SessionID: "1000", Active: true})
	if code != protocol.ErrCodeRequestCanceled {
		t.Fatalf("protocol error code = %d, want %d", code, protocol.ErrCodeRequestCanceled)
	}

	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(rpcwire.NewWebSocketTransport().Handler(func(ctx context.Context, conn rpcwire.Conn) {
		for event := range conn.Events() {
			if event.Err != nil {
				return
			}
			req := event.Frame.Request()
			switch req.Method {
			case protocol.MethodHandshake:
				resp := protocol.NewSuccessResponse(req.ID, protocol.HandshakeResponse{Identity: protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"}})
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(resp)); err != nil {
					reportGatewayHandlerError(handlerErrs, "send handshake: %w", err)
					return
				}
			case protocol.MethodProjectList:
				resp := protocol.NewErrorResponse(req.ID, code, message)
				if err := conn.Send(ctx, rpcwire.FrameFromResponse(resp)); err != nil {
					reportGatewayHandlerError(handlerErrs, "send project list error: %w", err)
				}
				return
			default:
				reportGatewayHandlerError(handlerErrs, "unexpected method %q", req.Method)
				return
			}
		}
	}))
	defer server.Close()

	remote, err := remoteclient.DialRemoteURL(context.Background(), "ws"+server.URL[len("http"):])
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	defer func() { _ = remote.Close() }()

	_, err = remote.ListProjects(context.Background(), serverapi.ProjectListRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ListProjects error = %v, want context.Canceled", err)
	}
	if err == nil || err.Error() != message {
		t.Fatalf("expected cancellation message %q, got %v", message, err)
	}
	if message == context.Canceled.Error() {
		t.Fatalf("test precondition failed: expected normalized message, got %q", message)
	}
	requireNoGatewayHandlerError(t, handlerErrs)
}

func newGatewayTestAuthSupport(t *testing.T, ready bool) serverbootstrap.AuthSupport {
	t.Helper()
	store := auth.NewMemoryStore(auth.EmptyState())
	authSupport, err := serverbootstrap.BuildAuthSupport(store, nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	if ready {
		if _, err := authSupport.AuthManager.SwitchMethod(context.Background(), auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "test-key"},
		}, true); err != nil {
			t.Fatalf("SwitchMethod: %v", err)
		}
	}
	return authSupport
}

func activateGatewayController(t *testing.T, appCore *core.Core, sessionID string) {
	t.Helper()
	settings := appCore.Config().Settings
	if strings.TrimSpace(settings.Model) == "" {
		settings.Model = "gpt-5"
	}
	if strings.TrimSpace(settings.ProviderOverride) == "" && strings.TrimSpace(settings.OpenAIBaseURL) == "" {
		settings.ProviderOverride = "openai"
	}
	if _, err := appCore.SessionRuntimeClient().ActivateSessionRuntime(context.Background(), serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: "activate-" + strings.TrimSpace(sessionID),
		SessionID:       strings.TrimSpace(sessionID),
		ActiveSettings:  settings,
		Source:          appCore.Config().Source,
	}); err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
}

func releaseGatewayController(t *testing.T, appCore *core.Core, sessionID string) {
	t.Helper()
	if _, err := appCore.SessionRuntimeClient().ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-" + strings.TrimSpace(sessionID),
		SessionID:       strings.TrimSpace(sessionID),
	}); err != nil {
		t.Fatalf("ReleaseSessionRuntime: %v", err)
	}
}

func gatewayRuntimeActivateRequest(appCore *core.Core, sessionID string, requestID string) serverapi.SessionRuntimeActivateRequest {
	settings := appCore.Config().Settings
	if strings.TrimSpace(settings.Model) == "" {
		settings.Model = "gpt-5"
	}
	if strings.TrimSpace(settings.ProviderOverride) == "" && strings.TrimSpace(settings.OpenAIBaseURL) == "" {
		settings.ProviderOverride = "openai"
	}
	return serverapi.SessionRuntimeActivateRequest{
		ClientRequestID: strings.TrimSpace(requestID),
		SessionID:       strings.TrimSpace(sessionID),
		ActiveSettings:  settings,
		Source:          appCore.Config().Source,
	}
}

func waitForGatewayCondition(t *testing.T, label string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", label)
}

type countingSessionRuntimeClient struct {
	remoteclient.SessionRuntimeClient
	releaseCount atomic.Int32
	activateErr  error
}

func (c *countingSessionRuntimeClient) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	if c.activateErr != nil {
		return serverapi.SessionRuntimeActivateResponse{}, c.activateErr
	}
	return c.SessionRuntimeClient.ActivateSessionRuntime(ctx, req)
}

func (c *countingSessionRuntimeClient) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	c.releaseCount.Add(1)
	return c.SessionRuntimeClient.ReleaseSessionRuntime(ctx, req)
}

type gatewayRuntimeClientOverride struct {
	*core.Core
	runtimeClient remoteclient.SessionRuntimeClient
}

func (d *gatewayRuntimeClientOverride) SessionRuntimeClient() remoteclient.SessionRuntimeClient {
	return d.runtimeClient
}

func newGatewayRuntimeClientOverrideServer(t *testing.T, runtimeClient remoteclient.SessionRuntimeClient) (*core.Core, *httptest.Server) {
	t.Helper()
	appCore, _ := newGatewayTestCore(t, true, true)
	gateway, err := NewGateway(&gatewayRuntimeClientOverride{Core: appCore, runtimeClient: runtimeClient}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return appCore, httptest.NewServer(gateway.Handler())
}

func TestGatewayConnectionCloseReleasesOwnedIdleRuntime(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	counter := &countingSessionRuntimeClient{SessionRuntimeClient: appCore.SessionRuntimeClient()}
	gateway, err := NewGateway(&gatewayRuntimeClientOverride{Core: appCore, runtimeClient: counter}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)

	conn := dialGateway(t, server)
	handshakeGateway(t, conn)
	var activation serverapi.SessionRuntimeActivateResponse
	callGateway(t, conn, "activate-runtime", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "activate-runtime"), &activation)
	if err := conn.Close(); err != nil {
		t.Fatalf("close gateway connection: %v", err)
	}
	waitForGatewayCondition(t, "idle owned runtime release on disconnect", func() bool {
		return counter.releaseCount.Load() >= 1
	})
}

func TestGatewayExplicitReleaseRemovesOwnedRuntimeLeaseBeforeConnectionClose(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	counter := &countingSessionRuntimeClient{SessionRuntimeClient: appCore.SessionRuntimeClient()}
	gateway, err := NewGateway(&gatewayRuntimeClientOverride{Core: appCore, runtimeClient: counter}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)

	conn := dialGateway(t, server)
	handshakeGateway(t, conn)
	var activation serverapi.SessionRuntimeActivateResponse
	callGateway(t, conn, "activate-runtime", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "activate-runtime"), &activation)
	var release serverapi.SessionRuntimeReleaseResponse
	callGateway(t, conn, "release-runtime", protocol.MethodSessionRuntimeRelease, serverapi.SessionRuntimeReleaseRequest{
		ClientRequestID: "release-runtime",
		SessionID:       store.Meta().SessionID,
	}, &release)
	if !release.Released {
		t.Fatalf("release response = %+v, want released", release)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close gateway connection: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := counter.releaseCount.Load(); got != 1 {
		t.Fatalf("runtime release call count = %d, want only explicit release", got)
	}
}

func TestGatewayFailedActivationDoesNotRecordOwnedRuntimeLease(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	counter := &countingSessionRuntimeClient{SessionRuntimeClient: appCore.SessionRuntimeClient(), activateErr: errors.New("activation failed before lease")}
	gateway, err := NewGateway(&gatewayRuntimeClientOverride{Core: appCore, runtimeClient: counter}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptest.NewServer(gateway.Handler())
	defer func() { _ = appCore.Close() }()
	defer server.Close()
	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)

	conn := dialGateway(t, server)
	handshakeGateway(t, conn)
	_ = callGatewayExpectError(t, conn, "activate-runtime", protocol.MethodSessionRuntimeActivate, gatewayRuntimeActivateRequest(appCore, store.Meta().SessionID, "activate-runtime"))
	if err := conn.Close(); err != nil {
		t.Fatalf("close gateway connection: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := counter.releaseCount.Load(); got != 0 {
		t.Fatalf("runtime release call count after failed activation = %d, want 0", got)
	}
}

func TestGatewayHandshakeAndProjectList(t *testing.T) {
	appCore, server := newGatewayTestServer(t)
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()

	var handshake protocol.HandshakeResponse
	callGateway(t, conn, "1", protocol.MethodHandshake, protocol.HandshakeRequest{ProtocolVersion: protocol.Version}, &handshake)
	if handshake.Identity.ProtocolVersion != protocol.Version || handshake.Identity.ServerID != "server-1" {
		t.Fatalf("unexpected handshake: %+v", handshake.Identity)
	}

	var projects serverapi.ProjectListResponse
	callGateway(t, conn, "2", protocol.MethodProjectList, serverapi.ProjectListRequest{}, &projects)
	if len(projects.Projects) != 1 || projects.Projects[0].ProjectID != appCore.ProjectID() {
		t.Fatalf("unexpected project list: %+v", projects.Projects)
	}
}

func TestGatewayHandshakeRejectsProtocolVersionMismatch(t *testing.T) {
	_, server := newGatewayTestServer(t)
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()

	respErr := callGatewayExpectError(t, conn, "1", protocol.MethodHandshake, protocol.HandshakeRequest{ProtocolVersion: "1"})
	if respErr.Code != protocol.ErrCodeProtocolVersionMismatch ||
		!strings.Contains(respErr.Message, "unsupported protocol version") ||
		!strings.Contains(respErr.Message, "server requires "+strconv.Quote(protocol.Version)) ||
		!strings.Contains(respErr.Message, "upgrade the older Kent process") {
		t.Fatalf("expected unsupported protocol version error, got %+v", respErr)
	}
}

func TestGatewayRejectsMethodsBeforeHandshake(t *testing.T) {
	_, server := newGatewayTestServer(t)
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()

	respErr := callGatewayExpectError(t, conn, "1", protocol.MethodProjectList, serverapi.ProjectListRequest{})
	if respErr.Code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("expected handshake-required error, got %+v", respErr)
	}
}

func TestGatewayPreAuthMethodPolicy(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		requiresAuth bool
	}{
		{name: "handshake", method: protocol.MethodHandshake, requiresAuth: false},
		{name: "bootstrap status", method: protocol.MethodAuthGetBootstrapStatus, requiresAuth: false},
		{name: "bootstrap complete", method: protocol.MethodAuthCompleteBootstrap, requiresAuth: false},
		{name: "project list", method: protocol.MethodProjectList, requiresAuth: false},
		{name: "project binding plan", method: protocol.MethodProjectPlanWorkspaceBinding, requiresAuth: false},
		{name: "project attach workspace", method: protocol.MethodProjectAttachWorkspace, requiresAuth: true},
		{name: "attach project", method: protocol.MethodAttachProject, requiresAuth: false},
		{name: "attach session", method: protocol.MethodAttachSession, requiresAuth: false},
		{name: "session transcript page", method: protocol.MethodSessionGetTranscriptPage, requiresAuth: false},
		{name: "process list", method: protocol.MethodProcessList, requiresAuth: false},
		{name: "session plan", method: protocol.MethodSessionPlan, requiresAuth: true},
		{name: "persist input draft", method: protocol.MethodSessionPersistInputDraft, requiresAuth: true},
		{name: "run prompt", method: protocol.MethodRunPrompt, requiresAuth: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := newRoutePolicyExecutor(&Gateway{}).requiresServerAuth(tt.method)
			if got != tt.requiresAuth {
				t.Fatalf("requiresServerAuth(%q) = %t, want %t", tt.method, got, tt.requiresAuth)
			}
		})
	}
}

func TestGatewaySubscriptionHandlersCoverRouteContract(t *testing.T) {
	for _, route := range rpccontract.Routes() {
		if route.Kind != rpccontract.KindSubscription {
			continue
		}
		if _, ok := gatewaySubscriptionHandlers[route.Method]; !ok {
			t.Fatalf("subscription route %q missing gateway handler", route.Method)
		}
		if _, ok := gatewaySubscriptionMethods[route.Method]; !ok {
			t.Fatalf("subscription route %q not classified as subscription", route.Method)
		}
	}
	for method := range gatewaySubscriptionHandlers {
		route, ok := rpccontract.RouteByMethod(method)
		if !ok {
			t.Fatalf("gateway subscription handler %q missing route contract", method)
		}
		if route.Kind != rpccontract.KindSubscription {
			t.Fatalf("gateway subscription handler %q route kind = %q, want subscription", method, route.Kind)
		}
	}
}

func TestGatewayProgressHandlersCoverRouteContract(t *testing.T) {
	for _, route := range rpccontract.Routes() {
		if route.Kind != rpccontract.KindProgress {
			continue
		}
		if _, ok := gatewayProgressHandlers[route.Method]; !ok {
			t.Fatalf("progress route %q missing gateway handler", route.Method)
		}
	}
	for method := range gatewayProgressHandlers {
		route, ok := rpccontract.RouteByMethod(method)
		if !ok {
			t.Fatalf("gateway progress handler %q missing route contract", method)
		}
		if route.Kind != rpccontract.KindProgress {
			t.Fatalf("gateway progress handler %q route kind = %q, want progress", method, route.Kind)
		}
	}
}

func TestGatewayStreamRoutesUseRouteContractMethods(t *testing.T) {
	runPromptRoute, ok := rpccontract.RouteByMethod(protocol.MethodRunPrompt)
	if !ok {
		t.Fatal("run prompt route missing")
	}
	if _, ok := gatewayProgressHandlers[runPromptRoute.Method]; !ok {
		t.Fatal("run prompt progress handler missing")
	}
	if runPromptRoute.EventMethod != protocol.MethodRunPromptProgress {
		t.Fatalf("run prompt event method = %q, want %q", runPromptRoute.EventMethod, protocol.MethodRunPromptProgress)
	}
	for _, tc := range []struct {
		method       string
		eventMethod  string
		completeName string
	}{
		{method: protocol.MethodSessionSubscribeActivity, eventMethod: protocol.MethodSessionActivityEvent, completeName: protocol.MethodSessionActivityComplete},
		{method: protocol.MethodProcessSubscribeOutput, eventMethod: protocol.MethodProcessOutputEvent, completeName: protocol.MethodProcessOutputComplete},
		{method: protocol.MethodPromptSubscribeActivity, eventMethod: protocol.MethodPromptActivityEvent, completeName: protocol.MethodPromptActivityComplete},
		{method: protocol.MethodWorkflowSubscribe, eventMethod: protocol.MethodWorkflowEvent, completeName: protocol.MethodWorkflowComplete},
		{method: protocol.MethodWorkflowSubscribeProject, eventMethod: protocol.MethodWorkflowProjectEvent, completeName: protocol.MethodWorkflowProjectComplete},
	} {
		route, ok := rpccontract.RouteByMethod(tc.method)
		if !ok {
			t.Fatalf("subscription route %q missing", tc.method)
		}
		if _, ok := gatewaySubscriptionHandlers[route.Method]; !ok {
			t.Fatalf("subscription handler %q missing", route.Method)
		}
		if route.EventMethod != tc.eventMethod || route.CompleteMethod != tc.completeName {
			t.Fatalf("route %q stream methods = event %q complete %q, want event %q complete %q", tc.method, route.EventMethod, route.CompleteMethod, tc.eventMethod, tc.completeName)
		}
	}
}

func TestGatewayAuthBootstrapStatusAllowedBeforeAttach(t *testing.T) {
	appCore, server, _ := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	var status serverapi.AuthGetBootstrapStatusResponse
	callGateway(t, conn, "status-1", protocol.MethodAuthGetBootstrapStatus, serverapi.AuthGetBootstrapStatusRequest{}, &status)
	if status.AuthReady {
		t.Fatal("expected unauthenticated bootstrap status")
	}
	if !status.AuthBootstrapSupported {
		t.Fatal("expected auth bootstrap to be supported")
	}
	if !containsString(status.AllowedPreAuthMethods, protocol.MethodProjectList) {
		t.Fatalf("allowed pre-auth methods = %+v, want %q", status.AllowedPreAuthMethods, protocol.MethodProjectList)
	}
	if !containsString(status.AllowedPreAuthMethods, protocol.MethodAuthCompleteBootstrap) {
		t.Fatalf("allowed pre-auth methods = %+v, want %q", status.AllowedPreAuthMethods, protocol.MethodAuthCompleteBootstrap)
	}
	if !sameStringSet(status.AllowedPreAuthMethods, rpccontract.AllowedPreAuthMethods()) {
		t.Fatalf("allowed pre-auth methods = %+v, want %+v", status.AllowedPreAuthMethods, rpccontract.AllowedPreAuthMethods())
	}
}

func TestGatewayAuthBootstrapAPIKeyCompletionEnablesAuthRequiredMethods(t *testing.T) {
	appCore, server, authSupport := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	callGateway(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()}, nil)
	if respErr := callGatewayExpectError(t, conn, "run-1", protocol.MethodRunPrompt, serverapi.RunPromptRequest{}); respErr.Code != protocol.ErrCodeAuthRequired {
		t.Fatalf("run.prompt error = %+v, want auth required", respErr)
	}

	callGateway(t, conn, "complete-1", protocol.MethodAuthCompleteBootstrap, serverapi.AuthCompleteBootstrapRequest{
		Mode:   serverapi.AuthBootstrapModeAPIKey,
		APIKey: "server-key",
	}, nil)
	var status serverapi.AuthGetBootstrapStatusResponse
	callGateway(t, conn, "status-2", protocol.MethodAuthGetBootstrapStatus, serverapi.AuthGetBootstrapStatusRequest{}, &status)
	if !status.AuthReady {
		t.Fatal("expected bootstrap completion to configure server auth")
	}
	state, err := authSupport.AuthManager.StoredState(context.Background())
	if err != nil {
		t.Fatalf("StoredState: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "server-key" {
		t.Fatalf("unexpected stored auth method: %+v", state.Method)
	}

	var secondComplete serverapi.AuthCompleteBootstrapResponse
	callGateway(t, conn, "complete-2", protocol.MethodAuthCompleteBootstrap, serverapi.AuthCompleteBootstrapRequest{Mode: serverapi.AuthBootstrapModeAPIKey, APIKey: "server-key-2"}, &secondComplete)
	if !secondComplete.AuthReady || secondComplete.MethodType != string(auth.MethodAPIKey) {
		t.Fatalf("unexpected second auth.completeBootstrap result: %+v", secondComplete)
	}
	state, err = authSupport.AuthManager.StoredState(context.Background())
	if err != nil {
		t.Fatalf("StoredState after second complete: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "server-key" {
		t.Fatalf("unexpected stored auth method after retry: %+v", state.Method)
	}
}

func TestGatewayRejectsProjectWorkspaceMutationBeforeServerAuthReady(t *testing.T) {
	appCore, server, _ := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()}, nil)

	if respErr := callGatewayExpectError(t, conn, "attach-workspace", protocol.MethodProjectAttachWorkspace, serverapi.ProjectAttachWorkspaceRequest{ProjectID: appCore.ProjectID(), WorkspaceRoot: "/tmp/workspace"}); respErr.Code != protocol.ErrCodeAuthRequired {
		t.Fatalf("project.attachWorkspace error = %+v, want auth required", respErr)
	}
}

func TestGatewayRejectsSessionActivitySubscriptionBeforeServerAuthReady(t *testing.T) {
	appCore, server, _ := newGatewayTestServerWithAuth(t, false)
	defer func() { _ = appCore.Close() }()
	store := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(store)
	defer server.Close()

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)

	callGateway(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: appCore.ProjectID()}, nil)
	callGateway(t, conn, "attach-session", protocol.MethodAttachSession, protocol.AttachSessionRequest{SessionID: store.Meta().SessionID}, nil)
	if respErr := callGatewayExpectError(t, conn, "subscribe", protocol.MethodSessionSubscribeActivity, serverapi.SessionActivitySubscribeRequest{SessionID: store.Meta().SessionID}); respErr.Code != protocol.ErrCodeAuthRequired {
		t.Fatalf("session activity subscribe error = %+v, want auth required", respErr)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, item := range left {
		counts[item]++
	}
	for _, item := range right {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func TestGatewayRejectsSessionAccessOutsideAttachedProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	foreignSession, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot,
		metadataStore.SessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create foreign: %v", err)
	}
	if err := foreignSession.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable foreign: %v", err)
	}
	if _, err := metadataStore.ResolveSessionExecutionTarget(context.Background(), foreignSession.Meta().SessionID); err != nil {
		t.Fatalf("ResolveSessionExecutionTarget precondition: %v", err)
	}
	record, err := metadataStore.ResolvePersistedSession(context.Background(), foreignSession.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession precondition: %v", err)
	}
	opened, err := session.Open(record.SessionDir, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Open precondition: %v", err)
	}
	_ = opened

	_, server := newGatewayTestServerForConfig(t, resolvedA.Config)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], bindingA.ProjectID)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	if _, err := remote.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: foreignSession.Meta().SessionID}); err == nil {
		t.Fatal("expected foreign-project session view access to be rejected")
	}
	if _, err := remote.PersistInputDraft(context.Background(), serverapi.SessionPersistInputDraftRequest{ClientRequestID: "persist-foreign", SessionID: foreignSession.Meta().SessionID, Input: "should fail"}); err == nil {
		t.Fatal("expected foreign-project session mutation to be rejected")
	}
	if _, err := remote.RetargetSessionWorkspace(context.Background(), serverapi.SessionRetargetWorkspaceRequest{ClientRequestID: "retarget-foreign", SessionID: foreignSession.Meta().SessionID, WorkspaceRoot: resolvedA.Config.WorkspaceRoot}); err == nil {
		t.Fatal("expected foreign-project session retarget to be rejected")
	}
	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-project-for-foreign-goal-checks", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: bindingA.ProjectID}, nil)
	assertForeignGoalAccessRejected(t, conn, foreignSession.Meta().SessionID)
	if bindingA.ProjectID == bindingB.ProjectID {
		t.Fatalf("expected distinct project ids, both=%q", bindingA.ProjectID)
	}
}

func assertForeignGoalAccessRejected(t *testing.T, conn *websocket.Conn, sessionID string) {
	t.Helper()
	wantMessage := "session " + strconv.Quote(sessionID) + " not available"
	for _, tc := range []struct {
		name   string
		method string
		params any
	}{
		{name: "show", method: protocol.MethodRuntimeGoalShow, params: serverapi.RuntimeGoalShowRequest{SessionID: sessionID}},
		{name: "set", method: protocol.MethodRuntimeGoalSet, params: serverapi.RuntimeGoalSetRequest{ClientRequestID: "foreign-goal-set", SessionID: sessionID, Objective: "ship", Actor: "user"}},
		{name: "pause", method: protocol.MethodRuntimeGoalPause, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "foreign-goal-pause", SessionID: sessionID, Actor: "user"}},
		{name: "resume", method: protocol.MethodRuntimeGoalResume, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "foreign-goal-resume", SessionID: sessionID, Actor: "user"}},
		{name: "complete", method: protocol.MethodRuntimeGoalComplete, params: serverapi.RuntimeGoalStatusRequest{ClientRequestID: "foreign-goal-complete", SessionID: sessionID, Actor: "agent"}},
		{name: "clear", method: protocol.MethodRuntimeGoalClear, params: serverapi.RuntimeGoalClearRequest{ClientRequestID: "foreign-goal-clear", SessionID: sessionID, Actor: "user"}},
	} {
		err := callGatewayExpectError(t, conn, "foreign-goal-"+tc.name, tc.method, tc.params)
		if err.Code != protocol.ErrCodeInternalError || err.Message != wantMessage {
			t.Fatalf("foreign goal %s error = code %d message %q, want code %d message %q", tc.name, err.Code, err.Message, protocol.ErrCodeInternalError, wantMessage)
		}
	}
}

func TestGatewayAllowsUnscopedSessionRetargetOutsideServerDefaultProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	defer func() { _ = metadataStore.Close() }()
	foreignSession, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create foreign: %v", err)
	}
	if err := foreignSession.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable foreign: %v", err)
	}

	authSupport := newGatewayTestAuthSupport(t, true)
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolvedA.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	defer func() { _ = runtimeSupport.Background.Close() }()
	appCore, err := core.New(resolvedA.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	defer func() { _ = appCore.Close() }()
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	if err := gateway.requireSessionInAttachedProject(context.Background(), &connectionState{}, foreignSession.Meta().SessionID); err != nil {
		t.Fatalf("requireSessionInAttachedProject unscoped: %v", err)
	}
	if err := gateway.requireSessionInAttachedProject(context.Background(), &connectionState{attachedProject: bindingA.ProjectID}, foreignSession.Meta().SessionID); err == nil {
		t.Fatal("expected attached project scope to reject foreign session retarget")
	}
}

func TestGatewayAllowsOptionalSessionLifecycleRequestsWithoutSessionID(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	registerGatewayWorkspace(t, workspace)

	resolved := resolveGatewayTestConfig(t, workspace)
	binding, err := metadata.ResolveBinding(context.Background(), resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("ResolveBinding: %v", err)
	}
	_, server := newGatewayTestServerForConfig(t, resolved.Config)

	remote, err := remoteclient.DialRemoteURLForProject(context.Background(), "ws"+server.URL[len("http"):], binding.ProjectID)
	if err != nil {
		t.Fatalf("DialRemoteURLForProject: %v", err)
	}
	defer func() { _ = remote.Close() }()

	initialInput, err := remote.GetInitialInput(context.Background(), serverapi.SessionInitialInputRequest{TransitionInput: "draft text"})
	if err != nil {
		t.Fatalf("GetInitialInput: %v", err)
	}
	if initialInput.Input != "draft text" {
		t.Fatalf("initial input = %q, want draft text", initialInput.Input)
	}

	resolvedTransition, err := remote.ResolveTransition(context.Background(), serverapi.SessionResolveTransitionRequest{
		ClientRequestID: "new-session-no-current-session",
		Transition: serverapi.SessionTransition{
			Action:        "new_session",
			InitialPrompt: "hello",
		},
	})
	if err != nil {
		t.Fatalf("ResolveTransition: %v", err)
	}
	if !resolvedTransition.ShouldContinue || !resolvedTransition.ForceNewSession {
		t.Fatalf("unexpected transition response: %+v", resolvedTransition)
	}
	if resolvedTransition.InitialPrompt != "hello" {
		t.Fatalf("initial prompt = %q, want hello", resolvedTransition.InitialPrompt)
	}
}

func TestGatewayProjectReattachClearsStaleSessionAttachment(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	bindingB := registerGatewayTestBinding(t, resolvedB.Config)

	appCore, server := newGatewayTestServerForConfig(t, resolvedA.Config)
	storeA := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(storeA)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	callGateway(t, conn, "attach-project-a", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: bindingA.ProjectID}, nil)
	callGateway(t, conn, "attach-session-a", protocol.MethodAttachSession, protocol.AttachSessionRequest{SessionID: storeA.Meta().SessionID}, nil)
	callGateway(t, conn, "attach-project-b", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: bindingB.ProjectID}, nil)

	if respErr := callGatewayExpectError(t, conn, "subscribe", protocol.MethodSessionSubscribeActivity, serverapi.SessionActivitySubscribeRequest{SessionID: storeA.Meta().SessionID}); respErr.Code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("expected session-attach-required error after project reattach, got %+v", respErr)
	}
}

func TestGatewayRejectsAttachProjectWorkspaceOutsideProject(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA := resolveGatewayTestConfig(t, workspaceA)
	bindingA := registerGatewayTestBinding(t, resolvedA.Config)
	resolvedB := resolveGatewayTestConfig(t, workspaceB)
	registerGatewayTestBinding(t, resolvedB.Config)

	_, server := newGatewayTestServerForConfig(t, resolvedA.Config)

	conn := dialGateway(t, server)
	defer func() { _ = conn.Close() }()
	handshakeGateway(t, conn)
	respErr := callGatewayExpectError(t, conn, "attach-project", protocol.MethodAttachProject, protocol.AttachProjectRequest{ProjectID: bindingA.ProjectID, WorkspaceRoot: resolvedB.Config.WorkspaceRoot})
	if !strings.Contains(respErr.Message, "not bound to project") {
		t.Fatalf("expected workspace/project mismatch error, got %+v", respErr)
	}
}
