package transport

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
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

func TestRouteScopeParamAccessorsCoverScopedRoutes(t *testing.T) {
	for _, route := range rpccontract.Routes() {
		if route.RequestType == nil {
			continue
		}
		params := reflect.New(route.RequestType).Elem().Interface()
		if _, err := routeScopeParamsFor(route, params); err != nil {
			t.Fatalf("route %q scope params: %v", route.Method, err)
		}
	}
}

type routeAccessorKind string

const (
	routeAccessorNone         routeAccessorKind = "none"
	routeAccessorProjectState routeAccessorKind = "active_project"
	routeAccessorSessionID    routeAccessorKind = "SessionID"
	routeAccessorProcessID    routeAccessorKind = "ProcessID"
	routeAccessorOwnerSession routeAccessorKind = "OwnerSessionID"
)

func TestRouteScopeAccessorKindsMatchRouteContract(t *testing.T) {
	expected := map[string]routeAccessorKind{}
	for _, method := range []string{
		protocol.MethodAttachSession,
		protocol.MethodSessionGetMainView,
		protocol.MethodSessionGetTranscriptPage,
		protocol.MethodSessionGetCommittedTranscriptSuffix,
		protocol.MethodSessionGetInitialInput,
		protocol.MethodSessionPersistInputDraft,
		protocol.MethodSessionRetargetWorkspace,
		protocol.MethodSessionResolveTransition,
		protocol.MethodSessionRuntimeActivate,
		protocol.MethodSessionRuntimeRelease,
		protocol.MethodWorktreeList,
		protocol.MethodWorktreeCreateTargetResolve,
		protocol.MethodWorktreeCreate,
		protocol.MethodWorktreeSwitch,
		protocol.MethodWorktreeDelete,
		protocol.MethodRuntimeSetSessionName,
		protocol.MethodRuntimeSetThinkingLevel,
		protocol.MethodRuntimeSetFastModeEnabled,
		protocol.MethodRuntimeSetReviewerEnabled,
		protocol.MethodRuntimeSetAutoCompactionEnabled,
		protocol.MethodRuntimeSetQuestionsEnabled,
		protocol.MethodRuntimeAppendCommittedEntry,
		protocol.MethodRuntimeShouldCompactBeforeUserMessage,
		protocol.MethodRuntimeSubmitUserTurn,
		protocol.MethodRuntimeSubmitUserShellCommand,
		protocol.MethodRuntimeCompactContext,
		protocol.MethodRuntimeCompactContextForPreSubmit,
		protocol.MethodRuntimeHasQueuedUserWork,
		protocol.MethodRuntimeSubmitQueuedUserMessages,
		protocol.MethodRuntimeInterrupt,
		protocol.MethodRuntimeQueueUserMessage,
		protocol.MethodRuntimeDiscardQueuedUserMessage,
		protocol.MethodRuntimeRecordPromptHistory,
		protocol.MethodRuntimeGoalShow,
		protocol.MethodRuntimeGoalSet,
		protocol.MethodRuntimeGoalPause,
		protocol.MethodRuntimeGoalResume,
		protocol.MethodRuntimeGoalComplete,
		protocol.MethodRuntimeGoalClear,
		protocol.MethodAskListPending,
		protocol.MethodAskAnswer,
		protocol.MethodApprovalListPending,
		protocol.MethodApprovalAnswer,
		protocol.MethodSessionSubscribeActivity,
		protocol.MethodPromptSubscribeActivity,
	} {
		expected[method] = routeAccessorSessionID
	}
	for _, method := range []string{
		protocol.MethodProcessGet,
		protocol.MethodProcessKill,
		protocol.MethodProcessInlineOutput,
		protocol.MethodProcessSubscribeOutput,
	} {
		expected[method] = routeAccessorProcessID
	}
	expected[protocol.MethodProcessList] = routeAccessorOwnerSession
	expected[protocol.MethodSessionPlan] = routeAccessorProjectState
	expected[protocol.MethodRunPrompt] = routeAccessorProjectState

	for _, route := range rpccontract.Routes() {
		actual := routeAccessorKindForScope(route.Scope)
		want, ok := expected[route.Method]
		if !ok {
			if actual != routeAccessorNone {
				t.Fatalf("route %q scope %q uses accessor %q but is missing from explicit accessor table", route.Method, route.Scope, actual)
			}
			continue
		}
		if actual != want {
			t.Fatalf("route %q accessor = %q, want %q", route.Method, actual, want)
		}
	}
	for method := range expected {
		if _, ok := rpccontract.RouteByMethod(method); !ok {
			t.Fatalf("accessor table references missing route %q", method)
		}
	}
}

func routeAccessorKindForScope(scope rpccontract.ScopePolicy) routeAccessorKind {
	switch scope {
	case rpccontract.ScopeProjectWorkspace:
		return routeAccessorProjectState
	case rpccontract.ScopeAttachSession,
		rpccontract.ScopeSessionActiveProject,
		rpccontract.ScopeSessionActiveProjectIfSet,
		rpccontract.ScopeSessionAttachedProject,
		rpccontract.ScopeAttachedSession,
		rpccontract.ScopeGoalSession:
		return routeAccessorSessionID
	case rpccontract.ScopeProcessActiveProject:
		return routeAccessorProcessID
	case rpccontract.ScopeProcessListActiveProject:
		return routeAccessorOwnerSession
	default:
		return routeAccessorNone
	}
}

func TestRoutePolicyAuthPolicyHandlesBlankAndUnknownMethods(t *testing.T) {
	executor := newRoutePolicyExecutor(nil)
	if err := executor.requireAuth(context.Background(), ""); err != nil {
		t.Fatalf("blank method auth: %v", err)
	}
	if err := executor.requireAuth(context.Background(), protocol.MethodProjectList); err != nil {
		t.Fatalf("pre-auth method auth: %v", err)
	}
	if err := executor.requireAuth(context.Background(), protocol.MethodProjectAttachWorkspace); !errors.Is(err, serverapi.ErrServerAuthRequired) {
		t.Fatalf("auth-required method error = %v, want server auth required", err)
	}
	if err := executor.requireAuth(context.Background(), "missing.method"); !errors.Is(err, serverapi.ErrServerAuthRequired) {
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
