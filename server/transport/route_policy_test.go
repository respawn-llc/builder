package transport

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	serverbootstrap "core/server/bootstrap"
	"core/server/core"
	"core/server/metadata"
	"core/server/session"
	shelltool "core/server/tools/shell"
	rpccontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestRoutePolicyAuthPolicyHandlesBlankAndUnknownMethods(t *testing.T) {
	executor := newRoutePolicyExecutor(nil)
	if err := executor.requireAuth(context.Background(), nil, ""); err != nil {
		t.Fatalf("blank method auth: %v", err)
	}
	if err := executor.requireAuth(context.Background(), nil, protocol.MethodProjectList); err != nil {
		t.Fatalf("pre-auth method auth: %v", err)
	}
	if err := executor.requireAuth(context.Background(), nil, protocol.MethodProjectAttachWorkspace); !errors.Is(err, serverapi.ErrServerAuthRequired) {
		t.Fatalf("auth-required method error = %v, want server auth required", err)
	}
	if err := executor.requireAuth(context.Background(), nil, "missing.method"); !errors.Is(err, serverapi.ErrServerAuthRequired) {
		t.Fatalf("unknown method error = %v, want server auth required", err)
	}
}

func TestRoutePolicyAllowsStatelessScopesWithoutGateway(t *testing.T) {
	executor := routePolicyExecutor{}
	for _, tc := range []struct {
		name   string
		method string
		params any
	}{
		{name: "none", method: protocol.MethodHandshake, params: protocol.HandshakeRequest{}},
		{name: "project view", method: protocol.MethodProjectList, params: serverapi.ProjectListRequest{}},
		{name: "attach project", method: protocol.MethodAttachProject, params: protocol.AttachProjectRequest{}},
		{name: "notification", method: protocol.MethodRunPromptProgress, params: serverapi.RunPromptProgress{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := executor.authorizeScope(context.Background(), &connectionState{}, routeForTest(t, tc.method), tc.params); err != nil {
				t.Fatalf("authorize scope: %v", err)
			}
		})
	}
}

