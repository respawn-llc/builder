package transport

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/server/core"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

var errRawCustomRouteCalled = errors.New("raw custom-route service called")

type customRouteSessionLaunchService struct {
	rawPlanCalls            int
	trustedPlanCalls        int
	rawDraftCalls           int
	trustedDraftCalls       int
	rawMaterializeCalls     int
	trustedMaterializeCalls int
	lastDraft               serverapi.WorkspaceChatDraftRequest
	sessionPlanResponse     serverapi.SessionPlanResponse
	draftResponse           serverapi.WorkspaceChatDraftResponse
	materializeResponse     serverapi.WorkspaceChatMaterializeResponse
}

type customRouteRunPromptService struct {
	rawCalls     int
	trustedCalls int
}

type customRouteAttentionService struct {
	apicontract.AttentionNotificationService
	rawCalls     int
	trustedCalls int
}

func (s *customRouteAttentionService) SubscribeAttentionNotifications(context.Context, serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	s.rawCalls++
	return nil, errRawCustomRouteCalled
}

func (s *customRouteAttentionService) SubscribeAttentionNotificationsValidated(context.Context, apicontract.Validated[serverapi.AttentionNotificationSubscribeRequest]) (serverapi.AttentionNotificationSubscription, error) {
	s.trustedCalls++
	return &immediateAttentionSubscription{}, nil
}

type immediateAttentionSubscription struct{}

func (*immediateAttentionSubscription) Next(context.Context) (clientui.AttentionNotificationEvent, error) {
	return clientui.AttentionNotificationEvent{}, io.EOF
}

func (*immediateAttentionSubscription) Close() error { return nil }

func (s *customRouteRunPromptService) RunPrompt(context.Context, serverapi.RunPromptRequest, serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	s.rawCalls++
	return serverapi.RunPromptResponse{}, errRawCustomRouteCalled
}

func (s *customRouteRunPromptService) RunPromptValidated(context.Context, apicontract.Validated[serverapi.RunPromptRequest], serverapi.RunPromptProgressSink) (serverapi.RunPromptResponse, error) {
	s.trustedCalls++
	return serverapi.RunPromptResponse{}, nil
}

func (s *customRouteSessionLaunchService) PlanSession(context.Context, serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	s.rawPlanCalls++
	return serverapi.SessionPlanResponse{}, errRawCustomRouteCalled
}

func (s *customRouteSessionLaunchService) PlanSessionValidated(_ context.Context, req apicontract.Validated[serverapi.SessionPlanRequest]) (serverapi.SessionPlanResponse, error) {
	s.trustedPlanCalls++
	return s.sessionPlanResponse, nil
}

func (s *customRouteSessionLaunchService) WorkspaceChatDraft(context.Context, serverapi.WorkspaceChatDraftRequest) (serverapi.WorkspaceChatDraftResponse, error) {
	s.rawDraftCalls++
	return serverapi.WorkspaceChatDraftResponse{}, errRawCustomRouteCalled
}

func (s *customRouteSessionLaunchService) WorkspaceChatDraftValidated(_ context.Context, req apicontract.Validated[serverapi.WorkspaceChatDraftRequest]) (serverapi.WorkspaceChatDraftResponse, error) {
	s.trustedDraftCalls++
	s.lastDraft = req.Value()
	return s.draftResponse, nil
}

func (s *customRouteSessionLaunchService) MaterializeWorkspaceChat(context.Context, serverapi.WorkspaceChatMaterializeRequest) (serverapi.WorkspaceChatMaterializeResponse, error) {
	s.rawMaterializeCalls++
	return serverapi.WorkspaceChatMaterializeResponse{}, errRawCustomRouteCalled
}

func (s *customRouteSessionLaunchService) MaterializeWorkspaceChatValidated(context.Context, apicontract.Validated[serverapi.WorkspaceChatMaterializeRequest]) (serverapi.WorkspaceChatMaterializeResponse, error) {
	s.trustedMaterializeCalls++
	return s.materializeResponse, nil
}

