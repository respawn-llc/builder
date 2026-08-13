package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"core/server/metadata"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type noAuthorizationFacts struct{}

type scopeAuthorizationRule string

const (
	scopeAuthorizationNoCheck                     scopeAuthorizationRule = "no scope check"
	scopeAuthorizationAttachProjectOwner          scopeAuthorizationRule = "Attach Project owner resolves and attaches the requested Project"
	scopeAuthorizationAttachSessionFact           scopeAuthorizationRule = "Attach Session resolves one typed Session attachment fact"
	scopeAuthorizationProjectViewCheck            scopeAuthorizationRule = "Project View owner checks Project access"
	scopeAuthorizationProjectWorkspaceCheck       scopeAuthorizationRule = "active Project attachment check"
	scopeAuthorizationProjectWorkspaceBindingFact scopeAuthorizationRule = "Project and Workspace binding resolves one typed authorization fact"
	scopeAuthorizationSessionActiveProjectFact    scopeAuthorizationRule = "Session membership resolves one typed active-Project fact"
	scopeAuthorizationOptionalSessionFact         scopeAuthorizationRule = "optional Session membership resolves a closed absent-or-active-Project fact"
	scopeAuthorizationAttachedProjectConstraint   scopeAuthorizationRule = "Session retarget carries the attached-Project constraint to its owner"
	scopeAuthorizationAttachedSessionCheck        scopeAuthorizationRule = "connection attached-Session identity check"
	scopeAuthorizationGoalSessionCheck            scopeAuthorizationRule = "Goal Session current-Project check"
	scopeAuthorizationRuntimeLiveOwner            scopeAuthorizationRule = "Runtime Live owner handles optional Session admission"
	scopeAuthorizationRequiredLiveNoCheck         scopeAuthorizationRule = "Runtime Live owner is the sole required-live authority"
	scopeAuthorizationProcessActiveProjectFact    scopeAuthorizationRule = "Process ownership resolves one typed active-Project fact"
	scopeAuthorizationProcessListCheck            scopeAuthorizationRule = "optional Process owner Session active-Project check"
	scopeAuthorizationOutboundNotification        scopeAuthorizationRule = "outbound notification has no inbound executable"
)

type scopeAuthorizationClassification struct {
	rule              scopeAuthorizationRule
	authorizationType reflect.Type
}

type inboundHandlerOwner string

const (
	inboundHandlerLegacyRaw        inboundHandlerOwner = "legacy raw service"
	inboundHandlerTrustedOwner     inboundHandlerOwner = "trusted owner"
	inboundHandlerRuntimeLiveOwner inboundHandlerOwner = "Runtime Live authority"
	inboundHandlerOutboundOnly     inboundHandlerOwner = "outbound only"
)

type inboundOwnerRecheck string

const (
	inboundOwnerRecheckNone                inboundOwnerRecheck = "none"
	inboundOwnerRecheckWorktreePostLease   inboundOwnerRecheck = "Worktree post-lease execution target"
	inboundOwnerRecheckRetargetMaintenance inboundOwnerRecheck = "Session retarget maintenance and transaction state"
)

type inboundHandlerClassification struct {
	owner   inboundHandlerOwner
	recheck inboundOwnerRecheck
}

type inboundRouteClassification struct {
	authorization scopeAuthorizationClassification
	handler       inboundHandlerClassification
}

var inboundScopeAuthorizationClassifications = map[apicontract.ScopePolicy]scopeAuthorizationClassification{
	apicontract.ScopeNone:                       {rule: scopeAuthorizationNoCheck, authorizationType: reflect.TypeOf(noAuthorizationFacts{})},
	apicontract.ScopeAttachProject:              {rule: scopeAuthorizationAttachProjectOwner, authorizationType: reflect.TypeOf(noAuthorizationFacts{})},
	apicontract.ScopeAttachSession:              {rule: scopeAuthorizationAttachSessionFact, authorizationType: reflect.TypeOf(apicontract.AuthorizedSessionAttachment{})},
	apicontract.ScopeProjectView:                {rule: scopeAuthorizationProjectViewCheck, authorizationType: reflect.TypeOf(noAuthorizationFacts{})},
	apicontract.ScopeProjectWorkspace:           {rule: scopeAuthorizationProjectWorkspaceCheck, authorizationType: reflect.TypeOf(noAuthorizationFacts{})},
	apicontract.ScopeProjectWorkspaceBinding:    {rule: scopeAuthorizationProjectWorkspaceBindingFact, authorizationType: reflect.TypeOf(apicontract.AuthorizedProjectWorkspaceBinding{})},
	apicontract.ScopeSessionActiveProject:       {rule: scopeAuthorizationSessionActiveProjectFact, authorizationType: reflect.TypeOf(apicontract.AuthorizedSessionInActiveProject{})},
	apicontract.ScopeSessionActiveProjectIfSet:  {rule: scopeAuthorizationOptionalSessionFact, authorizationType: reflect.TypeOf(apicontract.OptionalAuthorizedSessionInActiveProject{})},
	apicontract.ScopeSessionAttachedProject:     {rule: scopeAuthorizationAttachedProjectConstraint, authorizationType: reflect.TypeOf(apicontract.AttachedProjectConstraint{})},
	apicontract.ScopeAttachedSession:            {rule: scopeAuthorizationAttachedSessionCheck, authorizationType: reflect.TypeOf(noAuthorizationFacts{})},
	apicontract.ScopeGoalSession:                {rule: scopeAuthorizationGoalSessionCheck, authorizationType: reflect.TypeOf(noAuthorizationFacts{})},
	apicontract.ScopeRuntimeLiveSessionOptional: {rule: scopeAuthorizationRuntimeLiveOwner, authorizationType: reflect.TypeOf(noAuthorizationFacts{})},
	apicontract.ScopeProcessActiveProject:       {rule: scopeAuthorizationProcessActiveProjectFact, authorizationType: reflect.TypeOf(apicontract.AuthorizedProcessInActiveProject{})},
	apicontract.ScopeProcessListActiveProject:   {rule: scopeAuthorizationProcessListCheck, authorizationType: reflect.TypeOf(noAuthorizationFacts{})},
	apicontract.ScopeNotification:               {rule: scopeAuthorizationOutboundNotification},
}

func inboundRouteClassificationForRoute(route apicontract.Route) inboundRouteClassification {
	authorization, declared := inboundScopeAuthorizationClassifications[route.Scope]
	if !declared {
		panic(fmt.Sprintf("route %q scope %q has no authorization classification", route.Method, route.Scope))
	}
	handler := inboundHandlerClassification{owner: inboundHandlerLegacyRaw, recheck: inboundOwnerRecheckNone}
	switch route.Method {
	case protocol.MethodRuntimeLiveSteer, protocol.MethodRuntimeLiveWait:
		authorization = scopeAuthorizationClassification{
			rule:              scopeAuthorizationRequiredLiveNoCheck,
			authorizationType: reflect.TypeOf(noAuthorizationFacts{}),
		}
		handler.owner = inboundHandlerRuntimeLiveOwner
	case protocol.MethodRuntimeLiveStop, protocol.MethodRuntimeLiveWatch:
		handler.owner = inboundHandlerRuntimeLiveOwner
	case protocol.MethodAttachSession,
		protocol.MethodAuthGetBootstrapStatus,
		protocol.MethodAuthCompleteBootstrap,
		protocol.MethodAuthAcknowledgeNoAuth,
		protocol.MethodAuthGetStatus,
		protocol.MethodCapabilityFactsGet,
		protocol.MethodPromptCommandCatalogGet,
		protocol.MethodProjectList,
		protocol.MethodProjectHomeList,
		protocol.MethodProjectResolvePath,
		protocol.MethodProjectPlanWorkspaceBinding,
		protocol.MethodProjectCreate,
		protocol.MethodProjectEditGet,
		protocol.MethodProjectUpdate,
		protocol.MethodProjectSetDefaultWorkspace,
		protocol.MethodProjectWorkspaceList,
		protocol.MethodProjectWorkspaceGet,
		protocol.MethodProjectUnlinkWorkspace,
		protocol.MethodProjectDelete,
		protocol.MethodProjectAttachWorkspace,
		protocol.MethodProjectRebindWorkspace,
		protocol.MethodProjectGetOverview,
		protocol.MethodSessionPage,
		protocol.MethodPromptAnswerBatch,
		protocol.MethodServerReadinessGet,
		protocol.MethodServerUpdateStatusGet,
		protocol.MethodSessionRetargetWorkspace,
		protocol.MethodSessionGetMainView,
		protocol.MethodSessionGetTranscriptPage,
		protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer,
		protocol.MethodSessionGetExecutionEnvironment,
		protocol.MethodSessionGetInitialInput,
		protocol.MethodSessionPersistInputDraft,
		protocol.MethodSessionResolveTransition,
		protocol.MethodSessionRuntimeActivate,
		protocol.MethodSessionRuntimeRelease,
		protocol.MethodWorktreeWorkspaceList,
		protocol.MethodWorktreeStatus,
		protocol.MethodWorktreeList,
		protocol.MethodWorktreeSelectorResolve,
		protocol.MethodWorktreeDeletePreview,
		protocol.MethodWorktreeCreateTargetResolve,
		protocol.MethodWorktreeCreate,
		protocol.MethodWorktreeEnter,
		protocol.MethodWorktreeLeave,
		protocol.MethodWorktreeDelete,
		protocol.MethodProcessGet,
		protocol.MethodProcessKill,
		protocol.MethodProcessInlineOutput,
		protocol.MethodProcessSubscribeOutput,
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
		protocol.MethodRuntimeInterrupt,
		protocol.MethodRuntimeDiscardQueuedUserMessage,
		protocol.MethodRuntimeRecordPromptHistory:
		handler.owner = inboundHandlerTrustedOwner
	case protocol.MethodWorkflowCreate,
		protocol.MethodWorkflowCreateAndLinkProject,
		protocol.MethodWorkflowUpdate,
		protocol.MethodWorkflowList,
		protocol.MethodWorkflowGet,
		protocol.MethodWorkflowLinkProject,
		protocol.MethodWorkflowListProjectLinks,
		protocol.MethodWorkflowSetDefaultProjectLink,
		protocol.MethodWorkflowUnlinkProject,
		protocol.MethodWorkflowDeletePreview,
		protocol.MethodWorkflowDelete,
		protocol.MethodWorkflowValidate,
		protocol.MethodWorkflowScriptPathValidate,
		protocol.MethodWorkflowGraphValidateDraft,
		protocol.MethodWorkflowGraphDeriveWiring,
		protocol.MethodWorkflowGraphSavePreview,
		protocol.MethodWorkflowGraphSave,
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
		protocol.MethodWorkflowTaskGet:
		handler.owner = inboundHandlerTrustedOwner
	case protocol.MethodRuntimeGoalShow,
		protocol.MethodRuntimeGoalSet,
		protocol.MethodRuntimeGoalPause,
		protocol.MethodRuntimeGoalResume,
		protocol.MethodRuntimeGoalComplete,
		protocol.MethodRuntimeGoalClear:
		handler.owner = inboundHandlerTrustedOwner
	case protocol.MethodRunPrompt,
		protocol.MethodSessionPlan,
		protocol.MethodSessionWorkspaceChatDraft,
		protocol.MethodSessionWorkspaceChatMaterialize,
		protocol.MethodAttentionNotificationSubscribe,
		protocol.MethodPromptFollowUpWatch:
		handler.owner = inboundHandlerTrustedOwner
	}
	switch route.Method {
	case protocol.MethodWorktreeCreate,
		protocol.MethodWorktreeEnter,
		protocol.MethodWorktreeLeave,
		protocol.MethodWorktreeDelete:
		handler.recheck = inboundOwnerRecheckWorktreePostLease
	case protocol.MethodSessionRetargetWorkspace:
		handler.recheck = inboundOwnerRecheckRetargetMaintenance
	}
	if route.Kind == apicontract.KindNotification {
		handler = inboundHandlerClassification{owner: inboundHandlerOutboundOnly, recheck: inboundOwnerRecheckNone}
	}
	return inboundRouteClassification{authorization: authorization, handler: handler}
}

func inboundAuthorizationClassificationForRoute(route apicontract.Route) scopeAuthorizationClassification {
	return inboundRouteClassificationForRoute(route).authorization
}

func inboundHandlerClassificationForRoute(route apicontract.Route) inboundHandlerClassification {
	return inboundRouteClassificationForRoute(route).handler
}

