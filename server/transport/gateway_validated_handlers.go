package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type noAuthorizationFacts struct{}

type requestDecoderKind uint8

const (
	requestDecoderDefault requestDecoderKind = iota
	requestDecoderOnboardingFinalize
	requestDecoderSessionExecutionEnvironment
)

func validatedUnaryHandler[Req any, Authz any, Resp any](
	method string,
	policy apicontract.ValidationPolicy,
	decoder requestDecoderKind,
	prepare func(Req, *connectionState) Req,
	authorize func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (Authz, error),
	handle func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req], Authz) (Resp, error),
) gatewayUnaryHandler {
	route := mustGatewayRoute(method, apicontract.KindUnary)
	if prepare == nil {
		prepare = func(req Req, _ *connectionState) Req { return req }
	}
	return func(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
		request, err := decodeInboundRequest[Req](g, route, decoder, wire.Params)
		if err != nil {
			return protocol.NewErrorResponse(wire.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
		response, err := apicontract.WithValidated(prepare(request, state), policy, func(validated apicontract.Validated[Req]) (Resp, error) {
			authorization, err := authorize(ctx, g, state, validated)
			if err != nil {
				var zero Resp
				return zero, validatedOwnerError{cause: err}
			}
			response, err := handle(ctx, g, state, validated, authorization)
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
}

func validatedGatewayClientCall[Req any, Resp any, Trusted any](
	getClient func(GatewayDependencies) any,
	call func(Trusted, context.Context, apicontract.Validated[Req]) (Resp, error),
) gatewayUnaryHandler {
	return func(g *Gateway, ctx context.Context, state *connectionState, wire protocol.Request) protocol.Response {
		route := mustGatewayRoute(wire.Method, apicontract.KindUnary)
		policy := apicontract.SemanticValidationRequired
		if apicontract.ValidationMethodFor(*new(Req)) == apicontract.ValidationMethodNone {
			policy = apicontract.NoSemanticValidation
		}
		return validatedUnaryHandler(
			wire.Method,
			policy,
			requestDecoderDefault,
			nil,
			authorizeRouteScope[Req](route),
			func(ctx context.Context, g *Gateway, _ *connectionState, req apicontract.Validated[Req], _ noAuthorizationFacts) (Resp, error) {
				trusted, ok := getClient(g.deps).(Trusted)
				if !ok {
					var zero Resp
					return zero, fmt.Errorf("%s trusted service is required", route.Dependency)
				}
				return call(trusted, ctx, req)
			},
		)(g, ctx, state, wire)
	}
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

func handleAskListPending(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	req apicontract.Validated[serverapi.AskListPendingBySessionRequest],
	_ apicontract.AuthorizedSessionInActiveProject,
) (serverapi.AskListPendingBySessionResponse, error) {
	trusted, ok := g.deps.AskViewClient().(apicontract.AskViewTrustedService)
	if !ok {
		return serverapi.AskListPendingBySessionResponse{}, errors.New("Ask View trusted service is required")
	}
	return trusted.ListPendingAsksBySessionValidated(ctx, req)
}

func handleApprovalListPending(
	ctx context.Context,
	g *Gateway,
	_ *connectionState,
	req apicontract.Validated[serverapi.ApprovalListPendingBySessionRequest],
	_ apicontract.AuthorizedSessionInActiveProject,
) (serverapi.ApprovalListPendingBySessionResponse, error) {
	trusted, ok := g.deps.ApprovalViewClient().(apicontract.ApprovalViewTrustedService)
	if !ok {
		return serverapi.ApprovalListPendingBySessionResponse{}, errors.New("Approval View trusted service is required")
	}
	return trusted.ListPendingApprovalsBySessionValidated(ctx, req)
}

func decodeInboundRequest[Req any](g *Gateway, route apicontract.Route, decoder requestDecoderKind, raw json.RawMessage) (Req, error) {
	var decoded any
	var err error
	switch decoder {
	case requestDecoderDefault:
		var request Req
		if len(raw) != 0 {
			err = json.Unmarshal(raw, &request)
		}
		decoded = request
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
		panic(fmt.Sprintf("route %q decoder returned unexpected type %T", route.Method, decoded))
	}
	return request, nil
}

func mustGatewayRoute(method string, kind apicontract.Kind) apicontract.Route {
	route, ok := apicontract.RouteByMethod(method)
	if !ok {
		panic(fmt.Sprintf("gateway handler %q has no shared route metadata", method))
	}
	if route.Kind != kind {
		panic(fmt.Sprintf("gateway handler %q kind = %q, want %q", method, route.Kind, kind))
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

func authorizeGoalSession[Req any](sessionID func(Req) string) func(context.Context, *Gateway, *connectionState, apicontract.Validated[Req]) (noAuthorizationFacts, error) {
	return func(ctx context.Context, g *Gateway, state *connectionState, validated apicontract.Validated[Req]) (noAuthorizationFacts, error) {
		err := g.requireGoalSessionAccess(ctx, state, sessionID(validated.Value()))
		return noAuthorizationFacts{}, err
	}
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

func handleWorktreeCreate(ctx context.Context, g *Gateway, _ *connectionState, request apicontract.Validated[serverapi.WorktreeCreateRequest], authorization apicontract.AuthorizedSessionInActiveProject) (serverapi.WorktreeCreateResponse, error) {
	trusted, err := trustedWorktreeService(g)
	if err != nil {
		return serverapi.WorktreeCreateResponse{}, err
	}
	return trusted.CreateWorktreeValidated(ctx, request, authorization)
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
	route := mustGatewayRoute(protocol.MethodOnboardingFinalize, apicontract.KindUnary)
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
	route := mustGatewayRoute(protocol.MethodSessionGetExecutionEnvironment, apicontract.KindUnary)
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
	route := mustGatewayRoute(wire.Method, apicontract.KindUnary)
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
	route := mustGatewayRoute(wire.Method, apicontract.KindUnary)
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
	route := mustGatewayRoute(protocol.MethodAttachProject, apicontract.KindUnary)
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