type customRouteSessionViewService struct {
	apicontract.SessionViewService
	rawCalls     int
	trustedCalls int
}

func (s *customRouteSessionViewService) GetSessionExecutionEnvironment(context.Context, serverapi.SessionExecutionEnvironmentRequest) (serverapi.SessionExecutionEnvironmentResponse, error) {
	s.rawCalls++
	return serverapi.SessionExecutionEnvironmentResponse{}, errRawCustomRouteCalled
}

func (s *customRouteSessionViewService) GetSessionMainViewValidated(context.Context, apicontract.Validated[serverapi.SessionMainViewRequest], apicontract.AuthorizedSessionInActiveProject) (serverapi.SessionMainViewResponse, error) {
	return serverapi.SessionMainViewResponse{}, errors.New("unexpected Session Main View call")
}

func (s *customRouteSessionViewService) GetSessionTranscriptPageValidated(context.Context, apicontract.Validated[serverapi.SessionTranscriptPageRequest], apicontract.AuthorizedSessionInActiveProject) (serverapi.SessionTranscriptPageResponse, error) {
	return serverapi.SessionTranscriptPageResponse{}, errors.New("unexpected Session Transcript Page call")
}

func (s *customRouteSessionViewService) GetLatestCommittedAssistantFinalAnswerValidated(context.Context, apicontract.Validated[serverapi.SessionLatestCommittedAssistantFinalAnswerRequest], apicontract.AuthorizedSessionInActiveProject) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, errors.New("unexpected latest final answer call")
}

func (s *customRouteSessionViewService) GetSessionExecutionEnvironmentValidated(context.Context, apicontract.Validated[serverapi.SessionExecutionEnvironmentRequest], apicontract.AuthorizedSessionInActiveProject) (serverapi.SessionExecutionEnvironmentResponse, error) {
	s.trustedCalls++
	return serverapi.SessionExecutionEnvironmentResponse{Environment: serverapi.SessionExecutionEnvironment{
		SessionID: runtimeids.NewSessionID(),
		Workspace: serverapi.UnavailableSessionExecutionWorkspace(serverapi.SessionExecutionWorkspaceUnavailableNotConfigured),
		Branch:    serverapi.UnavailableSessionExecutionBranch(serverapi.SessionExecutionBranchUnavailableNotGitRepository),
		Auth:      serverapi.UnavailableSessionExecutionAuth(serverapi.SessionExecutionAuthUnavailableNotApplicable),
		Model:     serverapi.UnavailableSessionExecutionModel(serverapi.SessionExecutionModelUnavailableNotConfigured),
	}}, nil
}

type customRouteGatewayDependencies struct {
	*core.Core
	launch   apicontract.SessionLaunchService
	view     apicontract.SessionViewService
	run      apicontract.RunPromptService
	workflow apicontract.WorkflowService
}

func (d *customRouteGatewayDependencies) SessionLaunchClientForProjectWorkspace(context.Context, string, string) (apicontract.SessionLaunchService, error) {
	return d.launch, nil
}

func (d *customRouteGatewayDependencies) SessionLaunchClientForProjectWorkspaceID(context.Context, string, string) (apicontract.SessionLaunchService, error) {
	return d.launch, nil
}

func (d *customRouteGatewayDependencies) SessionViewClient() apicontract.SessionViewService {
	return d.view
}

func (d *customRouteGatewayDependencies) RunPromptClientForProjectWorkspace(context.Context, string, string) (apicontract.RunPromptService, error) {
	return d.run, nil
}

func (d *customRouteGatewayDependencies) RunPromptClientForProjectWorkspaceID(context.Context, string, string) (apicontract.RunPromptService, error) {
	return d.run, nil
}

func (d *customRouteGatewayDependencies) WorkflowClient() apicontract.WorkflowService {
	if d.workflow != nil {
		return d.workflow
	}
	return d.Core.WorkflowClient()
}