type requestValidatorKind = apicontract.ValidationMethod

const requestValidatorNone = apicontract.ValidationMethodNone

type requestDecoderKind uint8

const (
	requestDecoderDefault requestDecoderKind = iota
	requestDecoderOnboardingFinalize
	requestDecoderSessionExecutionEnvironment
)

type inboundExecutableRoute struct {
	route                 apicontract.Route
	requestType           reflect.Type
	authorizationType     reflect.Type
	authorizationRule     scopeAuthorizationRule
	handlerClassification inboundHandlerClassification
	validation            apicontract.ValidationPolicy
	validator             requestValidatorKind
	decoder               requestDecoderKind
	executeUnary          func(*Gateway, context.Context, *connectionState, protocol.Request) protocol.Response
	executeProgress       gatewayProgressHandler
	executeSubscription   gatewaySubscriptionHandler
}

type erasedInboundRequest struct {
	value             any
	validated         any
	authorizationType reflect.Type
}

func inboundUnary[Req any, Authz any](
	method string,
	policy apicontract.ValidationPolicy,
	decoder requestDecoderKind,
	prepare func(Req, *connectionState) Req,
	authorize func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (Authz, error),
) inboundExecutableRoute {
	route := mustInboundRoute(method, apicontract.KindUnary)
	if prepare == nil {
		prepare = func(req Req, _ *connectionState) Req { return req }
	}
	executable := inboundExecutableRoute{
		route:                 route,
		requestType:           reflect.TypeOf((*Req)(nil)).Elem(),
		authorizationType:     reflect.TypeOf((*Authz)(nil)).Elem(),
		authorizationRule:     inboundAuthorizationClassificationForRoute(route).rule,
		handlerClassification: inboundHandlerClassificationForRoute(route),
		validation:            policy,
		validator:             route.ValidationMethod,
		decoder:               decoder,
	}
	executable.executeUnary = func(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
		return executeInboundUnary(g, ctx, state, wire, route, policy, decoder, prepare, authorize)
	}
	return executable
}

