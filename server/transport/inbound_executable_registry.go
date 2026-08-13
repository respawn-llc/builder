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

type requestValidatorKind = apicontract.ValidationMethod

const requestValidatorNone = apicontract.ValidationMethodNone

type requestDecoderKind uint8

const (
	requestDecoderDefault requestDecoderKind = iota
	requestDecoderOnboardingFinalize
	requestDecoderSessionExecutionEnvironment
)

type inboundExecutableRoute struct {
	route               apicontract.Route
	requestType         reflect.Type
	authorizationType   reflect.Type
	validation          apicontract.ValidationPolicy
	validator           requestValidatorKind
	decoder             requestDecoderKind
	executeUnary        func(*Gateway, context.Context, *connectionState, protocol.Request) protocol.Response
	executeProgress     gatewayProgressHandler
	executeSubscription gatewaySubscriptionHandler
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
		route:             route,
		requestType:       reflect.TypeOf((*Req)(nil)).Elem(),
		authorizationType: reflect.TypeOf((*Authz)(nil)).Elem(),
		validation:        policy,
		validator:         route.ValidationMethod,
		decoder:           decoder,
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
		route:             route,
		requestType:       reflect.TypeOf((*Req)(nil)).Elem(),
		authorizationType: reflect.TypeOf((*Authz)(nil)).Elem(),
		validation:        policy,
		validator:         route.ValidationMethod,
		decoder:           decoder,
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
		route:             route,
		requestType:       reflect.TypeOf((*Req)(nil)).Elem(),
		authorizationType: reflect.TypeOf((*Authz)(nil)).Elem(),
		validation:        policy,
		validator:         route.ValidationMethod,
		decoder:           decoder,
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

	registerRequiredSessionUnary(protocol.MethodWorktreeStatus, activeProjectSessionUnary[serverapi.WorktreeStatusRequest](protocol.MethodWorktreeStatus, func(req serverapi.WorktreeStatusRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodWorktreeList, activeProjectSessionUnary[serverapi.WorktreeListRequest](protocol.MethodWorktreeList, func(req serverapi.WorktreeListRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodWorktreeSelectorResolve, activeProjectSessionUnary[serverapi.WorktreeSelectorPreviewRequest](protocol.MethodWorktreeSelectorResolve, func(req serverapi.WorktreeSelectorPreviewRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodWorktreeDeletePreview, activeProjectSessionUnary[serverapi.WorktreeDeletePreviewRequest](protocol.MethodWorktreeDeletePreview, func(req serverapi.WorktreeDeletePreviewRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodWorktreeCreateTargetResolve, activeProjectSessionUnary[serverapi.WorktreeCreateTargetResolveRequest](protocol.MethodWorktreeCreateTargetResolve, func(req serverapi.WorktreeCreateTargetResolveRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodWorktreeCreate, activeProjectSessionUnary[serverapi.WorktreeCreateRequest](protocol.MethodWorktreeCreate, func(req serverapi.WorktreeCreateRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodWorktreeEnter, activeProjectSessionUnary[serverapi.WorktreeEnterRequest](protocol.MethodWorktreeEnter, func(req serverapi.WorktreeEnterRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodWorktreeLeave, activeProjectSessionUnary[serverapi.WorktreeLeaveRequest](protocol.MethodWorktreeLeave, func(req serverapi.WorktreeLeaveRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodWorktreeDelete, activeProjectSessionUnary[serverapi.WorktreeDeleteRequest](protocol.MethodWorktreeDelete, func(req serverapi.WorktreeDeleteRequest) string { return req.SessionID }))

	registerRequiredSessionUnary(protocol.MethodRuntimeSetSessionName, activeProjectSessionUnary[serverapi.RuntimeSetSessionNameRequest](protocol.MethodRuntimeSetSessionName, func(req serverapi.RuntimeSetSessionNameRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetThinkingLevel, activeProjectSessionUnary[serverapi.RuntimeSetThinkingLevelRequest](protocol.MethodRuntimeSetThinkingLevel, func(req serverapi.RuntimeSetThinkingLevelRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetFastModeEnabled, activeProjectSessionUnary[serverapi.RuntimeSetFastModeEnabledRequest](protocol.MethodRuntimeSetFastModeEnabled, func(req serverapi.RuntimeSetFastModeEnabledRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetReviewerEnabled, activeProjectSessionUnary[serverapi.RuntimeSetReviewerEnabledRequest](protocol.MethodRuntimeSetReviewerEnabled, func(req serverapi.RuntimeSetReviewerEnabledRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetAutoCompactionEnabled, activeProjectSessionUnary[serverapi.RuntimeSetAutoCompactionEnabledRequest](protocol.MethodRuntimeSetAutoCompactionEnabled, func(req serverapi.RuntimeSetAutoCompactionEnabledRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeSetQuestionsEnabled, activeProjectSessionUnary[serverapi.RuntimeSetQuestionsEnabledRequest](protocol.MethodRuntimeSetQuestionsEnabled, func(req serverapi.RuntimeSetQuestionsEnabledRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeAppendCommittedEntry, activeProjectSessionUnary[serverapi.RuntimeAppendCommittedEntryRequest](protocol.MethodRuntimeAppendCommittedEntry, func(req serverapi.RuntimeAppendCommittedEntryRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeShouldCompactBeforeUserMessage, activeProjectSessionUnary[serverapi.RuntimeShouldCompactBeforeUserMessageRequest](protocol.MethodRuntimeShouldCompactBeforeUserMessage, func(req serverapi.RuntimeShouldCompactBeforeUserMessageRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeSubmitUserTurn, activeProjectSessionUnary[serverapi.RuntimeSubmitUserTurnRequest](protocol.MethodRuntimeSubmitUserTurn, func(req serverapi.RuntimeSubmitUserTurnRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeSubmitUserShellCommand, activeProjectSessionUnary[serverapi.RuntimeSubmitUserShellCommandRequest](protocol.MethodRuntimeSubmitUserShellCommand, func(req serverapi.RuntimeSubmitUserShellCommandRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeCompactContext, activeProjectSessionUnary[serverapi.RuntimeCompactContextRequest](protocol.MethodRuntimeCompactContext, func(req serverapi.RuntimeCompactContextRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeInterrupt, activeProjectSessionUnary[serverapi.RuntimeInterruptRequest](protocol.MethodRuntimeInterrupt, func(req serverapi.RuntimeInterruptRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeDiscardQueuedUserMessage, activeProjectSessionUnary[serverapi.RuntimeDiscardQueuedUserMessageRequest](protocol.MethodRuntimeDiscardQueuedUserMessage, func(req serverapi.RuntimeDiscardQueuedUserMessageRequest) string { return req.SessionID }))
	registerRequiredSessionUnary(protocol.MethodRuntimeRecordPromptHistory, activeProjectSessionUnary[serverapi.RuntimeRecordPromptHistoryRequest](protocol.MethodRuntimeRecordPromptHistory, func(req serverapi.RuntimeRecordPromptHistoryRequest) string { return req.SessionID }))

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
	return trusted.LiveSteerValidated(ctx, request)
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
	return trusted.LiveWaitValidated(ctx, request)
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
		route:             route,
		requestType:       route.RequestType,
		authorizationType: reflect.TypeOf(noAuthorizationFacts{}),
		validation:        policy,
		validator:         route.ValidationMethod,
		decoder:           requestDecoderDefault,
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
	for method, executable := range inboundExecutableRoutes {
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
		switch executable.route.Scope {
		case apicontract.ScopeSessionActiveProject:
			if executable.authorizationType != reflect.TypeOf(apicontract.AuthorizedSessionInActiveProject{}) {
				return fmt.Errorf(
					"route %q scope %q authorization type = %v, want AuthorizedSessionInActiveProject",
					method,
					executable.route.Scope,
					executable.authorizationType,
				)
			}
		case apicontract.ScopeSessionActiveProjectIfSet:
			if executable.authorizationType != reflect.TypeOf(apicontract.OptionalAuthorizedSessionInActiveProject{}) {
				return fmt.Errorf(
					"route %q scope %q authorization type = %v, want OptionalAuthorizedSessionInActiveProject",
					method,
					executable.route.Scope,
					executable.authorizationType,
				)
			}
		}
	}
	for _, route := range apicontract.Routes() {
		_, registered := inboundExecutableRoutes[route.Method]
		if route.Kind == apicontract.KindNotification {
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
