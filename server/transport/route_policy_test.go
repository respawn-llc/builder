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
	"core/shared/protoapi"
	projectpb "core/shared/protoapi/gen/kent/api/project"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestRoutePolicyAuthPolicyHandlesBlankAndUnknownMethods(t *testing.T) {
	registration, err := productionGatewayRegistration()
	if err != nil {
		t.Fatalf("production Gateway registration: %v", err)
	}
	if err := registration.Validate(); err != nil {
		t.Fatalf("validate production Gateway registration: %v", err)
	}
	executor := newRoutePolicyExecutor(&Gateway{registration: registration})
	if err := executor.requireAuth(context.Background(), nil, ""); err != nil {
		t.Fatalf("blank method auth: %v", err)
	}
	for name, operation := range registration.operations {
		activeIdentity := name
		if route, legacy := registration.legacy[name]; legacy {
			activeIdentity = route.Method
		}
		authErr := executor.requireAuth(context.Background(), nil, activeIdentity)
		requiresServerAuth := operation.Options.AuthenticationStage == sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER
		if requiresServerAuth && !errors.Is(authErr, serverapi.ErrServerAuthRequired) {
			t.Fatalf("auth-required method %q error = %v, want server auth required", activeIdentity, authErr)
		}
		if !requiresServerAuth && authErr != nil {
			t.Fatalf("pre-server method %q auth: %v", activeIdentity, authErr)
		}
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
		{name: "notification", method: protocol.MethodRunPromptProgress, params: serverapi.RunPromptProgress{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := executor.authorizeScope(context.Background(), &connectionState{}, routeForTest(t, tc.method), tc.params); err != nil {
				t.Fatalf("authorize scope: %v", err)
			}
		})
	}
	if err := executor.authorizeScopeFacts(
		context.Background(),
		&connectionState{},
		routeScopePolicy(sharedpb.ScopePolicy_SCOPE_POLICY_PROJECT_VIEW),
		gatewayOperationName(
			t,
			projectpb.File_kent_api_project_project_proto.Services().
				ByName("ProjectCatalogService").Methods().ByName("List"),
		),
		routeScopeParams{},
	); err != nil {
		t.Fatalf("authorize Project view scope: %v", err)
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
	latestFinalRoute := routeForTest(t, protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer)
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, latestFinalRoute, serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("active project own latest final answer: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedProject: fixture.bindingA.ProjectID}, latestFinalRoute, serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: fixture.foreignSessionID}); err == nil {
		t.Fatal("active project foreign latest final answer unexpectedly allowed")
	}
	draftRoute := routeForTest(t, protocol.MethodSessionPersistInputDraft)
	reboundSessionID, err := runtimeids.ParseSessionID(fixture.reboundSessionID)
	if err != nil {
		t.Fatalf("parse rebound Session ID: %v", err)
	}
	draftState := &connectionState{
		attachedProject: fixture.bindingA.ProjectID,
		attachedSession: &reboundSessionID,
	}
	if err := executor.authorizeScope(
		ctx,
		draftState,
		draftRoute,
		serverapi.SessionPersistInputDraftRequest{
			SessionID: fixture.reboundSessionID,
			Input:     "preserved draft",
		},
	); err != nil {
		t.Fatalf("rebind source project draft handoff: %v", err)
	}
	if err := executor.authorizeScope(
		ctx,
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		draftRoute,
		serverapi.SessionPersistInputDraftRequest{
			SessionID: fixture.reboundSessionID,
			Input:     "must remain inaccessible",
		},
	); err == nil {
		t.Fatal("detached source-project draft mutation unexpectedly allowed")
	}
	if err := executor.authorizeScope(
		ctx,
		draftState,
		draftRoute,
		serverapi.SessionPersistInputDraftRequest{
			SessionID: fixture.foreignSessionID,
			Input:     "must remain inaccessible",
		},
	); err == nil {
		t.Fatal("unrelated foreign-project draft mutation unexpectedly allowed")
	}
	typedSessionID, err := runtimeids.ParseSessionID(fixture.ownSessionID)
	if err != nil {
		t.Fatalf("parse execution-environment session ID: %v", err)
	}
	executionEnvironmentRoute := routeForTest(t, protocol.MethodSessionGetExecutionEnvironment)
	if err := executor.authorizeScope(
		ctx,
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		executionEnvironmentRoute,
		serverapi.SessionExecutionEnvironmentRequest{SessionID: typedSessionID},
	); err != nil {
		t.Fatalf("active project own execution environment: %v", err)
	}
	followUpRoute := routeForTest(t, protocol.MethodPromptFollowUpWatch)
	if err := executor.authorizeScope(
		ctx,
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		followUpRoute,
		serverapi.PromptFollowUpWatchRequest{SessionID: typedSessionID},
	); err != nil {
		t.Fatalf("active project own prompt follow-up watch: %v", err)
	}
	if err := executor.authorizeScope(
		ctx,
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		followUpRoute,
		serverapi.PromptFollowUpWatchRequest{SessionID: runtimeids.NewSessionID()},
	); err == nil {
		t.Fatal("active project foreign prompt follow-up watch unexpectedly allowed")
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

	if err := executor.authorizeScopeFacts(
		ctx,
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		rpccontract.ScopeAttachSession,
		"AttachSession",
		routeScopeParams{sessionID: fixture.ownSessionID},
	); err != nil {
		t.Fatalf("attach own session: %v", err)
	}
	if err := executor.authorizeScopeFacts(
		ctx,
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		rpccontract.ScopeAttachSession,
		"AttachSession",
		routeScopeParams{sessionID: fixture.foreignSessionID},
	); err == nil {
		t.Fatal("attach foreign session unexpectedly allowed")
	}
}

func TestRoutePolicyAllowsRuntimeReleaseAfterSessionMovesProjects(t *testing.T) {
	fixture := newRoutePolicyFixture(t)
	// The handler injects the connection-owned runtime owner ID; Project scope
	// must not reject the release before Runtime authority validates that owner.
	err := newRoutePolicyExecutor(fixture.gateway).authorizeScope(
		context.Background(),
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		routeForTest(t, protocol.MethodSessionRuntimeRelease),
		serverapi.SessionRuntimeReleaseRequest{
			Attachment: serverapi.SessionRuntimeAttachment{
				SessionID:  fixture.foreignSessionID,
				Generation: 1,
			},
		},
	)
	if err != nil {
		t.Fatalf("authorize moved Session runtime release: %v", err)
	}
}

func TestRoutePolicyAuthorizesGoalExceptionWithoutWebSocket(t *testing.T) {
	appCore, server := newUnboundGatewayTestServer(t)
	server.Close()
	gateway, err := NewGateway(appCore, gatewayTestIdentity())
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
		SessionID: fixture.ownSessionID,
		Text:      "steer",
	}); err != nil {
		t.Fatalf("live steer root-scoped existing session: %v", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{}, waitRoute, serverapi.RuntimeLiveWaitRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("live wait root-scoped existing session: %v", err)
	}
	missing := "6ff7ace4-e08b-43fc-b425-73242f0b3d26"
	if err := executor.authorizeScope(ctx, &connectionState{}, requiredRoute, serverapi.RuntimeLiveSteerRequest{
		SessionID: missing,
		Text:      "steer",
	}); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("missing required live session error = %v, want ErrRuntimeUnavailable", err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{}, stopRoute, serverapi.RuntimeLiveStopRequest{
		SessionID: missing,
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
	gateway, err := NewGateway(appCore, gatewayTestIdentity())
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

	transcriptRoute := routeForTest(t, protocol.MethodSessionSubscribeTranscript)
	ownSessionID, err := runtimeids.ParseSessionID(fixture.ownSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.authorizeScope(ctx, &connectionState{attachedSession: &ownSessionID}, transcriptRoute, serverapi.TranscriptSubscribeRequest{SessionID: fixture.ownSessionID}); err != nil {
		t.Fatalf("attached transcript subscription: %v", err)
	}
	err = executor.authorizeScope(ctx, &connectionState{attachedSession: &ownSessionID}, transcriptRoute, serverapi.TranscriptSubscribeRequest{SessionID: fixture.foreignSessionID})
	var routeErr gatewayRouteError
	if !errors.As(err, &routeErr) || routeErr.code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("attached transcript mismatch error = %v, want invalid request route error", err)
	}
	questionHistoryRoute := routeForTest(t, protocol.MethodSessionQuestionHistorySubscribe)
	if err := executor.authorizeScope(ctx, &connectionState{attachedSession: &ownSessionID}, questionHistoryRoute, serverapi.QuestionHistorySubscribeRequest{
		SessionID: fixture.ownSessionID, MaxHandoffs: 1,
	}); err != nil {
		t.Fatalf("attached Question-history subscription: %v", err)
	}
	err = executor.authorizeScope(ctx, &connectionState{attachedSession: &ownSessionID}, questionHistoryRoute, serverapi.QuestionHistorySubscribeRequest{
		SessionID: fixture.foreignSessionID, MaxHandoffs: 1,
	})
	if !errors.As(err, &routeErr) || routeErr.code != protocol.ErrCodeInvalidRequest {
		t.Fatalf("attached Question-history mismatch error = %v, want invalid request route error", err)
	}

	projectWorkspaceMethod := sessionlaunchpb.File_kent_api_session_launch_session_launch_proto.Services().
		ByName("SessionLaunchService").Methods().ByName("Plan")
	projectWorkspaceOperation, err := protoapi.OperationFromDescriptor(projectWorkspaceMethod)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.authorizeScopeFacts(
		ctx,
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		routeScopePolicy(projectWorkspaceOperation.Options.ScopePolicy),
		projectWorkspaceOperation.Name,
		routeScopeParams{},
	); err != nil {
		t.Fatalf("project workspace with attached project: %v", err)
	}
	materializationMethod := sessionlaunchpb.File_kent_api_session_launch_session_launch_proto.Services().
		ByName("SessionLaunchService").Methods().ByName("MaterializeWorkspaceChat")
	materializationOperation, err := protoapi.OperationFromDescriptor(materializationMethod)
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.authorizeScopeFacts(
		ctx,
		&connectionState{attachedProject: fixture.bindingA.ProjectID},
		routeScopePolicy(materializationOperation.Options.ScopePolicy),
		materializationOperation.Name,
		routeScopeParams{},
	); err != nil {
		t.Fatalf("workspace Chat materialization with attached project: %v", err)
	}
	workspaceListMethod := worktreepb.File_kent_api_worktree_worktree_proto.Services().
		ByName("ListService").Methods().ByName("ListWorkspace")
	workspaceListOperation, err := protoapi.OperationFromDescriptor(workspaceListMethod)
	if err != nil {
		t.Fatal(err)
	}
	authorizeWorkspaceList := func(request *worktreepb.WorkspaceListRequest) error {
		scopeParams, err := worktreeWorkspaceScope(request)
		if err != nil {
			return err
		}
		return executor.authorizeScopeFacts(
			ctx,
			&connectionState{attachedProject: fixture.bindingA.ProjectID, attachedWorkspaceID: fixture.bindingA.WorkspaceID},
			routeScopePolicy(workspaceListOperation.Options.ScopePolicy),
			workspaceListOperation.Name,
			scopeParams,
		)
	}
	if err := authorizeWorkspaceList(&worktreepb.WorkspaceListRequest{
		ProjectId:   fixture.bindingA.ProjectID,
		WorkspaceId: fixture.bindingA.WorkspaceID,
	}); err != nil {
		t.Fatalf("workspace list with matching project/workspace: %v", err)
	}
	for _, request := range []*worktreepb.WorkspaceListRequest{
		{ProjectId: fixture.bindingB.ProjectID, WorkspaceId: fixture.bindingB.WorkspaceID},
		{ProjectId: fixture.bindingA.ProjectID, WorkspaceId: fixture.bindingB.WorkspaceID},
	} {
		if err := authorizeWorkspaceList(request); err == nil {
			t.Fatalf("foreign workspace list unexpectedly allowed: %+v", request)
		}
	}
	unboundCore, unboundServer := newUnboundGatewayTestServer(t)
	unboundServer.Close()
	unboundGateway, err := NewGateway(unboundCore, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway unbound: %v", err)
	}
	if err := newRoutePolicyExecutor(unboundGateway).authorizeScopeFacts(
		ctx,
		&connectionState{},
		routeScopePolicy(projectWorkspaceOperation.Options.ScopePolicy),
		projectWorkspaceOperation.Name,
		routeScopeParams{},
	); err == nil {
		t.Fatal("project workspace without active project unexpectedly allowed")
	}
	if err := newRoutePolicyExecutor(unboundGateway).authorizeScopeFacts(
		ctx,
		&connectionState{},
		routeScopePolicy(materializationOperation.Options.ScopePolicy),
		materializationOperation.Name,
		routeScopeParams{},
	); err == nil {
		t.Fatal("workspace Chat materialization without active project unexpectedly allowed")
	}
}

type routePolicyFixture struct {
	appCore          *core.Core
	gateway          *Gateway
	bindingA         metadata.Binding
	bindingB         metadata.Binding
	ownSessionID     string
	foreignSessionID string
	reboundSessionID string
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
	foreignStore, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create foreign: %v", err)
	}
	if err := foreignStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable foreign: %v", err)
	}
	reboundStore, err := session.Create(
		filepath.Join(filepath.Join(resolvedB.Config.PersistenceRoot, "projects"), bindingB.ProjectID, "sessions"),
		"workspace-b",
		resolvedB.Config.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create rebound: %v", err)
	}
	if err := reboundStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable rebound: %v", err)
	}
	gateway, err := NewGateway(appCore, gatewayTestIdentity())
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	return routePolicyFixture{
		appCore:          appCore,
		gateway:          gateway,
		bindingA:         bindingA,
		bindingB:         bindingB,
		ownSessionID:     ownStore.Meta().SessionID,
		foreignSessionID: foreignStore.Meta().SessionID,
		reboundSessionID: reboundStore.Meta().SessionID,
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