func inboundTrustedUnary[Req any, Authz any, Resp any](
	method string,
	policy apicontract.ValidationPolicy,
	decoder requestDecoderKind,
	prepare func(Req, *connectionState) Req,
	authorize func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (Authz, error),
	handle func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req], Authz) (Resp, error),
) inboundExecutableRoute {
	route := mustInboundRoute(method, apicontract.KindUnary)
	if prepare == nil {
		prepare = func(req Req, _ *connectionState) Req { return req }
	}
	executable := inboundExecutableRoute{
		route:                 route,
		requestType:           reflect.TypeOf((*Req)(nil)).Elem(),
		authorizationType:     reflect.TypeOf((*Authz)(nil)).Elem(),
		authorizationRule:     inboundAuthorizationClassificationForRoute(route).rule,
		handlerClassification: inboundHandlerClassificationForRoute(route),
		validation:            policy,
		validator:             route.ValidationMethod,
		decoder:               decoder,
	}
	executable.executeUnary = func(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
		request, err := decodeInboundRequest[Req](g, route, decoder, wire.Params)
		if err != nil {
			return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
		prepared := prepare(request, state)
		response, err := apicontract.WithValidated(prepared, policy, func(validated apicontract.Validated[Req]) (Resp, error) {
			authz, err := authorize(ctx, g, state, validated)
			if err != nil {
				var zero Resp
				return zero, validatedOwnerError{cause: err}
			}
			response, err := handle(ctx, g, state, validated, authz)
			if err != nil {
				var zero Resp
				return zero, validatedOwnerError{cause: err}
			}
			return response, nil
		})
		if err != nil {
			return responseForValidationOrOwnerError(wire.ID, err)
		}
		return handlerSuccessResponse(wire.ID, response)
	}
	return executable
}

func trustedServiceUnary[Req any, Resp any, Trusted any](
	method string,
	client func(GatewayDependencies) any,
	call func(Trusted, context.Context, apicontract.Validated[Req]) (Resp, error),
) inboundExecutableRoute {
	route := mustInboundRoute(method, apicontract.KindUnary)
	policy := apicontract.SemanticValidationRequired
	if route.ValidationMethod == apicontract.ValidationMethodNone {
		policy = apicontract.NoSemanticValidation
	}
	return inboundTrustedUnary(
		method,
		policy,
		requestDecoderDefault,
		nil,
		authorizeRouteScope[Req](route),
		func(ctx context.Context, g *Gateway, _ *connectionState, req apicontract.Validated[Req], _ noAuthorizationFacts) (Resp, error) {
			trusted, ok := client(g.deps).(Trusted)
			if !ok {
				var zero Resp
				return zero, fmt.Errorf("%s trusted service is required", route.Dependency)
			}
			return call(trusted, ctx, req)
		},
	)
}

func trustedServiceUnaryNoResponse[Req any, Trusted any](
	method string,
	client func(GatewayDependencies) any,
	call func(Trusted, context.Context, apicontract.Validated[Req]) error,
) inboundExecutableRoute {
	return trustedServiceUnary(
		method,
		client,
		func(trusted Trusted, ctx context.Context, req apicontract.Validated[Req]) (struct{}, error) {
			return struct{}{}, call(trusted, ctx, req)
		},
	)
}

func handlePromptAnswerBatch(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	req apicontract.Validated[serverapi.PromptAnswerBatchRequest],
	_ apicontract.AuthorizedSessionInActiveProject,
) (serverapi.PromptAnswerBatchResponse, error) {
	trusted, ok := g.deps.PromptControlClient().(apicontract.PromptControlTrustedService)
	if !ok {
		return serverapi.PromptAnswerBatchResponse{}, errors.New("Prompt Control trusted service is required")
	}
	return trusted.AnswerPromptBatchValidated(ctx, req)
}

func executeInboundUnary[Req any, Authz any](
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	wire protocol.Request,
	route apicontract.Route,
	policy apicontract.ValidationPolicy,
	decoder requestDecoderKind,
	prepare func(Req, *connectionState) Req,
	authorize func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (Authz, error),
) protocol.Response {
	decoded, err := decodeInboundRequest[Req](g, route, decoder, wire.Params)
	if err != nil {
		return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
	}
	decoded = prepare(decoded, state)
	response, err := apicontract.WithValidated(decoded, policy, func(validated apicontract.Validated[Req]) (protocol.Response, error) {
		authz, err := authorize(ctx, g, state, validated)
		if err != nil {
			return protocol.Response{}, validatedOwnerError{cause: err}
		}
		return executeLegacyUnary(g, ctx, state, wire, validated.Value(), authz), nil
	})
	if err != nil {
		return responseForValidationOrOwnerError(wire.ID, err)
	}
	return response
}

func inboundProgress[Req any, Authz any](
	method string,
	policy apicontract.ValidationPolicy,
	decoder requestDecoderKind,
	prepare func(Req, *connectionState) Req,
	authorize func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (Authz, error),
) inboundExecutableRoute {
	return inboundMetadata[Req, Authz](method, apicontract.KindProgress, policy, decoder, prepare, authorize)
}

func inboundSubscription[Req any, Authz any](
	method string,
	policy apicontract.ValidationPolicy,
	decoder requestDecoderKind,
	prepare func(Req, *connectionState) Req,
	authorize func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (Authz, error),
) inboundExecutableRoute {
	return inboundMetadata[Req, Authz](method, apicontract.KindSubscription, policy, decoder, prepare, authorize)
}

func inboundMetadata[Req any, Authz any](
	method string,
	kind apicontract.Kind,
	policy apicontract.ValidationPolicy,
	decoder requestDecoderKind,
	prepare func(Req, *connectionState) Req,
	authorize func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (Authz, error),
) inboundExecutableRoute {
	route := mustInboundRoute(method, kind)
	return inboundExecutableRoute{
		route:                 route,
		requestType:           reflect.TypeOf((*Req)(nil)).Elem(),
		authorizationType:     reflect.TypeOf((*Authz)(nil)).Elem(),
		authorizationRule:     inboundAuthorizationClassificationForRoute(route).rule,
		handlerClassification: inboundHandlerClassificationForRoute(route),
		validation:            policy,
		validator:             route.ValidationMethod,
		decoder:               decoder,
	}
}

func decodeInboundRequest[Req any](g *Gateway, route apicontract.Route, decoder requestDecoderKind, raw json.RawMessage) (Req, error) {
	var decoded any
	var err error
	switch decoder {
	case requestDecoderDefault:
		decoded, err = route.DecodeRequest(raw)
	case requestDecoderOnboardingFinalize:
		decoded, err = g.onboardingFinalizeRequestContract.Decode(raw)
	case requestDecoderSessionExecutionEnvironment:
		decoded, err = g.sessionExecutionRequestContract.Decode(raw)
	default:
		panic(fmt.Sprintf("unsupported inbound decoder %d", decoder))
	}
	if err != nil {
		var zero Req
		return zero, fmt.Errorf("decode params: %w", err)
	}
	request, ok := decoded.(Req)
	if !ok {
		panic(fmt.Sprintf("route %q decoder returned %T, want %v", route.Method, decoded, reflect.TypeOf((*Req)(nil)).Elem()))
	}
	return request, nil
}

func mustInboundRoute(method string, kind apicontract.Kind) apicontract.Route {
	route, ok := apicontract.RouteByMethod(method)
	if !ok {
		panic(fmt.Sprintf("inbound executable route %q has no shared metadata", method))
	}
	if route.Kind != kind {
		panic(fmt.Sprintf("inbound executable route %q kind = %q, want %q", method, route.Kind, kind))
	}
	return route
}

func authorizeRouteScope[Req any](
	route apicontract.Route,
) func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (noAuthorizationFacts, error) {
	return func(ctx context.Context, g *Gateway, state *connectionState, validated apicontract.Validated[Req]) (noAuthorizationFacts, error) {
		if err := newRoutePolicyExecutor(g).authorizeScope(ctx, state, route, validated.Value()); err != nil {
			return noAuthorizationFacts{}, err
		}
		return noAuthorizationFacts{}, nil
	}
}

func authorizeProjectWorkspaceBinding(
	ctx context.Context,
	g *Gateway,
	state *connectionState,
	validated apicontract.Validated[serverapi.WorktreeWorkspaceListRequest],
) (apicontract.AuthorizedProjectWorkspaceBinding, error) {
	req := validated.Value()
	activeProjectID, err := g.activeProjectID(ctx, state)
	if err != nil {
		return apicontract.AuthorizedProjectWorkspaceBinding{}, err
	}
	if strings.TrimSpace(req.ProjectID) != strings.TrimSpace(activeProjectID) ||
		strings.TrimSpace(state.attachedWorkspaceID) != strings.TrimSpace(req.WorkspaceID) {
		return apicontract.AuthorizedProjectWorkspaceBinding{}, serverapi.ErrWorkspaceNotRegistered
	}
	binding, err := g.deps.MetadataStore().LookupWorkspaceBindingByID(ctx, req.WorkspaceID)
	if err != nil {
		return apicontract.AuthorizedProjectWorkspaceBinding{}, err
	}
	if strings.TrimSpace(binding.ProjectID) != strings.TrimSpace(activeProjectID) {
		return apicontract.AuthorizedProjectWorkspaceBinding{}, serverapi.ErrWorkspaceNotRegistered
	}
	return apicontract.AuthorizedProjectWorkspaceBinding{
		ProjectID:     binding.ProjectID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: binding.CanonicalRoot,
	}, nil
}

func authorizeProcessActiveProject[Req any](
	processID func(Req) string,
) func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (apicontract.AuthorizedProcessInActiveProject, error) {
	return func(ctx context.Context, g *Gateway, state *connectionState, validated apicontract.Validated[Req]) (apicontract.AuthorizedProcessInActiveProject, error) {
		resolver, ok := g.deps.ProcessViewClient().(apicontract.ProcessViewTrustedService)
		if !ok {
			return apicontract.AuthorizedProcessInActiveProject{}, errors.New("Process View trusted service is required")
		}
		candidate, err := resolver.ResolveProcessAuthorization(ctx, processID(validated.Value()))
		if err != nil {
			return apicontract.AuthorizedProcessInActiveProject{}, err
		}
		if strings.TrimSpace(candidate.OwnerSessionID) == "" {
			return apicontract.AuthorizedProcessInActiveProject{}, fmt.Errorf("process %q not available", candidate.ProcessID)
		}
		if err := g.requireSessionInActiveProject(ctx, state, candidate.OwnerSessionID); err != nil {
			return apicontract.AuthorizedProcessInActiveProject{}, err
		}
		return apicontract.AuthorizedProcessInActiveProject{
			ProcessID:      candidate.ProcessID,
			OwnerSessionID: candidate.OwnerSessionID,
			Process:        candidate.Process,
		}, nil
	}
}

func authorizeSessionActiveProject[Req any](
	sessionID func(Req) string,
) func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (apicontract.AuthorizedSessionInActiveProject, error) {
	return func(ctx context.Context, g *Gateway, state *connectionState, validated apicontract.Validated[Req]) (apicontract.AuthorizedSessionInActiveProject, error) {
		activeProjectID, err := g.activeProjectID(ctx, state)
		if err != nil {
			return apicontract.AuthorizedSessionInActiveProject{}, err
		}
		metadataStore := g.deps.MetadataStore()
		if metadataStore == nil {
			return apicontract.AuthorizedSessionInActiveProject{}, errors.New("metadata store is required")
		}
		resolved, err := metadataStore.ResolveActiveProjectSession(ctx, sessionID(validated.Value()))
		if err != nil {
			return apicontract.AuthorizedSessionInActiveProject{}, err
		}
		if resolved.OwningProjectID != strings.TrimSpace(activeProjectID) {
			return apicontract.AuthorizedSessionInActiveProject{}, sessionOutsideActiveProjectError{
				sessionID: resolved.SessionID.String(),
			}
		}
		return apicontract.AuthorizedSessionInActiveProject{
			SessionID:       resolved.SessionID,
			ActiveProjectID: strings.TrimSpace(activeProjectID),
			OwningProjectID: resolved.OwningProjectID,
			ExecutionTarget: resolved.ExecutionTarget,
		}, nil
	}
}

func authorizeOptionalSessionActiveProject[Req any](
	sessionID func(Req) string,
) func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (apicontract.OptionalAuthorizedSessionInActiveProject, error) {
	required := authorizeSessionActiveProject(sessionID)
	return func(ctx context.Context, g *Gateway, state *connectionState, validated apicontract.Validated[Req]) (apicontract.OptionalAuthorizedSessionInActiveProject, error) {
		if strings.TrimSpace(sessionID(validated.Value())) == "" {
			return apicontract.AbsentAuthorizedSessionInActiveProject(), nil
		}
		authorization, err := required(ctx, g, state, validated)
		if err != nil {
			return apicontract.OptionalAuthorizedSessionInActiveProject{}, err
		}
		return apicontract.PresentAuthorizedSessionInActiveProject(authorization), nil
	}
}

func authorizeSessionAttachment(
	ctx context.Context,
	g *Gateway,
	state *connectionState,
	validated apicontract.Validated[protocol.AttachSessionRequest],
) (apicontract.AuthorizedSessionAttachment, error) {
	request := validated.Value()
	activeProjectID := strings.TrimSpace(g.deps.ProjectID())
	if state != nil && strings.TrimSpace(state.attachedProject) != "" {
		activeProjectID = strings.TrimSpace(state.attachedProject)
	}
	return resolveAuthorizedSessionAttachment(ctx, g.deps.MetadataStore(), activeProjectID, request.SessionID)
}

func attachedProjectConstraint(
	_ context.Context,
	_ *Gateway,
	state *connectionState,
	_ apicontract.Validated[serverapi.SessionRetargetWorkspaceRequest],
) (apicontract.AttachedProjectConstraint, error) {
	if state == nil || strings.TrimSpace(state.attachedProject) == "" {
		return apicontract.AbsentAttachedProjectConstraint(), nil
	}
	return apicontract.PresentAttachedProjectConstraint(state.attachedProject), nil
}

type sessionAttachmentMetadata interface {
	ResolveSessionExecutionTarget(context.Context, string) (clientui.SessionExecutionTarget, error)
	LookupWorkspaceBindingByID(context.Context, string) (metadata.Binding, error)
}

func resolveAuthorizedSessionAttachment(
	ctx context.Context,
	metadataStore sessionAttachmentMetadata,
	activeProjectID string,
	rawSessionID string,
) (apicontract.AuthorizedSessionAttachment, error) {
	sessionID, err := runtimeids.ParseSessionID(rawSessionID)
	if err != nil {
		return apicontract.AuthorizedSessionAttachment{}, err
	}
	if metadataStore == nil {
		return apicontract.AuthorizedSessionAttachment{}, errors.New("metadata store is required")
	}
	target, err := metadataStore.ResolveSessionExecutionTarget(ctx, sessionID.String())
	if err != nil {
		return apicontract.AuthorizedSessionAttachment{}, err
	}
	binding, err := metadataStore.LookupWorkspaceBindingByID(ctx, target.WorkspaceID)
	if err != nil {
		return apicontract.AuthorizedSessionAttachment{}, err
	}
	if trimmedActiveProjectID := strings.TrimSpace(activeProjectID); trimmedActiveProjectID != "" &&
		strings.TrimSpace(binding.ProjectID) != trimmedActiveProjectID {
		return apicontract.AuthorizedSessionAttachment{}, sessionOutsideActiveProjectError{sessionID: sessionID.String()}
	}
	return apicontract.AuthorizedSessionAttachment{
		SessionID:     sessionID,
		ProjectID:     binding.ProjectID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: binding.CanonicalRoot,
	}, nil
}

func executeLegacyUnary[Req any, Authz any](
	g *Gateway,
	ctx context.Context,
	state *connectionState,
	wire protocol.Request,
	req Req,
	authz Authz,
) protocol.Response {
	handler, ok := gatewayUnaryHandlerEntries[wire.Method]
	if !ok {
		return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", wire.Method))
	}
	return handler(g, ctx, state, wire)
}

type validatedOwnerError struct{ cause error }

func (e validatedOwnerError) Error() string { return e.cause.Error() }
func (e validatedOwnerError) Unwrap() error { return e.cause }

func responseForValidationOrOwnerError(id string, err error) protocol.Response {
	var ownerErr validatedOwnerError
	if errors.As(err, &ownerErr) {
		return responseForError(id, ownerErr.cause)
	}
	var rpcErr interface {
		RPCErrorCode() int
		RPCErrorData() json.RawMessage
	}
	if errors.As(err, &rpcErr) {
		return responseForError(id, err)
	}
	return protocol.NewErrorResponse(id, protocol.ErrCodeInvalidParams, err.Error())
}

func declareInboundExecutableRoutes() map[string]inboundExecutableRoute {
	executables := make(map[string]inboundExecutableRoute)
	for _, route := range apicontract.Routes() {
		switch route.Kind {
		case apicontract.KindUnary, apicontract.KindProgress, apicontract.KindSubscription:
			executables[route.Method] = erasedInboundExecutable(route)
		case apicontract.KindNotification:
		}
	}
	registerActiveProjectSessionRoutes(executables)
	registerProjectAndSmallServiceRoutes(executables)
	executables[protocol.MethodOnboardingFinalize] = inboundUnary[serverapi.OnboardingFinalizeRequest, noAuthorizationFacts](
		protocol.MethodOnboardingFinalize,
		apicontract.SemanticValidationRequired,
		requestDecoderOnboardingFinalize,
		nil,
		authorizeRouteScope[serverapi.OnboardingFinalizeRequest](mustInboundRoute(protocol.MethodOnboardingFinalize, apicontract.KindUnary)),
	)
	onboarding := executables[protocol.MethodOnboardingFinalize]
	onboarding.executeUnary = executeOnboardingFinalize
	executables[protocol.MethodOnboardingFinalize] = onboarding
	executables[protocol.MethodSessionGetExecutionEnvironment] = inboundUnary[serverapi.SessionExecutionEnvironmentRequest, apicontract.AuthorizedSessionInActiveProject](
		protocol.MethodSessionGetExecutionEnvironment,
		apicontract.SemanticValidationRequired,
		requestDecoderSessionExecutionEnvironment,
		nil,
		authorizeSessionActiveProject(func(req serverapi.SessionExecutionEnvironmentRequest) string { return req.SessionID.String() }),
	)
	sessionEnvironment := executables[protocol.MethodSessionGetExecutionEnvironment]
	sessionEnvironment.executeUnary = executeSessionExecutionEnvironment
	executables[protocol.MethodSessionGetExecutionEnvironment] = sessionEnvironment
	sessionPlan := executables[protocol.MethodSessionPlan]
	sessionPlan.executeUnary = executeSessionPlan
	executables[protocol.MethodSessionPlan] = sessionPlan
	workspaceChatDraft := executables[protocol.MethodSessionWorkspaceChatDraft]
	workspaceChatDraft.executeUnary = executeWorkspaceChatDraft
	executables[protocol.MethodSessionWorkspaceChatDraft] = workspaceChatDraft
	executables[protocol.MethodSessionWorkspaceChatMaterialize] = inboundTrustedUnary(
		protocol.MethodSessionWorkspaceChatMaterialize,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeRouteScope[serverapi.WorkspaceChatMaterializeRequest](mustInboundRoute(protocol.MethodSessionWorkspaceChatMaterialize, apicontract.KindUnary)),
		handleWorkspaceChatMaterialize,
	)
	attachProject := executables[protocol.MethodAttachProject]
	attachProject.executeUnary = executeAttachProject
	executables[protocol.MethodAttachProject] = attachProject
	executables[protocol.MethodAttachSession] = inboundTrustedUnary[protocol.AttachSessionRequest, apicontract.AuthorizedSessionAttachment, protocol.AttachResponse](
		protocol.MethodAttachSession,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionAttachment,
		handleAttachSession,
	)
	executables[protocol.MethodSessionRetargetWorkspace] = inboundTrustedUnary[serverapi.SessionRetargetWorkspaceRequest, apicontract.AttachedProjectConstraint, serverapi.SessionRetargetWorkspaceResponse](
		protocol.MethodSessionRetargetWorkspace,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		attachedProjectConstraint,
		handleSessionWorkspaceRetarget,
	)
	runPrompt := executables[protocol.MethodRunPrompt]
	runPrompt.executeProgress = executeRunPrompt
	executables[protocol.MethodRunPrompt] = runPrompt
	attentionSubscription := executables[protocol.MethodAttentionNotificationSubscribe]
	attentionSubscription.executeSubscription = executeAttentionNotificationSubscription
	executables[protocol.MethodAttentionNotificationSubscribe] = attentionSubscription
	executables[protocol.MethodWorktreeWorkspaceList] = inboundTrustedUnary[serverapi.WorktreeWorkspaceListRequest, apicontract.AuthorizedProjectWorkspaceBinding, serverapi.WorktreeWorkspaceListResponse](
		protocol.MethodWorktreeWorkspaceList,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeProjectWorkspaceBinding,
		handleWorktreeWorkspaceList,
	)
	executables[protocol.MethodProcessGet] = inboundTrustedUnary[serverapi.ProcessGetRequest, apicontract.AuthorizedProcessInActiveProject, serverapi.ProcessGetResponse](
		protocol.MethodProcessGet,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeProcessActiveProject(func(req serverapi.ProcessGetRequest) string { return req.ProcessID }),
		handleProcessGet,
	)
	executables[protocol.MethodProcessKill] = inboundTrustedUnary[serverapi.ProcessKillRequest, apicontract.AuthorizedProcessInActiveProject, serverapi.ProcessKillResponse](
		protocol.MethodProcessKill,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeProcessActiveProject(func(req serverapi.ProcessKillRequest) string { return req.ProcessID }),
		handleProcessKill,
	)
	executables[protocol.MethodProcessInlineOutput] = inboundTrustedUnary[serverapi.ProcessInlineOutputRequest, apicontract.AuthorizedProcessInActiveProject, serverapi.ProcessInlineOutputResponse](
		protocol.MethodProcessInlineOutput,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeProcessActiveProject(func(req serverapi.ProcessInlineOutputRequest) string { return req.ProcessID }),
		handleProcessInlineOutput,
	)
	executables[protocol.MethodProcessSubscribeOutput] = inboundSubscription[serverapi.ProcessOutputSubscribeRequest, apicontract.AuthorizedProcessInActiveProject](
		protocol.MethodProcessSubscribeOutput,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeProcessActiveProject(func(req serverapi.ProcessOutputSubscribeRequest) string { return req.ProcessID }),
	)
	processOutput := executables[protocol.MethodProcessSubscribeOutput]
	processOutput.executeSubscription = executeProcessOutputSubscription
	executables[protocol.MethodProcessSubscribeOutput] = processOutput
	executables[protocol.MethodRuntimeLiveSteer] = inboundTrustedUnary[serverapi.RuntimeLiveSteerRequest, noAuthorizationFacts, serverapi.RuntimeLiveSteerResponse](
		protocol.MethodRuntimeLiveSteer,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		func(context.Context, *Gateway, *connectionState, apicontract.Validated[serverapi.RuntimeLiveSteerRequest]) (noAuthorizationFacts, error) {
			return noAuthorizationFacts{}, nil
		},
		handleRuntimeLiveSteer,
	)
	executables[protocol.MethodRuntimeLiveStop] = inboundTrustedUnary[serverapi.RuntimeLiveStopRequest, noAuthorizationFacts, serverapi.RuntimeLiveStopResponse](
		protocol.MethodRuntimeLiveStop,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		func(context.Context, *Gateway, *connectionState, apicontract.Validated[serverapi.RuntimeLiveStopRequest]) (noAuthorizationFacts, error) {
			return noAuthorizationFacts{}, nil
		},
		handleRuntimeLiveStop,
	)
	executables[protocol.MethodRuntimeLiveWait] = inboundTrustedUnary[serverapi.RuntimeLiveWaitRequest, noAuthorizationFacts, serverapi.RuntimeLiveWaitResponse](
		protocol.MethodRuntimeLiveWait,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		func(context.Context, *Gateway, *connectionState, apicontract.Validated[serverapi.RuntimeLiveWaitRequest]) (noAuthorizationFacts, error) {
			return noAuthorizationFacts{}, nil
		},
		handleRuntimeLiveWait,
	)
	executables[protocol.MethodRuntimeLiveWatch] = inboundTrustedUnary[serverapi.RuntimeLiveWatchRequest, noAuthorizationFacts, serverapi.RuntimeLiveWatchResponse](
		protocol.MethodRuntimeLiveWatch,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		func(context.Context, *Gateway, *connectionState, apicontract.Validated[serverapi.RuntimeLiveWatchRequest]) (noAuthorizationFacts, error) {
			return noAuthorizationFacts{}, nil
		},
		handleRuntimeLiveWatch,
	)
	registerGoalRoutes(executables)
	executables[protocol.MethodSessionRuntimeActivate] = inboundTrustedUnary[serverapi.SessionRuntimeActivateRequest, apicontract.AuthorizedSessionInActiveProject, serverapi.SessionRuntimeActivateResponse](
		protocol.MethodSessionRuntimeActivate,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		prepareSessionRuntimeActivate,
		authorizeSessionActiveProject(func(req serverapi.SessionRuntimeActivateRequest) string { return req.SessionID }),
		handleSessionRuntimeActivate,
	)
	executables[protocol.MethodSessionRuntimeRelease] = inboundTrustedUnary[serverapi.SessionRuntimeReleaseRequest, apicontract.AuthorizedSessionInActiveProject, serverapi.SessionRuntimeReleaseResponse](
		protocol.MethodSessionRuntimeRelease,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		prepareSessionRuntimeRelease,
		authorizeSessionActiveProject(func(req serverapi.SessionRuntimeReleaseRequest) string { return req.Attachment.SessionID }),
		handleSessionRuntimeRelease,
	)
	return executables
}

func registerProjectAndSmallServiceRoutes(executables map[string]inboundExecutableRoute) {
	projectClient := func(deps GatewayDependencies) any { return deps.ProjectViewClient() }
	executables[protocol.MethodProjectList] = trustedServiceUnary(protocol.MethodProjectList, projectClient, apicontract.ProjectViewTrustedService.ListProjectsValidated)
	executables[protocol.MethodProjectHomeList] = trustedServiceUnary(protocol.MethodProjectHomeList, projectClient, apicontract.ProjectViewTrustedService.ListProjectHomeValidated)
	executables[protocol.MethodProjectResolvePath] = trustedServiceUnary(protocol.MethodProjectResolvePath, projectClient, apicontract.ProjectViewTrustedService.ResolveProjectPathValidated)
	executables[protocol.MethodProjectPlanWorkspaceBinding] = trustedServiceUnary(protocol.MethodProjectPlanWorkspaceBinding, projectClient, apicontract.ProjectViewTrustedService.PlanWorkspaceBindingValidated)
	executables[protocol.MethodProjectCreate] = trustedServiceUnary(protocol.MethodProjectCreate, projectClient, apicontract.ProjectViewTrustedService.CreateProjectValidated)
	executables[protocol.MethodProjectEditGet] = trustedServiceUnary(protocol.MethodProjectEditGet, projectClient, apicontract.ProjectViewTrustedService.GetProjectEditValidated)
	executables[protocol.MethodProjectUpdate] = trustedServiceUnary(protocol.MethodProjectUpdate, projectClient, apicontract.ProjectViewTrustedService.UpdateProjectValidated)
	executables[protocol.MethodProjectSetDefaultWorkspace] = trustedServiceUnary(protocol.MethodProjectSetDefaultWorkspace, projectClient, apicontract.ProjectViewTrustedService.SetDefaultWorkspaceValidated)
	executables[protocol.MethodProjectWorkspaceList] = trustedServiceUnary(protocol.MethodProjectWorkspaceList, projectClient, apicontract.ProjectViewTrustedService.ListProjectWorkspacesValidated)
	executables[protocol.MethodProjectWorkspaceGet] = trustedServiceUnary(protocol.MethodProjectWorkspaceGet, projectClient, apicontract.ProjectViewTrustedService.GetProjectWorkspaceValidated)
	executables[protocol.MethodProjectUnlinkWorkspace] = trustedServiceUnary(protocol.MethodProjectUnlinkWorkspace, projectClient, apicontract.ProjectViewTrustedService.UnlinkWorkspaceFromProjectValidated)
	executables[protocol.MethodProjectDelete] = trustedServiceUnary(protocol.MethodProjectDelete, projectClient, apicontract.ProjectViewTrustedService.DeleteProjectValidated)
	executables[protocol.MethodProjectAttachWorkspace] = trustedServiceUnary(protocol.MethodProjectAttachWorkspace, projectClient, apicontract.ProjectViewTrustedService.AttachWorkspaceToProjectValidated)
	executables[protocol.MethodProjectRebindWorkspace] = trustedServiceUnary(protocol.MethodProjectRebindWorkspace, projectClient, apicontract.ProjectViewTrustedService.RebindWorkspaceValidated)
	executables[protocol.MethodProjectGetOverview] = trustedServiceUnary(protocol.MethodProjectGetOverview, projectClient, apicontract.ProjectViewTrustedService.GetProjectOverviewValidated)
	executables[protocol.MethodSessionPage] = trustedServiceUnary(protocol.MethodSessionPage, projectClient, apicontract.ProjectViewTrustedService.ListSessionPageValidated)

	workflowClient := func(deps GatewayDependencies) any { return deps.WorkflowClient() }
	executables[protocol.MethodWorkflowCreate] = trustedServiceUnary(protocol.MethodWorkflowCreate, workflowClient, apicontract.WorkflowTrustedService.CreateWorkflowValidated)
	executables[protocol.MethodWorkflowCreateAndLinkProject] = trustedServiceUnary(protocol.MethodWorkflowCreateAndLinkProject, workflowClient, apicontract.WorkflowTrustedService.CreateAndLinkWorkflowToProjectValidated)
	executables[protocol.MethodWorkflowUpdate] = trustedServiceUnary(protocol.MethodWorkflowUpdate, workflowClient, apicontract.WorkflowTrustedService.UpdateWorkflowValidated)
	executables[protocol.MethodWorkflowList] = trustedServiceUnary(protocol.MethodWorkflowList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowsValidated)
	executables[protocol.MethodWorkflowGet] = trustedServiceUnary(protocol.MethodWorkflowGet, workflowClient, apicontract.WorkflowTrustedService.GetWorkflowValidated)
	executables[protocol.MethodWorkflowLinkProject] = trustedServiceUnary(protocol.MethodWorkflowLinkProject, workflowClient, apicontract.WorkflowTrustedService.LinkWorkflowToProjectValidated)
	executables[protocol.MethodWorkflowListProjectLinks] = trustedServiceUnary(protocol.MethodWorkflowListProjectLinks, workflowClient, apicontract.WorkflowTrustedService.ListProjectWorkflowLinksValidated)
	executables[protocol.MethodWorkflowSetDefaultProjectLink] = trustedServiceUnary(protocol.MethodWorkflowSetDefaultProjectLink, workflowClient, apicontract.WorkflowTrustedService.SetDefaultProjectWorkflowLinkValidated)
	executables[protocol.MethodWorkflowUnlinkProject] = trustedServiceUnary(protocol.MethodWorkflowUnlinkProject, workflowClient, apicontract.WorkflowTrustedService.UnlinkWorkflowFromProjectValidated)
	executables[protocol.MethodWorkflowDeletePreview] = trustedServiceUnary(protocol.MethodWorkflowDeletePreview, workflowClient, apicontract.WorkflowTrustedService.PreviewWorkflowDeleteValidated)
	executables[protocol.MethodWorkflowDelete] = trustedServiceUnary(protocol.MethodWorkflowDelete, workflowClient, apicontract.WorkflowTrustedService.DeleteWorkflowValidated)
	executables[protocol.MethodWorkflowValidate] = trustedServiceUnary(protocol.MethodWorkflowValidate, workflowClient, apicontract.WorkflowTrustedService.ValidateWorkflowValidated)
	executables[protocol.MethodWorkflowScriptPathValidate] = trustedServiceUnary(protocol.MethodWorkflowScriptPathValidate, workflowClient, apicontract.WorkflowTrustedService.ValidateWorkflowScriptPathValidated)
	executables[protocol.MethodWorkflowGraphValidateDraft] = trustedServiceUnary(protocol.MethodWorkflowGraphValidateDraft, workflowClient, apicontract.WorkflowTrustedService.ValidateWorkflowGraphDraftValidated)
	executables[protocol.MethodWorkflowGraphDeriveWiring] = trustedServiceUnary(protocol.MethodWorkflowGraphDeriveWiring, workflowClient, apicontract.WorkflowTrustedService.DeriveWorkflowGraphWiringValidated)
	executables[protocol.MethodWorkflowGraphSavePreview] = trustedServiceUnary(protocol.MethodWorkflowGraphSavePreview, workflowClient, apicontract.WorkflowTrustedService.PreviewWorkflowGraphSaveValidated)
	executables[protocol.MethodWorkflowGraphSave] = trustedServiceUnary(protocol.MethodWorkflowGraphSave, workflowClient, apicontract.WorkflowTrustedService.SaveWorkflowGraphValidated)
	executables[protocol.MethodWorkflowProjectLabelCreate] = trustedServiceUnary(protocol.MethodWorkflowProjectLabelCreate, workflowClient, apicontract.WorkflowTrustedService.CreateWorkflowProjectLabelValidated)
	executables[protocol.MethodWorkflowProjectLabelRename] = trustedServiceUnary(protocol.MethodWorkflowProjectLabelRename, workflowClient, apicontract.WorkflowTrustedService.RenameWorkflowProjectLabelValidated)
	executables[protocol.MethodWorkflowProjectLabelDelete] = trustedServiceUnary(protocol.MethodWorkflowProjectLabelDelete, workflowClient, apicontract.WorkflowTrustedService.DeleteWorkflowProjectLabelValidated)
	executables[protocol.MethodWorkflowTaskCreate] = trustedServiceUnary(protocol.MethodWorkflowTaskCreate, workflowClient, apicontract.WorkflowTrustedService.CreateWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskDependencyAdd] = trustedServiceUnary(protocol.MethodWorkflowTaskDependencyAdd, workflowClient, apicontract.WorkflowTrustedService.AddWorkflowTaskDependencyValidated)
	executables[protocol.MethodWorkflowTaskDependencyRemove] = trustedServiceUnary(protocol.MethodWorkflowTaskDependencyRemove, workflowClient, apicontract.WorkflowTrustedService.RemoveWorkflowTaskDependencyValidated)
	executables[protocol.MethodWorkflowTaskUpdate] = trustedServiceUnary(protocol.MethodWorkflowTaskUpdate, workflowClient, apicontract.WorkflowTrustedService.UpdateWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskStart] = trustedServiceUnary(protocol.MethodWorkflowTaskStart, workflowClient, apicontract.WorkflowTrustedService.StartWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskInterrupt] = trustedServiceUnary(protocol.MethodWorkflowTaskInterrupt, workflowClient, apicontract.WorkflowTrustedService.InterruptWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskResume] = trustedServiceUnary(protocol.MethodWorkflowTaskResume, workflowClient, apicontract.WorkflowTrustedService.ResumeWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskApprove] = trustedServiceUnary(protocol.MethodWorkflowTaskApprove, workflowClient, apicontract.WorkflowTrustedService.ApproveWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskMovePreview] = trustedServiceUnary(protocol.MethodWorkflowTaskMovePreview, workflowClient, apicontract.WorkflowTrustedService.PreviewWorkflowTaskMoveValidated)
	executables[protocol.MethodWorkflowTaskMove] = trustedServiceUnary(protocol.MethodWorkflowTaskMove, workflowClient, apicontract.WorkflowTrustedService.MoveWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskComplete] = trustedServiceUnary(protocol.MethodWorkflowTaskComplete, workflowClient, apicontract.WorkflowTrustedService.CompleteWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskDelete] = trustedServiceUnaryNoResponse(protocol.MethodWorkflowTaskDelete, workflowClient, apicontract.WorkflowTrustedService.DeleteWorkflowTaskValidated)
	executables[protocol.MethodWorkflowTaskObserve] = trustedServiceUnary(protocol.MethodWorkflowTaskObserve, workflowClient, apicontract.WorkflowTrustedService.ObserveWorkflowTaskValidated)
	executables[protocol.MethodWorkflowProjectLabelList] = trustedServiceUnary(protocol.MethodWorkflowProjectLabelList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowProjectLabelsValidated)
	executables[protocol.MethodWorkflowTaskLabelsGet] = trustedServiceUnary(protocol.MethodWorkflowTaskLabelsGet, workflowClient, apicontract.WorkflowTrustedService.GetWorkflowTaskLabelsValidated)
	executables[protocol.MethodWorkflowAttentionList] = trustedServiceUnary(protocol.MethodWorkflowAttentionList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowAttentionValidated)
	executables[protocol.MethodWorkflowTaskAttentionList] = trustedServiceUnary(protocol.MethodWorkflowTaskAttentionList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowTaskAttentionValidated)
	executables[protocol.MethodWorkflowTaskCommentList] = trustedServiceUnary(protocol.MethodWorkflowTaskCommentList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowTaskCommentsValidated)
	executables[protocol.MethodWorkflowTaskActivityList] = trustedServiceUnary(protocol.MethodWorkflowTaskActivityList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowTaskActivityValidated)
	executables[protocol.MethodWorkflowTaskSessionList] = trustedServiceUnary(protocol.MethodWorkflowTaskSessionList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowTaskSessionsValidated)
	executables[protocol.MethodWorkflowTaskList] = trustedServiceUnary(protocol.MethodWorkflowTaskList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowTasksValidated)
	executables[protocol.MethodWorkflowTaskSearch] = trustedServiceUnary(protocol.MethodWorkflowTaskSearch, workflowClient, apicontract.WorkflowTrustedService.SearchWorkflowTasksValidated)
	executables[protocol.MethodWorkflowBoardGet] = trustedServiceUnary(protocol.MethodWorkflowBoardGet, workflowClient, apicontract.WorkflowTrustedService.GetWorkflowBoardValidated)
	executables[protocol.MethodWorkflowBoardNodeCardsList] = trustedServiceUnary(protocol.MethodWorkflowBoardNodeCardsList, workflowClient, apicontract.WorkflowTrustedService.ListWorkflowBoardNodeCardsValidated)
	executables[protocol.MethodWorkflowTaskGet] = trustedServiceUnary(protocol.MethodWorkflowTaskGet, workflowClient, apicontract.WorkflowTrustedService.GetWorkflowTaskValidated)

	executables[protocol.MethodAuthGetStatus] = trustedServiceUnary(
		protocol.MethodAuthGetStatus,
		func(deps GatewayDependencies) any { return deps.AuthStatusClient() },
		apicontract.AuthStatusTrustedService.GetAuthStatusValidated,
	)
	executables[protocol.MethodCapabilityFactsGet] = trustedServiceUnary(
		protocol.MethodCapabilityFactsGet,
		func(deps GatewayDependencies) any { return deps.CapabilityFactsClient() },
		apicontract.CapabilityFactsTrustedService.GetCapabilityFactsValidated,
	)
	executables[protocol.MethodPromptAnswerBatch] = inboundTrustedUnary(
		protocol.MethodPromptAnswerBatch,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.PromptAnswerBatchRequest) string { return req.SessionID.String() }),
		handlePromptAnswerBatch,
	)
	executables[protocol.MethodServerUpdateStatusGet] = trustedServiceUnary(
		protocol.MethodServerUpdateStatusGet,
		func(deps GatewayDependencies) any { return deps.ServerStatusClient() },
		apicontract.ServerStatusTrustedService.GetUpdateStatusValidated,
	)

	executables[protocol.MethodAuthGetBootstrapStatus] = inboundTrustedUnary(
		protocol.MethodAuthGetBootstrapStatus, apicontract.NoSemanticValidation, requestDecoderDefault, nil,
		authorizeRouteScope[serverapi.AuthGetBootstrapStatusRequest](mustInboundRoute(protocol.MethodAuthGetBootstrapStatus, apicontract.KindUnary)),
		handleAuthGetBootstrapStatus,
	)
	executables[protocol.MethodAuthCompleteBootstrap] = inboundTrustedUnary(
		protocol.MethodAuthCompleteBootstrap, apicontract.SemanticValidationRequired, requestDecoderDefault, nil,
		authorizeRouteScope[serverapi.AuthCompleteBootstrapRequest](mustInboundRoute(protocol.MethodAuthCompleteBootstrap, apicontract.KindUnary)),
		handleAuthCompleteBootstrap,
	)
	executables[protocol.MethodAuthAcknowledgeNoAuth] = inboundTrustedUnary(
		protocol.MethodAuthAcknowledgeNoAuth, apicontract.NoSemanticValidation, requestDecoderDefault, nil,
		authorizeRouteScope[serverapi.AuthAcknowledgeNoAuthRequest](mustInboundRoute(protocol.MethodAuthAcknowledgeNoAuth, apicontract.KindUnary)),
		handleAuthAcknowledgeNoAuth,
	)
	executables[protocol.MethodServerReadinessGet] = inboundTrustedUnary(
		protocol.MethodServerReadinessGet, apicontract.NoSemanticValidation, requestDecoderDefault, nil,
		authorizeRouteScope[serverapi.ServerReadinessRequest](mustInboundRoute(protocol.MethodServerReadinessGet, apicontract.KindUnary)),
		handleServerReadiness,
	)
}