type rejectingRawWorkflowService struct {
	apicontract.WorkflowService
	apicontract.WorkflowTrustedService
}

type countingWorkflowReadService struct {
	apicontract.WorkflowService
	apicontract.WorkflowTrustedService
	attentionCalls int
	searchCalls    int
}

func (*countingWorkflowReadService) ListWorkflowAttention(context.Context, serverapi.WorkflowAttentionListRequest) (serverapi.WorkflowAttentionListResponse, error) {
	panic("gateway re-entered raw Workflow attention service")
}

func (s *countingWorkflowReadService) ListWorkflowAttentionValidated(_ context.Context, req apicontract.Validated[serverapi.WorkflowAttentionListRequest]) (serverapi.WorkflowAttentionListResponse, error) {
	s.attentionCalls++
	return serverapi.WorkflowAttentionListResponse{NextPageToken: req.Value().PageToken}, nil
}

func (*countingWorkflowReadService) SearchWorkflowTasks(context.Context, serverapi.TaskSearchRequest) (serverapi.TaskSearchResponse, error) {
	panic("gateway re-entered raw Workflow task search service")
}

func (s *countingWorkflowReadService) SearchWorkflowTasksValidated(_ context.Context, req apicontract.Validated[serverapi.TaskSearchRequest]) (serverapi.TaskSearchResponse, error) {
	s.searchCalls++
	return serverapi.TaskSearchResponse{Mode: req.Value().Mode, Groups: []serverapi.TaskSearchGroup{}}, nil
}

func TestGatewayWorkflowTaskMutationsRejectMalformedRequestsBeforeTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	deps := &customRouteGatewayDependencies{
		Core:     appCore,
		workflow: &rejectingRawWorkflowService{},
	}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	for _, method := range []string{
		protocol.MethodWorkflowTaskCreate,
		protocol.MethodWorkflowTaskDependencyAdd,
		protocol.MethodWorkflowTaskDependencyRemove,
		protocol.MethodWorkflowTaskUpdate,
		protocol.MethodWorkflowTaskStart,
		protocol.MethodWorkflowTaskInterrupt,
		protocol.MethodWorkflowTaskResume,
		protocol.MethodWorkflowTaskApprove,
		protocol.MethodWorkflowTaskMovePreview,
		protocol.MethodWorkflowTaskMove,
		protocol.MethodWorkflowTaskComplete,
		protocol.MethodWorkflowTaskDelete,
		protocol.MethodWorkflowTaskObserve,
	} {
		t.Run(method, func(t *testing.T) {
			response := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
				JSONRPC: protocol.JSONRPCVersion,
				ID:      method,
				Method:  method,
				Params:  []byte(`{}`),
			})
			if response.Error == nil || response.Error.Code == protocol.ErrCodeInternalError {
				t.Fatalf("response = %+v, want request validation failure before trusted owner", response)
			}
		})
	}
}

func TestGatewayWorkflowReadModelsRejectMalformedRequestsBeforeTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	deps := &customRouteGatewayDependencies{
		Core:     appCore,
		workflow: &rejectingRawWorkflowService{},
	}
	gateway, err := NewGateway(deps, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	for _, method := range []string{
		protocol.MethodWorkflowProjectLabelList,
		protocol.MethodWorkflowTaskLabelsGet,
		protocol.MethodWorkflowAttentionList,
		protocol.MethodWorkflowTaskAttentionList,
		protocol.MethodWorkflowTaskCommentList,
		protocol.MethodWorkflowTaskActivityList,
		protocol.MethodWorkflowTaskSessionList,
		protocol.MethodWorkflowTaskList,
		protocol.MethodWorkflowTaskSearch,
		protocol.MethodWorkflowBoardGet,
		protocol.MethodWorkflowBoardNodeCardsList,
		protocol.MethodWorkflowTaskGet,
	} {
		t.Run(method, func(t *testing.T) {
			params := []byte(`{}`)
			if method == protocol.MethodWorkflowAttentionList {
				params = []byte(`{"page_size":-1}`)
			}
			response := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
				JSONRPC: protocol.JSONRPCVersion,
				ID:      method,
				Method:  method,
				Params:  params,
			})
			if response.Error == nil || response.Error.Code == protocol.ErrCodeInternalError {
				t.Fatalf("response = %+v, want request validation failure before trusted owner", response)
			}
		})
	}
}