func TestRoutePolicyAuthorizesSessionScopesWithoutWebSocket(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	executor := newRoutePolicyExecutor(fixture.gateway)
	ctx := context.Background()

	activeRoute := routeForTest(t, protocol.MethodSessionGetMainView)
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, activeRoute, serverapi.SessionMainViewRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("active project own session: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, activeRoute, serverapi.SessionMainViewRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("active project foreign session unexpectedly allowed")
	}
	transcriptPageRoute := routeForTest(t, protocol.MethodSessionGetTranscriptPage)
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, transcriptPageRoute, serverapi.SessionTranscriptPageRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("active project own transcript page: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, transcriptPageRoute, serverapi.SessionTranscriptPageRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("active project foreign transcript page unexpectedly allowed")
	}
	transcriptSuffixRoute := routeForTest(t, protocol.MethodSessionGetCommittedTranscriptSuffix)
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, transcriptSuffixRoute, serverapi.SessionCommittedTranscriptSuffixRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("active project own transcript suffix: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, transcriptSuffixRoute, serverapi.SessionCommittedTranscriptSuffixRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("active project foreign transcript suffix unexpectedly allowed")
	}

	attachedRoute := routeForTest(t, protocol.MethodSessionRetargetWorkspace)
	if err := executor.authorizeScope(ctx, &connectionState{}, attachedRoute, serverapi.SessionRetargetWorkspaceRequest{SessionID: fixture.foreignSessionID}); err != nil {
		t.Fatalf("attached-project unscoped session: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, attachedRoute, serverapi.SessionRetargetWorkspaceRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("attached-project foreign session unexpectedly allowed")
	}

	optionalRoute := routeForTest(t, protocol.MethodSessionGetInitialInput)
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, optionalRoute, serverapi.SessionInitialInputRequest{}); err != nil {
		t.Fatalf("optional empty session: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, optionalRoute, serverapi.SessionInitialInputRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("optional foreign session unexpectedly allowed")
	}

	attachSessionRoute := routeForTest(t, protocol.MethodAttachSession)
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, attachSessionRoute, protocol.AttachSessionRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("attach own session: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, attachSessionRoute, protocol.AttachSessionRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("attach foreign session unexpectedly allowed")
	}
}

func TestRoutePolicyAuthorizesGoalExceptionWithoutWebSocket(t *testing.T) {
	appCore, server := newUnboundGatewayTestServer(t)
	server.Close()
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	route := routeForTest(t, protocol.MethodRuntimeGoalShow)
	err = newRoutePolicyExecutor(gateway).authorizeScope(context.Background(), &connectionState{}, route, serverapi.RuntimeGoalShowRequest{SessionID: "missing-session"})
	if err != nil {
		t.Fatalf("unbound goal scope: %v", err)
	}

	fixture := newRoutePolicyFixture(t)
	err = newRoutePolicyExecutor(fixture.gateway).authorizeScope(
		context.Background(),
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		route,
		serverapi.RuntimeGoalShowRequest{SessionID: fixture.foreignSessionID},
	)
	if err == nil {
		t.Fatal("active-project foreign goal scope unexpectedly allowed")
	}
}

func TestRoutePolicyAuthorizesRuntimeLiveControlsWithoutActiveProject(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	executor := newRoutePolicyExecutor(fixture.gateway)
	ctx := context.Background()
	requiredRoute := routeForTest(t, protocol.MethodRuntimeLiveSteer)
	waitRoute := routeForTest(t, protocol.MethodRuntimeLiveWait)
	stopRoute := routeForTest(t, protocol.MethodRuntimeLiveStop)

	if err := executor.authorizeScope(ctx, &connectionState{}, requiredRoute, serverapi.RuntimeLiveSteerRequest{
		ClientRequestID: "8b0364cc-5c6c-412e-a4e8-31380661d1e1",
		SessionID:       fixture.ownSessionID,
		Text:            "steer",
	}); err != nil {
		t.Fatalf("live steer root-scoped existing session: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{}, waitRoute, serverapi.RuntimeLiveWaitRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("live wait root-scoped existing session: %v", err)
	}
	missing := "6ff7ace4-e08b-43fc-b425-73242f0b3d26"
	if err := executor.authorizeScope(ctx, &connectionState{}, requiredRoute, serverapi.RuntimeLiveSteerRequest{
		ClientRequestID: "8b0364cc-5c6c-412e-a4e8-31380661d1e1",
		SessionID:       missing,
		Text:            "steer",
	}); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("missing required live session error = %v, want ErrRuntimeUnavailable", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{}, stopRoute, serverapi.RuntimeLiveStopRequest{
		ClientRequestID: "8b0364cc-5c6c-412e-a4e8-31380661d1e1",
		SessionID:       missing,
	}); err != nil {
		t.Fatalf("optional live stop missing session: %v", err)
	}
}

func TestRoutePolicyAuthorizesProcessScopesWithoutWebSocket(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	fixture.appCore.Background().SetMinimumExecToBgTime(time.Millisecond)
	ctx := context.Background()
	own, err := fixture.appCore.Background().Start(ctx, shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf own\\n; sleep 1"},
		DisplayCommand: "printf own; sleep 1",
		OwnerSessionID: fixture.ownSessionID,
		Workdir:        fixture.appCore.Config().WorkspaceRoot,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start own process: %v", err)
	}
	foreign, err := fixture.appCore.Background().Start(ctx, shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf foreign\\n; sleep 1"},
		DisplayCommand: "printf foreign; sleep 1",
		OwnerSessionID: fixture.foreignSessionID,
		Workdir:        fixture.workspaceB,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start foreign process: %v", err)
	}
	ownerless, err := fixture.appCore.Background().Start(ctx, shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-lc", "printf ownerless\\n; sleep 1"},
		DisplayCommand: "printf ownerless; sleep 1",
		Workdir:        fixture.appCore.Config().WorkspaceRoot,
		YieldTime:      time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start ownerless process: %v", err)
	}

	executor := newRoutePolicyExecutor(fixture.gateway)
	state := &connectionState{attachedProject: fixture.bindingA.ProjectID}
	processRoute := routeForTest(t, protocol.MethodProcessGet)
	if err := executor.authorizeScope(ctx, state, processRoute, serverapi.ProcessGetRequest{ProcessID: own.SessionID}); err != nil {
		t.Fatalf("own process: %v", err)
	}
	if err := executor.authorizeScope(ctx, state, processRoute, serverapi.ProcessGetRequest{ProcessID: foreign.SessionID}); err == nil {
		t.Fatal("foreign process unexpectedly allowed")
	}
	if err := executor.authorizeScope(ctx, state, processRoute, serverapi.ProcessGetRequest{ProcessID: ownerless.SessionID}); err == nil {
		t.Fatal("ownerless process unexpectedly allowed")
	}

	listRoute := routeForTest(t, protocol.MethodProcessList)
	if err := executor.authorizeScope(ctx, state, listRoute, serverapi.ProcessListRequest{}); err != nil {
		t.Fatalf("process list without owner: %v", err)
	}
	if err := executor.authorizeScope(ctx, state, listRoute, serverapi.ProcessListRequest{OwnerSessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("process list own owner: %v", err)
	}
	if err := executor.authorizeScope(ctx, state, listRoute, serverapi.ProcessListRequest{OwnerSessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("process list foreign owner unexpectedly allowed")
	}
}

func TestFilterProcessesForActiveProjectSkipsWhenActiveProjectUnset(t *testing.T) {
	appCore, server := newUnboundGatewayTestServer(t)
	server.Close()
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	filtered, err := gateway.filterProcessesForActiveProject(context.Background(), &connectionState{}, []clientui.BackgroundProcess{{ID: "proc-1", OwnerSessionID: "session-1"}})
	if err != nil {
		t.Fatalf("filter error = %v, want nil", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered processes = %+v, want empty without active project", filtered)
	}
}

func TestFilterProcessesForActiveProjectSkipsStaleOwnerSessions(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	filtered, err := fixture.gateway.filterProcessesForActiveProject(
		context.Background(),
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		[]clientui.BackgroundProcess{{ID: "proc-1", OwnerSessionID: "missing-session"}},
	)
	if err != nil {
		t.Fatalf("filter error = %v, want nil", err)
	}
	if len(filtered) != 0 {
		t.Fatalf("filtered processes = %+v, want empty for stale owner", filtered)
	}
}

func TestRoutePolicyAuthorizesAttachmentAndProjectWorkspaceScopesWithoutWebSocket(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	executor := newRoutePolicyExecutor(fixture.gateway)
	ctx := context.Background()

	attachedRoute := routeForTest(t, protocol.MethodSessionSubscribeActivity)
	if err := executor.authorizeScope(ctx, &connectionState{attachedSession: fixture.ownSessionID}, attachedRoute, serverapi.SessionActivitySubscribeRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("attached session subscription: %v", err)
	}
	err := executor.authorizeScope(ctx, &connectionState{attachedSession: fixture.ownSessionID}, attachedRoute, serverapi.SessionActivitySubscribeRequest{SessionID: fixture.foreignSessionID})
	var routeErr gatewayRouteError
	if !errors.As(err, &routeErr) || routeErr.code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("attached session mismatch error = %v, want invalid request route error", err)
	}
	transcriptRoute := routeForTest(t, protocol.MethodSessionSubscribeTranscript)
	if err := executor.authorizeScope(ctx, &connectionState{attachedSession: fixture.ownSessionID}, transcriptRoute, serverapi.TranscriptSubscribeRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("attached transcript subscription: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedSession: fixture.ownSessionID}, transcriptRoute, serverapi.TranscriptSubscribeRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("attached transcript subscription mismatch unexpectedly allowed")
	}
	promptAttachedRoute := routeForTest(t, protocol.MethodPromptSubscribeActivity)
	if err := executor.authorizeScope(ctx, &connectionState{attachedSession: fixture.ownSessionID}, promptAttachedRoute, serverapi.PromptActivitySubscribeRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("attached prompt subscription: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedSession: fixture.ownSessionID}, promptAttachedRoute, serverapi.PromptActivitySubscribeRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("attached prompt subscription mismatch unexpectedly allowed")
	}

	projectWorkspaceRoute := routeForTest(t, protocol.MethodSessionPlan)
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, projectWorkspaceRoute, serverapi.SessionPlanRequest{}); err != nil {
		t.Fatalf("project workspace with attached project: %v", err)
	}
	unboundCore, unboundServer := newUnboundGatewayTestServer(t)
	unboundServer.Close()
	unboundGateway, err := NewGateway(unboundCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway unbound: %v", err)
	}
	if err := newRoutePolicyExecutor(unboundGateway).authorizeScope(ctx, &connectionState{}, projectWorkspaceRoute, serverapi.SessionPlanRequest{}); err == nil {
		t.Fatal("project workspace without active project unexpectedly allowed")
	}
}

type routePolicyFixture struct {
	appCore          *core.Core
	gateway          *Gateway
	bindingA         metadata.Binding
	ownSessionID     string
	foreignSessionID string
	workspaceB       string
}

func newRoutePolicyFixture(t *testing.T) routePolicyFixture {
	t.Helper()
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	t.Setenv("HOME", home)
	configureGatewayTestServerPort(t)

	resolvedA, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceA})
	if err != nil {
		t.Fatalf("ResolveConfig A: %v", err)
	}
	bindingA, err := metadata.RegisterBinding(context.Background(), resolvedA.Config.PersistenceRoot, resolvedA.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding A: %v", err)
	}
	resolvedB, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspaceB})
	if err != nil {
		t.Fatalf("ResolveConfig B: %v", err)
	}
	bindingB, err := metadata.RegisterBinding(context.Background(), resolvedB.Config.PersistenceRoot, resolvedB.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding B: %v", err)
	}
	metadataStore, err := metadata.Open(resolvedA.Config.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	authSupport := newGatewayTestAuthSupport(t, true)
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolvedA.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	appCore, err := core.New(resolvedA.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("core.New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	ownStore := createGatewayAuthoritativeSession(t, appCore)
	appCore.RegisterSessionStore(ownStore)
	foreignStore, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create foreign: %v", err)
	}
	if err := foreignStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable foreign: %v", err)
	}
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return routePolicyFixture{
		appCore:          appCore,
		gateway:          gateway,
		bindingA:         bindingA,
		ownSessionID:     ownStore.Meta().SessionID,
		foreignSessionID: foreignStore.Meta().SessionID,
		workspaceB:       resolvedB.Config.WorkspaceRoot,
	}
}

func routeForTest(t *testing.T, method string) rpccontract.Route {
	t.Helper()
	route, ok := rpccontract.RouteByMethod(method)
	if !ok {
		t.Fatalf("route %q missing", method)
	}
	return route
}