func registerGoalRoutes(executables map[string]inboundExecutableRoute) {
	authorizeShow := authorizeGoalSession(func(req serverapi.RuntimeGoalShowRequest) string { return req.SessionID })
	authorizeSet := authorizeGoalSession(func(req serverapi.RuntimeGoalSetRequest) string { return req.SessionID })
	authorizeStatus := authorizeGoalSession(func(req serverapi.RuntimeGoalStatusRequest) string { return req.SessionID })
	authorizeClear := authorizeGoalSession(func(req serverapi.RuntimeGoalClearRequest) string { return req.SessionID })
	executables[protocol.MethodRuntimeGoalShow] = inboundTrustedUnary(protocol.MethodRuntimeGoalShow, apicontract.SemanticValidationRequired, requestDecoderDefault, nil, authorizeShow, handleRuntimeGoalShow)
	executables[protocol.MethodRuntimeGoalSet] = inboundTrustedUnary(protocol.MethodRuntimeGoalSet, apicontract.SemanticValidationRequired, requestDecoderDefault, nil, authorizeSet, handleRuntimeGoalSet)
	executables[protocol.MethodRuntimeGoalPause] = inboundTrustedUnary(protocol.MethodRuntimeGoalPause, apicontract.SemanticValidationRequired, requestDecoderDefault, nil, authorizeStatus, handleRuntimeGoalPause)
	executables[protocol.MethodRuntimeGoalResume] = inboundTrustedUnary(protocol.MethodRuntimeGoalResume, apicontract.SemanticValidationRequired, requestDecoderDefault, nil, authorizeStatus, handleRuntimeGoalResume)
	executables[protocol.MethodRuntimeGoalComplete] = inboundTrustedUnary(protocol.MethodRuntimeGoalComplete, apicontract.SemanticValidationRequired, requestDecoderDefault, nil, authorizeStatus, handleRuntimeGoalComplete)
	executables[protocol.MethodRuntimeGoalClear] = inboundTrustedUnary(protocol.MethodRuntimeGoalClear, apicontract.SemanticValidationRequired, requestDecoderDefault, nil, authorizeClear, handleRuntimeGoalClear)
}