func TestGatewayWorkflowReadModelsValidateOnceBeforeTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	workflowService := &countingWorkflowReadService{}
	gateway, err := NewGateway(&customRouteGatewayDependencies{
		Core:     appCore,
		workflow: workflowService,
	}, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	attention := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "attention",
		Method:  protocol.MethodWorkflowAttentionList,
		Params:  mustJSON(t, serverapi.WorkflowAttentionListRequest{PageSize: 7}),
	})
	if attention.Error != nil {
		t.Fatalf("attention response = %+v", attention)
	}
	if workflowService.attentionCalls != 1 {
		t.Fatalf("attention trusted calls = %d, want 1", workflowService.attentionCalls)
	}

	search := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      apicontract.TaskSearchDedicatedRequestID,
		Method:  protocol.MethodWorkflowTaskSearch,
		Params: mustJSON(t, serverapi.TaskSearchRequest{
			Mode:     serverapi.TaskSearchModeLiteral,
			Query:    "task",
			Context:  serverapi.TaskSearchDefaultContext,
			PageSize: 10,
		}),
	})
	if search.Error != nil {
		t.Fatalf("search response error = %+v", *search.Error)
	}
	if workflowService.searchCalls != 1 {
		t.Fatalf("search trusted calls = %d, want 1", workflowService.searchCalls)
	}
}

func TestCustomRouteDecodersDoNotApplyTopLevelSemantics(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	invalidTheme := serverapi.OnboardingTheme("blue")
	onboarding, err := decodeInboundRequest[serverapi.OnboardingFinalizeRequest](
		gateway,
		mustGatewayRoute(protocol.MethodOnboardingFinalize, apicontract.KindUnary),
		requestDecoderOnboardingFinalize,
		mustJSON(t, serverapi.OnboardingFinalizeRequest{Theme: &invalidTheme}),
	)
	if err != nil {
		t.Fatalf("decode representable Onboarding Finalize request: %v", err)
	}
	if onboarding.Validate() == nil {
		t.Fatal("decoded Onboarding Finalize request unexpectedly passed semantics")
	}

	tests := []struct {
		name   string
		method string
		decode func() error
	}{
		{
			name:   "Run Prompt",
			method: protocol.MethodRunPrompt,
			decode: func() error {
				request, err := decodeInboundRequest[serverapi.RunPromptRequest](
					gateway,
					mustGatewayRoute(protocol.MethodRunPrompt, apicontract.KindProgress),
					requestDecoderDefault,
					[]byte(`{"client_request_id":"","intent":{"kind":"create_new","origin":{"kind":"independent"}},"prompt":"hello"}`),
				)
				if err == nil && request.Validate() == nil {
					return errors.New("decoded request unexpectedly passed semantics")
				}
				return err
			},
		},
		{
			name:   "Session Plan",
			method: protocol.MethodSessionPlan,
			decode: func() error {
				request, err := decodeInboundRequest[serverapi.SessionPlanRequest](
					gateway,
					mustGatewayRoute(protocol.MethodSessionPlan, apicontract.KindUnary),
					requestDecoderDefault,
					[]byte(`{"client_request_id":"","mode":"interactive","intent":{"kind":"create_new","origin":{"kind":"independent"}}}`),
				)
				if err == nil && request.Validate() == nil {
					return errors.New("decoded request unexpectedly passed semantics")
				}
				return err
			},
		},
		{
			name:   "Attach Project",
			method: protocol.MethodAttachProject,
			decode: func() error {
				request, err := decodeInboundRequest[protocol.AttachProjectRequest](
					gateway,
					mustGatewayRoute(protocol.MethodAttachProject, apicontract.KindUnary),
					requestDecoderDefault,
					[]byte(`{"project_id":"","workspace":null}`),
				)
				if err == nil && request.Validate() == nil {
					return errors.New("decoded request unexpectedly passed semantics")
				}
				return err
			},
		},
		{
			name:   "Workspace Chat Draft",
			method: protocol.MethodSessionWorkspaceChatDraft,
			decode: func() error {
				request, err := decodeInboundRequest[serverapi.WorkspaceChatDraftRequest](
					gateway,
					mustGatewayRoute(protocol.MethodSessionWorkspaceChatDraft, apicontract.KindUnary),
					requestDecoderDefault,
					[]byte(`{"operation":{"kind":"update_message"}}`),
				)
				if err == nil && request.Validate() == nil {
					return errors.New("decoded request unexpectedly passed semantics")
				}
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.decode(); err != nil {
				t.Fatalf("%s representation decode: %v", test.method, err)
			}
		})
	}
}