func authorizeGoalSession[Req any](sessionID func(Req) string) func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (noAuthorizationFacts, error) {
	return func(ctx context.Context, g *Gateway, state *connectionState, validated apicontract.Validated[Req]) (noAuthorizationFacts, error) {
		err := g.requireGoalSessionAccess(ctx, state, sessionID(validated.Value()))
		return noAuthorizationFacts{}, err
	}
}

func registerActiveProjectSessionRoutes(executables map[string]inboundExecutableRoute) {
	registerRequiredSessionUnary := func(method string, executable inboundExecutableRoute) {
		executables[method] = executable
	}
	registerOptionalSessionUnary := func(method string, executable inboundExecutableRoute) {
		executables[method] = executable
	}

	registerRequiredSessionUnary(protocol.MethodSessionGetMainView, inboundTrustedUnary(
		protocol.MethodSessionGetMainView,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.SessionMainViewRequest) string { return req.SessionID }),
		handleSessionMainView,
	))
	registerRequiredSessionUnary(protocol.MethodSessionGetTranscriptPage, inboundTrustedUnary(
		protocol.MethodSessionGetTranscriptPage,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.SessionTranscriptPageRequest) string { return req.SessionID }),
		handleSessionTranscriptPage,
	))
	registerRequiredSessionUnary(protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer, inboundTrustedUnary(
		protocol.MethodSessionGetLatestCommittedAssistantFinalAnswer,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.SessionLatestCommittedAssistantFinalAnswerRequest) string { return req.SessionID }),
		handleLatestCommittedAssistantFinalAnswer,
	))
	registerRequiredSessionUnary(protocol.MethodSessionPersistInputDraft, inboundTrustedUnary(
		protocol.MethodSessionPersistInputDraft,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.SessionPersistInputDraftRequest) string { return req.SessionID }),
		handleSessionPersistInputDraft,
	))
	registerOptionalSessionUnary(protocol.MethodSessionGetInitialInput, inboundTrustedUnary(
		protocol.MethodSessionGetInitialInput,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeOptionalSessionActiveProject(func(req serverapi.SessionInitialInputRequest) string { return req.SessionID }),
		handleSessionInitialInput,
	))
	registerOptionalSessionUnary(protocol.MethodSessionResolveTransition, inboundTrustedUnary(
		protocol.MethodSessionResolveTransition,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeOptionalSessionActiveProject(func(req serverapi.SessionResolveTransitionRequest) string { return req.SessionID }),
		handleSessionResolveTransition,
	))

	executables[protocol.MethodWorktreeStatus] = trustedWorktreeSessionUnary(protocol.MethodWorktreeStatus, func(req serverapi.WorktreeStatusRequest) string { return req.SessionID }, handleWorktreeStatus)
	executables[protocol.MethodWorktreeList] = trustedWorktreeSessionUnary(protocol.MethodWorktreeList, func(req serverapi.WorktreeListRequest) string { return req.SessionID }, handleWorktreeList)
	executables[protocol.MethodWorktreeSelectorResolve] = trustedWorktreeSessionUnary(protocol.MethodWorktreeSelectorResolve, func(req serverapi.WorktreeSelectorPreviewRequest) string { return req.SessionID }, handleWorktreeSelectorResolve)
	executables[protocol.MethodWorktreeDeletePreview] = trustedWorktreeSessionUnary(protocol.MethodWorktreeDeletePreview, func(req serverapi.WorktreeDeletePreviewRequest) string { return req.SessionID }, handleWorktreeDeletePreview)
	executables[protocol.MethodWorktreeCreateTargetResolve] = trustedWorktreeSessionUnary(protocol.MethodWorktreeCreateTargetResolve, func(req serverapi.WorktreeCreateTargetResolveRequest) string { return req.SessionID }, handleWorktreeCreateTargetResolve)
	executables[protocol.MethodWorktreeCreate] = trustedWorktreeSessionUnary(protocol.MethodWorktreeCreate, func(req serverapi.WorktreeCreateRequest) string { return req.SessionID }, handleWorktreeCreate)
	executables[protocol.MethodWorktreeEnter] = trustedWorktreeSessionUnary(protocol.MethodWorktreeEnter, func(req serverapi.WorktreeEnterRequest) string { return req.SessionID }, handleWorktreeEnter)
	executables[protocol.MethodWorktreeLeave] = trustedWorktreeSessionUnary(protocol.MethodWorktreeLeave, func(req serverapi.WorktreeLeaveRequest) string { return req.SessionID }, handleWorktreeLeave)
	executables[protocol.MethodWorktreeDelete] = trustedWorktreeSessionUnary(protocol.MethodWorktreeDelete, func(req serverapi.WorktreeDeleteRequest) string { return req.SessionID }, handleWorktreeDelete)

	registerRequiredSessionUnary(protocol.MethodRuntimeSetSessionName, trustedRuntimeControlNoResponse(protocol.MethodRuntimeSetSessionName, func(req serverapi.RuntimeSetSessionNameRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.SetSessionNameValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetThinkingLevel, trustedRuntimeControlNoResponse(protocol.MethodRuntimeSetThinkingLevel, func(req serverapi.RuntimeSetThinkingLevelRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.SetThinkingLevelValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetFastModeEnabled, trustedRuntimeControlUnary(protocol.MethodRuntimeSetFastModeEnabled, func(req serverapi.RuntimeSetFastModeEnabledRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.SetFastModeEnabledValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetReviewerEnabled, trustedRuntimeControlUnary(protocol.MethodRuntimeSetReviewerEnabled, func(req serverapi.RuntimeSetReviewerEnabledRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.SetReviewerEnabledValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetAutoCompactionEnabled, trustedRuntimeControlUnary(protocol.MethodRuntimeSetAutoCompactionEnabled, func(req serverapi.RuntimeSetAutoCompactionEnabledRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.SetAutoCompactionEnabledValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetQuestionsEnabled, trustedRuntimeControlUnary(protocol.MethodRuntimeSetQuestionsEnabled, func(req serverapi.RuntimeSetQuestionsEnabledRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.SetQuestionsEnabledValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeAppendCommittedEntry, trustedRuntimeControlNoResponse(protocol.MethodRuntimeAppendCommittedEntry, func(req serverapi.RuntimeAppendCommittedEntryRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.AppendCommittedEntryValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeShouldCompactBeforeUserMessage, trustedRuntimeControlUnary(protocol.MethodRuntimeShouldCompactBeforeUserMessage, func(req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.ShouldCompactBeforeUserMessageValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeSubmitUserTurn, trustedRuntimeControlUnary(protocol.MethodRuntimeSubmitUserTurn, func(req serverapi.RuntimeSubmitUserTurnRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.SubmitUserTurnValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeSubmitUserShellCommand, trustedRuntimeControlNoResponse(protocol.MethodRuntimeSubmitUserShellCommand, func(req serverapi.RuntimeSubmitUserShellCommandRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.SubmitUserShellCommandValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeCompactContext, trustedRuntimeControlNoResponse(protocol.MethodRuntimeCompactContext, func(req serverapi.RuntimeCompactContextRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.CompactContextValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeInterrupt, trustedRuntimeControlUnary(protocol.MethodRuntimeInterrupt, func(req serverapi.RuntimeInterruptRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.InterruptValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeDiscardQueuedUserMessage, trustedRuntimeControlUnary(protocol.MethodRuntimeDiscardQueuedUserMessage, func(req serverapi.RuntimeDiscardQueuedUserMessageRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.DiscardQueuedUserMessageValidated))
	registerRequiredSessionUnary(protocol.MethodRuntimeRecordPromptHistory, trustedRuntimeControlNoResponse(protocol.MethodRuntimeRecordPromptHistory, func(req serverapi.RuntimeRecordPromptHistoryRequest) string { return req.SessionID }, apicontract.RuntimeControlTrustedService.RecordPromptHistoryValidated))

	registerRequiredSessionUnary(protocol.MethodAskListPending, activeProjectSessionUnary[serverapi.AskListPendingBySessionRequest](protocol.MethodAskListPending, func(req serverapi.AskListPendingBySessionRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodPromptAnswerBatch, activeProjectSessionUnary[serverapi.PromptAnswerBatchRequest](protocol.MethodPromptAnswerBatch, func(req serverapi.PromptAnswerBatchRequest) string { return req.SessionID.String() }))
	registerRequiredSessionUnary(protocol.MethodApprovalListPending, activeProjectSessionUnary[serverapi.ApprovalListPendingBySessionRequest](protocol.MethodApprovalListPending, func(req serverapi.ApprovalListPendingBySessionRequest) string { return req.SessionID }))

	promptFollowUp := inboundSubscription[serverapi.PromptFollowUpWatchRequest, apicontract.AuthorizedSessionInActiveProject](
		protocol.MethodPromptFollowUpWatch,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(func(req serverapi.PromptFollowUpWatchRequest) string { return req.SessionID.String() }),
	)
	promptFollowUp.executeSubscription = executePromptFollowUpSubscription
	executables[protocol.MethodPromptFollowUpWatch] = promptFollowUp
}

func activeProjectSessionUnary[Req any](method string, sessionID func(Req) string) inboundExecutableRoute {
	return inboundUnary[Req, apicontract.AuthorizedSessionInActiveProject](
		method,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(sessionID),
	)
}

func trustedRuntimeControlUnary[Req any, Resp any](
	method string,
	sessionID func(Req) string,
	call func(apicontract.RuntimeControlTrustedService, context.Context, apicontract.Validated[Req], apicontract.AuthorizedSessionInActiveProject) (Resp, error),
) inboundExecutableRoute {
	return inboundTrustedUnary(
		method,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(sessionID),
		func(ctx context.Context, g *Gateway, _ *connectionState, req apicontract.Validated[Req], authorization apicontract.AuthorizedSessionInActiveProject) (Resp, error) {
			trusted, ok := g.deps.RuntimeControlClient().(apicontract.RuntimeControlTrustedService)
			if !ok {
				var zero Resp
				return zero, errors.New("Runtime Control trusted service is required")
			}
			return call(trusted, ctx, req, authorization)
		},
	)
}

func trustedRuntimeControlNoResponse[Req any](
	method string,
	sessionID func(Req) string,
	call func(apicontract.RuntimeControlTrustedService, context.Context, apicontract.Validated[Req], apicontract.AuthorizedSessionInActiveProject) error,
) inboundExecutableRoute {
	return trustedRuntimeControlUnary(
		method,
		sessionID,
		func(trusted apicontract.RuntimeControlTrustedService, ctx context.Context, req apicontract.Validated[Req], authorization apicontract.AuthorizedSessionInActiveProject) (struct{}, error) {
			return struct{}{}, call(trusted, ctx, req, authorization)
		},
	)
}

func trustedWorktreeSessionUnary[Req any, Resp any](
	method string,
	sessionID func(Req) string,
	handle func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req], apicontract.AuthorizedSessionInActiveProject) (Resp, error),
) inboundExecutableRoute {
	return inboundTrustedUnary(
		method,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeSessionActiveProject(sessionID),
		handle,
	)
}

func optionalActiveProjectSessionUnary[Req any](method string, sessionID func(Req) string) inboundExecutableRoute {
	return inboundUnary[Req, apicontract.OptionalAuthorizedSessionInActiveProject](
		method,
		apicontract.SemanticValidationRequired,
		requestDecoderDefault,
		nil,
		authorizeOptionalSessionActiveProject(sessionID),
	)
}

func handleRuntimeLiveSteer(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.RuntimeLiveSteerRequest],
	_ noAuthorizationFacts,
) (serverapi.RuntimeLiveSteerResponse, error) {
	trusted, ok := g.deps.RuntimeLiveControlClient().(apicontract.RuntimeLiveControlTrustedService)
	if !ok {
		return serverapi.RuntimeLiveSteerResponse{}, errors.New("Runtime Live Control trusted service is required")
	}
	return trusted.LiveSteerValidated(ctx, request, runtimeLiveSteerRequestIdentity(request))
}

func runtimeLiveControlTrustedService(g *Gateway) (apicontract.RuntimeLiveControlTrustedService, error) {
	trusted, ok := g.deps.RuntimeLiveControlClient().(apicontract.RuntimeLiveControlTrustedService)
	if !ok {
		return nil, errors.New("Runtime Live Control trusted service is required")
	}
	return trusted, nil
}

func handleRuntimeLiveStop(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.RuntimeLiveStopRequest],
	_ noAuthorizationFacts,
) (serverapi.RuntimeLiveStopResponse, error) {
	trusted, err := runtimeLiveControlTrustedService(g)
	if err != nil {
		return serverapi.RuntimeLiveStopResponse{}, err
	}
	return trusted.LiveStopValidated(ctx, request, apicontract.RuntimeLiveRequestIdentity{SessionID: request.SessionID(request.Value().SessionID)})
}

func handleRuntimeLiveWait(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.RuntimeLiveWaitRequest],
	_ noAuthorizationFacts,
) (serverapi.RuntimeLiveWaitResponse, error) {
	trusted, ok := g.deps.RuntimeLiveControlClient().(apicontract.RuntimeLiveControlTrustedService)
	if !ok {
		return serverapi.RuntimeLiveWaitResponse{}, errors.New("Runtime Live Control trusted service is required")
	}
	return trusted.LiveWaitValidated(ctx, request, apicontract.RuntimeLiveRequestIdentity{SessionID: request.SessionID(request.Value().SessionID)})
}

func handleRuntimeLiveWatch(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.RuntimeLiveWatchRequest],
	_ noAuthorizationFacts,
) (serverapi.RuntimeLiveWatchResponse, error) {
	trusted, err := runtimeLiveControlTrustedService(g)
	if err != nil {
		return serverapi.RuntimeLiveWatchResponse{}, err
	}
	return trusted.LiveWatchValidated(ctx, request, apicontract.RuntimeLiveRequestIdentity{SessionID: request.SessionID(request.Value().SessionID)})
}

func runtimeLiveSteerRequestIdentity(request apicontract.Validated[serverapi.RuntimeLiveSteerRequest]) apicontract.RuntimeLiveRequestIdentity {
	req := request.Value()
	identity := apicontract.RuntimeLiveRequestIdentity{
		SessionID:       request.SessionID(req.SessionID),
		ClientRequestID: request.RuntimeClientRequestID(req.ClientRequestID),
	}
	if req.CallerSessionID != nil {
		callerID := request.SessionID(*req.CallerSessionID)
		identity.CallerSessionID = &callerID
	}
	return identity
}

func runtimeGoalTrustedService(g *Gateway) (apicontract.RuntimeGoalTrustedService, error) {
	trusted, ok := g.deps.RuntimeControlClient().(apicontract.RuntimeGoalTrustedService)
	if !ok {
		return nil, errors.New("Runtime Goal trusted service is required")
	}
	return trusted, nil
}

func handleRuntimeGoalShow(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.RuntimeGoalShowRequest], _ noAuthorizationFacts) (serverapi.RuntimeGoalShowResponse, error) {
	trusted, err := runtimeGoalTrustedService(g)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	return trusted.ShowGoalValidated(ctx, request)
}

func handleRuntimeGoalSet(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.RuntimeGoalSetRequest], _ noAuthorizationFacts) (serverapi.RuntimeGoalMutationResponse, error) {
	trusted, err := runtimeGoalTrustedService(g)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	return trusted.SetGoalValidated(ctx, request)
}

func handleRuntimeGoalPause(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.RuntimeGoalStatusRequest], _ noAuthorizationFacts) (serverapi.RuntimeGoalMutationResponse, error) {
	trusted, err := runtimeGoalTrustedService(g)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	return trusted.PauseGoalValidated(ctx, request)
}

func handleRuntimeGoalResume(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.RuntimeGoalStatusRequest], _ noAuthorizationFacts) (serverapi.RuntimeGoalMutationResponse, error) {
	trusted, err := runtimeGoalTrustedService(g)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	return trusted.ResumeGoalValidated(ctx, request)
}

func handleRuntimeGoalComplete(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.RuntimeGoalStatusRequest], _ noAuthorizationFacts) (serverapi.RuntimeGoalMutationResponse, error) {
	trusted, err := runtimeGoalTrustedService(g)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	return trusted.CompleteGoalValidated(ctx, request)
}

func handleRuntimeGoalClear(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.RuntimeGoalClearRequest], _ noAuthorizationFacts) (serverapi.RuntimeGoalMutationResponse, error) {
	trusted, err := runtimeGoalTrustedService(g)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	return trusted.ClearGoalValidated(ctx, request)
}

func handleSessionMainView(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.SessionMainViewRequest],
	authorization apicontract.AuthorizedSessionInActiveProject,
) (serverapi.SessionMainViewResponse, error) {
	trusted, ok := g.deps.SessionViewClient().(apicontract.SessionViewTrustedService)
	if !ok {
		return serverapi.SessionMainViewResponse{}, errors.New("Session View trusted service is required")
	}
	return trusted.GetSessionMainViewValidated(ctx, request, authorization)
}

func handleSessionTranscriptPage(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.SessionTranscriptPageRequest],
	authorization apicontract.AuthorizedSessionInActiveProject,
) (serverapi.SessionTranscriptPageResponse, error) {
	trusted, ok := g.deps.SessionViewClient().(apicontract.SessionViewTrustedService)
	if !ok {
		return serverapi.SessionTranscriptPageResponse{}, errors.New("Session View trusted service is required")
	}
	return trusted.GetSessionTranscriptPageValidated(ctx, request, authorization)
}

func handleLatestCommittedAssistantFinalAnswer(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.SessionLatestCommittedAssistantFinalAnswerRequest],
	authorization apicontract.AuthorizedSessionInActiveProject,
) (serverapi.SessionLatestCommittedAssistantFinalAnswerResponse, error) {
	trusted, ok := g.deps.SessionViewClient().(apicontract.SessionViewTrustedService)
	if !ok {
		return serverapi.SessionLatestCommittedAssistantFinalAnswerResponse{}, errors.New("Session View trusted service is required")
	}
	return trusted.GetLatestCommittedAssistantFinalAnswerValidated(ctx, request, authorization)
}

func handleSessionPersistInputDraft(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.SessionPersistInputDraftRequest],
	authorization apicontract.AuthorizedSessionInActiveProject,
) (serverapi.SessionPersistInputDraftResponse, error) {
	trusted, ok := g.deps.SessionLifecycleClient().(apicontract.SessionLifecycleTrustedService)
	if !ok {
		return serverapi.SessionPersistInputDraftResponse{}, errors.New("Session Lifecycle trusted service is required")
	}
	return trusted.PersistInputDraftValidated(ctx, request, authorization)
}

func handleSessionInitialInput(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.SessionInitialInputRequest],
	authorization apicontract.OptionalAuthorizedSessionInActiveProject,
) (serverapi.SessionInitialInputResponse, error) {
	trusted, ok := g.deps.SessionLifecycleClient().(apicontract.SessionLifecycleTrustedService)
	if !ok {
		return serverapi.SessionInitialInputResponse{}, errors.New("Session Lifecycle trusted service is required")
	}
	return trusted.GetInitialInputValidated(ctx, request, authorization)
}

func handleSessionResolveTransition(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.SessionResolveTransitionRequest],
	authorization apicontract.OptionalAuthorizedSessionInActiveProject,
) (serverapi.SessionResolveTransitionResponse, error) {
	trusted, ok := g.deps.SessionLifecycleClient().(apicontract.SessionLifecycleTrustedService)
	if !ok {
		return serverapi.SessionResolveTransitionResponse{}, errors.New("Session Lifecycle trusted service is required")
	}
	return trusted.ResolveTransitionValidated(ctx, request, authorization)
}

func handleWorktreeWorkspaceList(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.WorktreeWorkspaceListRequest],
	binding apicontract.AuthorizedProjectWorkspaceBinding,
) (serverapi.WorktreeWorkspaceListResponse, error) {
	trusted, ok := g.deps.WorktreeClient().(apicontract.WorktreeTrustedService)
	if !ok {
		return serverapi.WorktreeWorkspaceListResponse{}, errors.New("Worktree service does not implement trusted Workspace list")
	}
	return trusted.ListWorkspaceWorktreesValidated(ctx, request, binding)
}

func trustedWorktreeService(g *Gateway) (apicontract.WorktreeTrustedService, error) {
	trusted, ok := g.deps.WorktreeClient().(apicontract.WorktreeTrustedService)
	if !ok {
		return nil, errors.New("Worktree trusted service is required")
	}
	return trusted, nil
}

func handleWorktreeStatus(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeStatusRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeStatusResponse, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeStatusResponse{}, err
	}
	return trusted.GetWorktreeStatusValidated(ctx, request, authorization)
}

func handleWorktreeList(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeListRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeListResponse, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeListResponse{}, err
	}
	return trusted.ListWorktreesValidated(ctx, request, authorization)
}

func handleWorktreeSelectorResolve(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeSelectorPreviewRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeSelectorPreviewResponse, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeSelectorPreviewResponse{}, err
	}
	return trusted.ResolveWorktreeSelectorValidated(ctx, request, authorization)
}

func handleWorktreeDeletePreview(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeDeletePreviewRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeDeletePreviewResponse, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeDeletePreviewResponse{}, err
	}
	return trusted.PreviewWorktreeDeleteValidated(ctx, request, authorization)
}

func handleWorktreeCreateTargetResolve(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeCreateTargetResolveRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeCreateTargetResolveResponse, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeCreateTargetResolveResponse{}, err
	}
	return trusted.ResolveWorktreeCreateTargetValidated(ctx, request, authorization)
}

func handleWorktreeCreate(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeCreateRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeCreateResponse, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	return trusted.CreateWorktreeValidated(ctx, request, authorization)
}

func handleWorktreeEnter(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeEnterRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeScheduledAcknowledgement, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, err
	}
	return trusted.EnterWorktreeValidated(ctx, request, authorization)
}

func handleWorktreeLeave(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeLeaveRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeScheduledAcknowledgement, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeScheduledAcknowledgement{}, err
	}
	return trusted.LeaveWorktreeValidated(ctx, request, authorization)
}

func handleWorktreeDelete(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeDeleteRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeDeleteResult, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeDeleteResult{}, err
	}
	return trusted.DeleteWorktreeValidated(ctx, request, authorization)
}

func handleSessionWorkspaceRetarget(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.SessionRetargetWorkspaceRequest],
	constraint apicontract.AttachedProjectConstraint,
) (serverapi.SessionRetargetWorkspaceResponse, error) {
	trusted, ok := g.deps.SessionLifecycleClient().(apicontract.SessionLifecycleTrustedService)
	if !ok {
		return serverapi.SessionRetargetWorkspaceResponse{}, errors.New("Session Lifecycle trusted service is required")
	}
	return trusted.RetargetSessionWorkspaceValidated(ctx, request, constraint)
}

func handleAttachSession(
	_ context.Context,
	_ *Gateway,
	state *connectionState,
	_ apicontract.Validated[protocol.AttachSessionRequest],
	attachment apicontract.AuthorizedSessionAttachment,
) (protocol.AttachResponse, error) {
	response, err := protocol.SessionAttachResponse(
		attachment.ProjectID,
		attachment.WorkspaceID,
		attachment.CanonicalRoot,
		attachment.SessionID.String(),
	)
	if err != nil {
		return protocol.AttachResponse{}, err
	}
	state.attachedProject = attachment.ProjectID
	state.attachedWorkspaceID = attachment.WorkspaceID
	state.attachedWorkspaceRoot = attachment.CanonicalRoot
	state.attachedSession = &attachment.SessionID
	return response, nil
}