func TestGatewayCustomUnaryRoutesRejectBeforeTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	launch := &customRouteSessionLaunchService{}
	view := &customRouteSessionViewService{}
	gateway, err := NewGateway(&customRouteGatewayDependencies{Core: appCore, launch: launch, view: view}, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	state := &connectionState{handshakeDone: true}

	for _, test := range []struct {
		name   string
		method string
		params []byte
	}{
		{name: "Session Plan", method: protocol.MethodSessionPlan, params: []byte(`{"client_request_id":"","mode":"interactive","intent":{"kind":"create_new","origin":{"kind":"independent"}}}`)},
		{name: "Attach Project", method: protocol.MethodAttachProject, params: []byte(`{"project_id":"","workspace":null}`)},
		{name: "Workspace Chat Draft", method: protocol.MethodSessionWorkspaceChatDraft, params: []byte(`{"operation":{"kind":"unknown"}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := gateway.dispatch(t.Context(), state, protocol.Request{
				JSONRPC: protocol.JSONRPCVersion,
				ID:      test.name,
				Method:  test.method,
				Params:  test.params,
			})
			if response.Error == nil || response.Error.Code != protocol.ErrCodeInvalidParams {
				t.Fatalf("response = %+v, want invalid params", response)
			}
		})
	}
	if launch.trustedPlanCalls != 0 || launch.trustedDraftCalls != 0 || view.trustedCalls != 0 {
		t.Fatalf("invalid requests reached trusted owners: plan=%d draft=%d view=%d", launch.trustedPlanCalls, launch.trustedDraftCalls, view.trustedCalls)
	}
}

func TestGatewaySessionPlanCallsOnlyTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	launch := &customRouteSessionLaunchService{}
	gateway, err := NewGateway(&customRouteGatewayDependencies{Core: appCore, launch: launch}, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	response := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "session-plan",
		Method:  protocol.MethodSessionPlan,
		Params: mustJSON(t, serverapi.SessionPlanRequest{
			ClientRequestID: "plan-1",
			Mode:            serverapi.SessionLaunchModeInteractive,
			Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		}),
	})
	if response.Error != nil {
		t.Fatalf("Session Plan response error = %+v", response.Error)
	}
	if launch.rawPlanCalls != 0 || launch.trustedPlanCalls != 1 {
		t.Fatalf("Session Plan calls raw=%d trusted=%d, want raw=0 trusted=1", launch.rawPlanCalls, launch.trustedPlanCalls)
	}
}

func TestGatewaySessionExecutionEnvironmentCallsOnlyTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	store := createGatewayAuthoritativeSession(t, appCore)
	view := &customRouteSessionViewService{}
	gateway, err := NewGateway(&customRouteGatewayDependencies{Core: appCore, view: view}, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	response := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "execution-environment",
		Method:  protocol.MethodSessionGetExecutionEnvironment,
		Params:  []byte(`{"session_id":"` + store.Meta().SessionID + `"}`),
	})
	if response.Error != nil {
		t.Fatalf("Session Execution Environment response error = %+v", response.Error)
	}
	if view.rawCalls != 0 || view.trustedCalls != 1 {
		t.Fatalf("Session Execution Environment calls raw=%d trusted=%d, want raw=0 trusted=1", view.rawCalls, view.trustedCalls)
	}
}

func TestGatewayWorkspaceChatDraftCallsOnlyTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	launch := &customRouteSessionLaunchService{draftResponse: serverapi.WorkspaceChatDraftResponse{
		GoalAvailability: clientui.GoalAvailabilityAvailable,
	}}
	gateway, err := NewGateway(&customRouteGatewayDependencies{Core: appCore, launch: launch}, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	response := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "draft-read",
		Method:  protocol.MethodSessionWorkspaceChatDraft,
		Params:  []byte(`{"operation":{"kind":"read_message"}}`),
	})
	if response.Error != nil {
		t.Fatalf("Workspace Chat Draft response error = %+v", response.Error)
	}
	if launch.rawDraftCalls != 0 || launch.trustedDraftCalls != 1 {
		t.Fatalf("Workspace Chat Draft calls raw=%d trusted=%d, want raw=0 trusted=1", launch.rawDraftCalls, launch.trustedDraftCalls)
	}
}

func TestGatewayRejectsInvalidTrustedOwnerResponseAtEgress(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	launch := &customRouteSessionLaunchService{}
	gateway, err := NewGateway(&customRouteGatewayDependencies{Core: appCore, launch: launch}, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	response := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "draft-materialize",
		Method:  protocol.MethodSessionWorkspaceChatMaterialize,
		Params:  []byte(`{}`),
	})
	if response.Error == nil || response.Error.Code != protocol.ErrCodeInternalError || len(response.Result) != 0 {
		t.Fatalf("response = %+v, want gateway egress rejection", response)
	}
	if launch.rawMaterializeCalls != 0 || launch.trustedMaterializeCalls != 1 {
		t.Fatalf(
			"Workspace Chat Materialize calls raw=%d trusted=%d, want raw=0 trusted=1",
			launch.rawMaterializeCalls,
			launch.trustedMaterializeCalls,
		)
	}
}

func TestGatewayRunPromptCallsOnlyTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	run := &customRouteRunPromptService{}
	gateway, err := NewGateway(&customRouteGatewayDependencies{Core: appCore, run: run}, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptestServerForGateway(t, gateway)
	t.Cleanup(server.Close)
	conn := dialGateway(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	handshakeGateway(t, conn)

	callGateway(t, conn, "run-prompt", protocol.MethodRunPrompt, serverapi.RunPromptRequest{
		ClientRequestID: "run-1",
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
		Prompt:          "hello",
	}, nil)
	if run.rawCalls != 0 || run.trustedCalls != 1 {
		t.Fatalf("Run Prompt calls raw=%d trusted=%d, want raw=0 trusted=1", run.rawCalls, run.trustedCalls)
	}
}

func TestGatewaySubscriptionCallsOnlyTrustedOwner(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	attention := &customRouteAttentionService{}
	gateway, err := NewGateway(&gatewayAttentionDependencies{Core: appCore, attention: attention}, protocol.ServerIdentity{
		ProtocolVersion: protocol.Version,
		ServerID:        "server-1",
	})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	server := httptestServerForGateway(t, gateway)
	t.Cleanup(server.Close)
	conn := dialGateway(t, server)
	t.Cleanup(func() { _ = conn.Close() })
	handshakeGateway(t, conn)

	callGateway(t, conn, "subscribe-attention", protocol.MethodAttentionNotificationSubscribe, serverapi.AttentionNotificationSubscribeRequest{}, nil)
	var complete protocol.StreamCompleteParams
	receiveGatewayNotification(t, conn, protocol.MethodAttentionNotificationComplete, "attention completion", &complete)
	if attention.rawCalls != 0 || attention.trustedCalls != 1 {
		t.Fatalf("Attention subscription calls raw=%d trusted=%d, want raw=0 trusted=1", attention.rawCalls, attention.trustedCalls)
	}
}

func TestWorkspaceChatDraftInvalidOperationsRejectNetworkAndRawCalls(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	raw := appCore.SessionLaunchClient()
	state := &connectionState{handshakeDone: true}
	message := "forbidden"

	for _, test := range []struct {
		name      string
		operation serverapi.WorkspaceChatDraftOperation
		params    []byte
	}{
		{name: "unknown kind", operation: serverapi.WorkspaceChatDraftOperation{Kind: "unknown"}, params: []byte(`{"operation":{"kind":"unknown"}}`)},
		{name: "forbidden message", operation: serverapi.WorkspaceChatDraftOperation{Kind: serverapi.WorkspaceChatDraftReadMessage, Message: &message}, params: []byte(`{"operation":{"kind":"read_message","message":"forbidden"}}`)},
		{name: "missing update message", operation: serverapi.WorkspaceChatDraftOperation{Kind: serverapi.WorkspaceChatDraftUpdateMessage}, params: []byte(`{"operation":{"kind":"update_message"}}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := gateway.dispatch(t.Context(), state, protocol.Request{
				JSONRPC: protocol.JSONRPCVersion,
				ID:      test.name,
				Method:  protocol.MethodSessionWorkspaceChatDraft,
				Params:  test.params,
			})
			if response.Error == nil || response.Error.Code != protocol.ErrCodeInvalidParams {
				t.Fatalf("network response = %+v, want invalid params", response)
			}
			if _, err := raw.WorkspaceChatDraft(t.Context(), serverapi.WorkspaceChatDraftRequest{Operation: test.operation}); err == nil {
				t.Fatal("direct raw call unexpectedly succeeded")
			}
		})
	}
}