func handleAuthGetBootstrapStatus(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.AuthGetBootstrapStatusRequest], _ noAuthorizationFacts) (serverapi.AuthGetBootstrapStatusResponse, error) {
	trusted, ok := g.deps.AuthBootstrapClient().(apicontract.AuthBootstrapTrustedService)
	if !ok {
		return serverapi.AuthGetBootstrapStatusResponse{}, serverapi.ErrServerAuthRequired
	}
	return trusted.GetAuthBootstrapStatusValidated(ctx, request)
}

func handleAuthCompleteBootstrap(ctx context.Context, g *Gateway, state *connectionState, request apicontract.Validated[serverapi.AuthCompleteBootstrapRequest], _ noAuthorizationFacts) (serverapi.AuthCompleteBootstrapResponse, error) {
	trusted, ok := g.deps.AuthBootstrapClient().(apicontract.AuthBootstrapTrustedService)
	if !ok {
		return serverapi.AuthCompleteBootstrapResponse{}, serverapi.ErrServerAuthRequired
	}
	response, err := trusted.CompleteAuthBootstrapValidated(ctx, request)
	if err == nil {
		state.noAuthAccepted = response.NoAuthSelected
	}
	return response, err
}

func handleAuthAcknowledgeNoAuth(ctx context.Context, g *Gateway, state *connectionState, request apicontract.Validated[serverapi.AuthAcknowledgeNoAuthRequest], _ noAuthorizationFacts) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
	trusted, ok := g.deps.AuthBootstrapClient().(apicontract.AuthBootstrapTrustedService)
	if !ok {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, serverapi.ErrServerAuthRequired
	}
	response, err := trusted.AcknowledgeNoAuthValidated(ctx, request)
	if err == nil {
		state.noAuthAccepted = response.NoAuthSelected
	}
	return response, err
}

func handleServerReadiness(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.ServerReadinessRequest], _ noAuthorizationFacts) (serverapi.ServerReadinessResponse, error) {
	trusted, ok := g.deps.ServerStatusClient().(apicontract.ServerStatusTrustedService)
	if !ok {
		return serverapi.ServerReadinessResponse{}, errors.New("server status trusted service is required")
	}
	response, err := trusted.GetServerReadinessValidated(ctx, request)
	if err != nil {
		return serverapi.ServerReadinessResponse{}, err
	}
	response.ServerID = g.identity.ServerID
	response.ProtocolVersion = g.identity.ProtocolVersion
	return response, nil
}

func handleProcessGet(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.ProcessGetRequest],
	authorization apicontract.AuthorizedProcessInActiveProject,
) (serverapi.ProcessGetResponse, error) {
	trusted, ok := g.deps.ProcessViewClient().(apicontract.ProcessViewTrustedService)
	if !ok {
		return serverapi.ProcessGetResponse{}, errors.New("Process View trusted service is required")
	}
	return trusted.GetProcessValidated(ctx, request, authorization)
}

func handleProcessKill(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.ProcessKillRequest],
	authorization apicontract.AuthorizedProcessInActiveProject,
) (serverapi.ProcessKillResponse, error) {
	trusted, ok := g.deps.ProcessControlClient().(apicontract.ProcessControlTrustedService)
	if !ok {
		return serverapi.ProcessKillResponse{}, errors.New("Process Control trusted service is required")
	}
	return trusted.KillProcessValidated(ctx, request, authorization)
}

func handleProcessInlineOutput(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	request apicontract.Validated[serverapi.ProcessInlineOutputRequest],
	authorization apicontract.AuthorizedProcessInActiveProject,
) (serverapi.ProcessInlineOutputResponse, error) {
	trusted, ok := g.deps.ProcessControlClient().(apicontract.ProcessControlTrustedService)
	if !ok {
		return serverapi.ProcessInlineOutputResponse{}, errors.New("Process Control trusted service is required")
	}
	return trusted.GetInlineOutputValidated(ctx, request, authorization)
}

func prepareSessionRuntimeActivate(source serverapi.SessionRuntimeActivateRequest, state *connectionState) serverapi.SessionRuntimeActivateRequest {
	prepared := source
	prepared.OwnerID = strings.TrimSpace(state.runtimeOwnerID)
	return prepared
}

func prepareSessionRuntimeRelease(source serverapi.SessionRuntimeReleaseRequest, state *connectionState) serverapi.SessionRuntimeReleaseRequest {
	prepared := source
	prepared.OwnerID = strings.TrimSpace(state.runtimeOwnerID)
	return prepared
}