func TestGatewayWorkflowDualValidatorPreservesValidateRPCStructuredError(t *testing.T) {
	appCore, _ := newGatewayTestCore(t, true, true)
	t.Cleanup(func() { _ = appCore.Close() })
	gateway, err := NewGateway(appCore, protocol.ServerIdentity{ProtocolVersion: protocol.Version, ServerID: "server-1"})
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}
	labelID := runtimeids.NewSessionID().String()
	request := serverapi.WorkflowTaskCreateRequest{
		ProjectID: appCore.ProjectID(),
		Title:     "task",
		LabelIDs:  []string{labelID, labelID},
	}
	rpcErr := request.ValidateRPC()
	var labelErr *serverapi.WorkflowLabelError
	if !errors.As(rpcErr, &labelErr) {
		t.Fatalf("ValidateRPC error = %T %v, want WorkflowLabelError", rpcErr, rpcErr)
	}
	var plainErr serverapi.WorkflowRequestValidationError
	if !errors.As(request.Validate(), &plainErr) {
		t.Fatalf("Validate error = %T %v, want WorkflowRequestValidationError", request.Validate(), request.Validate())
	}

	response := gateway.dispatch(t.Context(), &connectionState{handshakeDone: true}, protocol.Request{
		JSONRPC: protocol.JSONRPCVersion,
		ID:      "workflow-dual-validator",
		Method:  protocol.MethodWorkflowTaskCreate,
		Params:  mustJSON(t, request),
	})
	if response.Error == nil || response.Error.Code != labelErr.RPCErrorCode() || len(response.Error.Data) == 0 {
		t.Fatalf("response = %+v, want structured ValidateRPC error code %d", response, labelErr.RPCErrorCode())
	}
}