func handleSessionRuntimeActivate(ctx context.Context, g *Gateway, state *connectionState, validated apicontract.Validated[serverapi.SessionRuntimeActivateRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeActivateResponse, error) {
	trusted, ok := g.deps.SessionRuntimeClient().(apicontract.SessionRuntimeTrustedService)
	if !ok {
		return serverapi.SessionRuntimeActivateResponse{}, errors.New("Session Runtime trusted service is required")
	}
	response, err := trusted.ActivateSessionRuntimeValidated(ctx, validated, authorization)
	if err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	if err := response.ValidateForSession(validated.Value().SessionID); err != nil {
		return serverapi.SessionRuntimeActivateResponse{}, err
	}
	state.recordOwnedRuntime(response.Attachment)
	return response, nil
}

func handleSessionRuntimeRelease(ctx context.Context, g *Gateway, state *connectionState, validated apicontract.Validated[serverapi.SessionRuntimeReleaseRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.SessionRuntimeReleaseResponse, error) {
	trusted, ok := g.deps.SessionRuntimeClient().(apicontract.SessionRuntimeTrustedService)
	if !ok {
		return serverapi.SessionRuntimeReleaseResponse{}, errors.New("Session Runtime trusted service is required")
	}
	response, err := trusted.ReleaseSessionRuntimeValidated(ctx, validated, authorization)
	if err == nil && (response.Released || validated.Value().DropOwner) {
		state.removeOwnedRuntime(validated.Value().Attachment)
	}
	return response, err
}

func executeOnboardingFinalize(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
	route := mustInboundRoute(protocol.MethodOnboardingFinalize, apicontract.KindUnary)
	request, err := decodeInboundRequest[serverapi.OnboardingFinalizeRequest](g, route, requestDecoderOnboardingFinalize, wire.Params)
	if err != nil {
		return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
	}
	response, err := apicontract.WithValidated(request, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.OnboardingFinalizeRequest]) (serverapi.OnboardingFinalizeResponse, error) {
		client := g.deps.OnboardingFinalizeClient()
		trusted, ok := client.(apicontract.OnboardingFinalizeTrustedService)
		if !ok {
			return serverapi.OnboardingFinalizeResponse{}, validatedOwnerError{cause: errors.New("onboarding finalize trusted service is required")}
		}
		response, err := trusted.FinalizeOnboardingValidated(ctx, validated)
		if err != nil {
			return serverapi.OnboardingFinalizeResponse{}, validatedOwnerError{cause: err}
		}
		return response, nil
	})
	if err != nil {
		return responseForValidationOrOwnerError(wire.ID, err)
	}
	return handlerSuccessResponse(wire.ID, response)
}

func executeSessionExecutionEnvironment(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
	route := mustInboundRoute(protocol.MethodSessionGetExecutionEnvironment, apicontract.KindUnary)
	request, err := decodeInboundRequest[serverapi.SessionExecutionEnvironmentRequest](g, route, requestDecoderSessionExecutionEnvironment, wire.Params)
	if err != nil {
		return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
	}
	response, err := apicontract.WithValidated(request, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.SessionExecutionEnvironmentRequest]) (serverapi.SessionExecutionEnvironmentResponse, error) {
		authorization, err := authorizeSessionActiveProject(
			func(req serverapi.SessionExecutionEnvironmentRequest) string { return req.SessionID.String() },
		)(ctx, g, state, validated)
		if err != nil {
			return serverapi.SessionExecutionEnvironmentResponse{}, validatedOwnerError{cause: err}
		}
		trusted, ok := g.deps.SessionViewClient().(apicontract.SessionViewTrustedService)
		if !ok {
			return serverapi.SessionExecutionEnvironmentResponse{}, validatedOwnerError{cause: errors.New("Session View trusted service is required")}
		}
		response, err := trusted.GetSessionExecutionEnvironmentValidated(ctx, validated, authorization)
		if err != nil {
			return serverapi.SessionExecutionEnvironmentResponse{}, validatedOwnerError{cause: err}
		}
		return response, nil
	})
	if err != nil {
		return responseForValidationOrOwnerError(wire.ID, err)
	}
	return handlerSuccessResponse(wire.ID, response)
}

func executeSessionPlan(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
	route := mustInboundRoute(wire.Method, apicontract.KindUnary)
	request, err := decodeInboundRequest[serverapi.SessionPlanRequest](g, route, requestDecoderDefault, wire.Params)
	if err != nil {
		return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
	}
	response, err := apicontract.WithValidated(request, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.SessionPlanRequest]) (serverapi.SessionPlanResponse, error) {
		if err := newRoutePolicyExecutor(g).authorizeScope(ctx, state, route, validated.Value()); err != nil {
			return serverapi.SessionPlanResponse{}, validatedOwnerError{cause: err}
		}
		client, err := g.sessionLaunchClientForState(ctx, state)
		if err != nil {
			return serverapi.SessionPlanResponse{}, validatedOwnerError{cause: err}
		}
		trusted, ok := client.(apicontract.SessionLaunchTrustedService)
		if !ok {
			return serverapi.SessionPlanResponse{}, validatedOwnerError{cause: errors.New("Session Launch trusted service is required")}
		}
		response, err := trusted.PlanSessionValidated(ctx, validated)
		if err != nil {
			return serverapi.SessionPlanResponse{}, validatedOwnerError{cause: err}
		}
		return response, nil
	})
	if err != nil {
		return responseForValidationOrOwnerError(wire.ID, err)
	}
	return handlerSuccessResponse(wire.ID, response)
}

func executeWorkspaceChatDraft(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
	route := mustInboundRoute(wire.Method, apicontract.KindUnary)
	request, err := decodeInboundRequest[serverapi.WorkspaceChatDraftRequest](g, route, requestDecoderDefault, wire.Params)
	if err != nil {
		return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
	}
	response, err := apicontract.WithValidated(request, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[serverapi.WorkspaceChatDraftRequest]) (serverapi.WorkspaceChatDraftResponse, error) {
		if err := newRoutePolicyExecutor(g).authorizeScope(ctx, state, route, validated.Value()); err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, validatedOwnerError{cause: err}
		}
		client, err := g.sessionLaunchClientForState(ctx, state)
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, validatedOwnerError{cause: err}
		}
		trusted, ok := client.(apicontract.SessionLaunchTrustedService)
		if !ok {
			return serverapi.WorkspaceChatDraftResponse{}, validatedOwnerError{cause: errors.New("Session Launch trusted service is required")}
		}
		response, err := trusted.WorkspaceChatDraftValidated(ctx, validated)
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, validatedOwnerError{cause: err}
		}
		return response, nil
	})
	if err != nil {
		return responseForValidationOrOwnerError(wire.ID, err)
	}
	return handlerSuccessResponse(wire.ID, response)
}

func handleWorkspaceChatMaterialize(
	ctx context.Context,
	g *Gateway,
	state *connectionState,
	request apicontract.Validated[serverapi.WorkspaceChatMaterializeRequest],
	_ noAuthorizationFacts,
) (serverapi.WorkspaceChatMaterializeResponse, error) {
	client, err := g.sessionLaunchClientForState(ctx, state)
	if err != nil {
		return serverapi.WorkspaceChatMaterializeResponse{}, err
	}
	trusted, ok := client.(apicontract.SessionLaunchTrustedService)
	if !ok {
		return serverapi.WorkspaceChatMaterializeResponse{}, errors.New("Session Launch trusted service is required")
	}
	return trusted.MaterializeWorkspaceChatValidated(ctx, request)
}

func executeAttachProject(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
	route := mustInboundRoute(protocol.MethodAttachProject, apicontract.KindUnary)
	request, err := decodeInboundRequest[protocol.AttachProjectRequest](g, route, requestDecoderDefault, wire.Params)
	if err != nil {
		return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
	}
	response, err := apicontract.WithValidated(request, apicontract.SemanticValidationRequired, func(validated apicontract.Validated[protocol.AttachProjectRequest]) (protocol.AttachResponse, error) {
		params := validated.Value()
		if err := g.deps.ProjectExists(ctx, params.ProjectID); err != nil {
			return protocol.AttachResponse{}, validatedOwnerError{cause: err}
		}
		workspaceID, root, err := g.resolveAttachedProjectWorkspace(ctx, params)
		if err != nil {
			return protocol.AttachResponse{}, validatedOwnerError{cause: err}
		}
		state.attachedProject = params.ProjectID
		state.attachedWorkspaceID = workspaceID
		state.attachedWorkspaceRoot = root
		state.attachedSession = nil
		return protocol.ProjectAttachResponseForRequest(params, workspaceID, root)
	})
	if err != nil {
		return responseForValidationOrOwnerError(wire.ID, err)
	}
	return handlerSuccessResponse(wire.ID, response)
}

func erasedInboundExecutable(route apicontract.Route) inboundExecutableRoute {
	policy := apicontract.SemanticValidationRequired
	if route.ValidationMethod == apicontract.ValidationMethodNone {
		policy = apicontract.NoSemanticValidation
	}
	executable := inboundExecutableRoute{
		route:                 route,
		requestType:           route.RequestType,
		authorizationType:     reflect.TypeOf(noAuthorizationFacts{}),
		authorizationRule:     inboundAuthorizationClassificationForRoute(route).rule,
		handlerClassification: inboundHandlerClassificationForRoute(route),
		validation:            policy,
		validator:             route.ValidationMethod,
		decoder:               requestDecoderDefault,
	}
	if route.Kind == apicontract.KindUnary {
		executable.executeUnary = func(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
			decoded, err := route.DecodeRequest(wire.Params)
			if err != nil {
				return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
			}
			response, err := route.WithValidated(decoded, policy, func(_ any, params any) (any, error) {
				if err := newRoutePolicyExecutor(g).authorizeScope(ctx, state, route, params); err != nil {
					return nil, validatedOwnerError{cause: err}
				}
				return gatewayUnaryHandlerEntries[wire.Method](g, ctx, state, wire), nil
			})
			if err != nil {
				return responseForValidationOrOwnerError(wire.ID, err)
			}
			return response.(protocol.Response)
		}
	}
	return executable
}

var inboundExecutableRoutes = declareInboundExecutableRoutes()

func validateInboundExecutableRegistry() error {
	return validateInboundExecutableRegistryEntries(inboundExecutableRoutes)
}

func validateInboundExecutableRegistryEntries(executables map[string]inboundExecutableRoute) error {
	for method, executable := range executables {
		if method != executable.route.Method {
			return fmt.Errorf("inbound executable key %q does not match route method %q", method, executable.route.Method)
		}
		if executable.route.Kind == apicontract.KindNotification {
			return fmt.Errorf("notification %q has an inbound executable", method)
		}
		if executable.requestType != executable.route.RequestType {
			return fmt.Errorf("route %q request type mismatch", method)
		}
		switch executable.validation {
		case apicontract.SemanticValidationRequired:
			if executable.validator == requestValidatorNone {
				return fmt.Errorf("semantic route %q has no validator", method)
			}
		case apicontract.NoSemanticValidation:
			if executable.validator != requestValidatorNone {
				return fmt.Errorf("no-semantic route %q bypasses an available validator", method)
			}
		default:
			return fmt.Errorf("route %q has unknown validation policy", method)
		}
		classification := inboundAuthorizationClassificationForRoute(executable.route)
		if executable.authorizationType != classification.authorizationType {
			return fmt.Errorf(
				"route %q scope %q authorization type = %v, want %v for rule %q",
				method,
				executable.route.Scope,
				executable.authorizationType,
				classification.authorizationType,
				classification.rule,
			)
		}
		if executable.authorizationRule != classification.rule {
			return fmt.Errorf("route %q authorization rule = %q, want %q", method, executable.authorizationRule, classification.rule)
		}
		handler := inboundHandlerClassificationForRoute(executable.route)
		if executable.handlerClassification != handler {
			return fmt.Errorf("route %q handler classification = %+v, want %+v", method, executable.handlerClassification, handler)
		}
	}
	for _, route := range apicontract.Routes() {
		classification, declared := inboundScopeAuthorizationClassifications[route.Scope]
		if !declared {
			return fmt.Errorf("route %q scope %q has no authorization classification", route.Method, route.Scope)
		}
		_, registered := executables[route.Method]
		if route.Kind == apicontract.KindNotification {
			if classification.rule != scopeAuthorizationOutboundNotification {
				return fmt.Errorf("notification %q has authorization rule %q", route.Method, classification.rule)
			}
			if registered {
				return fmt.Errorf("notification %q has executable registration", route.Method)
			}
			continue
		}
		if !registered {
			return fmt.Errorf("inbound route %q has no executable registration", route.Method)
		}
	}
	return nil
}
