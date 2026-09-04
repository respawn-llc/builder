package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"core/server/auth"
	"core/server/chatcontext"
	"core/server/metadata"
	"core/shared/apicontract"
	"core/shared/invariant"
	"core/shared/jsoncontract"
	"core/shared/llmerrors"
	"core/shared/protoapi"
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/serverjsoncontract"

	"github.com/google/uuid"
)

// ErrGatewayDependenciesRequired is returned by NewGateway when the supplied
// dependencies are nil. Callers match it via errors.Is.
var ErrGatewayDependenciesRequired = errors.New("gateway dependencies are required")

// canceledByClientMessage is the normalized protocol message used when a
// context.Canceled error carries no actionable wording. It is the source of
// truth for the cancellation message surfaced to clients.
const canceledByClientMessage = "request canceled by client"

type Gateway struct {
	deps                            GatewayDependencies
	identity                        protocol.ServerIdentity
	registration                    gatewayRegistration
	sessionExecutionRequestContract serverjsoncontract.SessionExecutionEnvironmentRequest
}

type GatewayDependencies interface {
	GatewayServerStatusDependencies
	GatewayAuthDependencies
	GatewayCapabilityFactsDependencies
	GatewayOnboardingDependencies
	GatewayProjectDependencies
	GatewaySessionDependencies
	GatewayRuntimeDependencies
	GatewayPromptDependencies
	GatewayPromptCommandDependencies
	GatewayProcessDependencies
	GatewayWorktreeDependencies
}

type GatewayStartupLifecycle interface {
	RequireCoreActive() error
}

type GatewayServerStatusDependencies interface {
	ServerStatusClient() apicontract.ServerStatusService
}

type GatewayAuthDependencies interface {
	AuthManager() *auth.Manager
	AuthBootstrapClient() apicontract.AuthBootstrapService
	AuthStatusClient() apicontract.AuthStatusService
	ServerAuthRequired() bool
}

type GatewayCapabilityFactsDependencies interface {
	CapabilityFactsClient() apicontract.CapabilityFactsService
}

type GatewayOnboardingDependencies interface {
	OnboardingFinalizeClient() apicontract.OnboardingFinalizeService
}

type GatewayProjectDependencies interface {
	MetadataStore() *metadata.Store
	ProjectID() string
	ProjectExists(context.Context, string) error
	ProjectViewClient() apicontract.ProjectViewService
	WorkflowClient() apicontract.WorkflowService
}

type GatewaySessionDependencies interface {
	SessionBelongsToProject(context.Context, string, string) error
	ChatSettingsClient() apicontract.ChatSettingsService
	SessionViewClient() apicontract.SessionViewService
	SessionLifecycleClient() apicontract.SessionLifecycleService
	SessionRuntimeClient() apicontract.SessionRuntimeService
	SessionTranscriptClient() apicontract.SessionTranscriptService
	SessionLaunchClientForProjectWorkspace(context.Context, string, string) (apicontract.SessionLaunchService, error)
	SessionLaunchClientForProjectWorkspaceID(context.Context, string, string) (apicontract.SessionLaunchService, error)
	WorkspaceChatContextOwnerForProjectWorkspace(context.Context, string, string) (chatcontext.WorkspaceOwner, error)
	WorkspaceChatContextOwnerForProjectWorkspaceID(context.Context, string, string) (chatcontext.WorkspaceOwner, error)
	SessionChatContextOwner() chatcontext.SessionOwner
	RunPromptClientForProjectWorkspace(context.Context, string, string) (apicontract.RunPromptService, error)
	RunPromptClientForProjectWorkspaceID(context.Context, string, string) (apicontract.RunPromptService, error)
}

type GatewayRuntimeDependencies interface {
	RuntimeControlClient() apicontract.RuntimeControlService
	RuntimeLiveControlClient() apicontract.RuntimeLiveControlService
}

type GatewayPromptDependencies interface {
	AskViewClient() apicontract.AskViewService
	ApprovalViewClient() apicontract.ApprovalViewService
	PromptControlClient() apicontract.PromptControlService
	AttentionNotificationClient() apicontract.AttentionNotificationService
}

type GatewayPromptCommandDependencies interface {
	PromptCommandCatalogClientForProjectWorkspace(context.Context, string, string) (apicontract.PromptCommandCatalogService, error)
}

type GatewayProcessDependencies interface {
	ProcessViewClient() apicontract.ProcessViewService
	ProcessControlClient() apicontract.ProcessControlService
}

type GatewayWorktreeDependencies interface {
	WorktreeClient() apicontract.WorktreeService
}

type gatewayUnaryHandler func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response

var gatewayUnaryHandlers = routeHandlersForKind(apicontract.KindUnary, gatewayUnaryHandlerEntries)

var gatewayProgressHandlerEntries = map[string]gatewayProgressHandler{
	protocol.MethodRunPrompt: (*Gateway).serveRunPrompt,
}

type gatewayProgressHandler func(g *Gateway, conn rpcwire.Conn, ctx context.Context, state *connectionState, route apicontract.Route, req protocol.Request) bool

type gatewayRequestScheduleKind uint8

const (
	gatewayRequestScheduleOrdinary gatewayRequestScheduleKind = iota
	gatewayRequestScheduleExclusive
	gatewayRequestScheduleProgress
	gatewayRequestScheduleSubscription
)

type gatewayRequestSchedule struct {
	kind          gatewayRequestScheduleKind
	progress      gatewayProgressHandler
	progressRoute apicontract.Route
}

type gatewayEstablishedRequest struct {
	legacy  *protocol.Request
	binary  *gatewayBinaryRequest
	failure *sharedpb.TransportFailure
}

var gatewayProgressHandlers = routeHandlersForKind(apicontract.KindProgress, gatewayProgressHandlerEntries)

type connectionState struct {
	handshakeDone         bool
	noAuthAccepted        bool
	attachedProject       string
	attachedWorkspaceID   string
	attachedWorkspaceRoot string
	attachedSession       *runtimeids.SessionID
	runtimeOwnerID        string
	ownedRuntimesMu       sync.Mutex
	ownedRuntimes         map[serverapi.SessionRuntimeAttachment]struct{}
}

type gatewaySubscriptionHandler func(g *Gateway, conn rpcwire.Conn, ctx context.Context, state *connectionState, route apicontract.Route, req protocol.Request)

var gatewaySubscriptionHandlerEntries = map[string]gatewaySubscriptionHandler{
	protocol.MethodSessionSubscribeTranscript:            (*Gateway).serveSessionTranscriptSubscription,
	protocol.MethodSessionQuestionHistorySubscribe:       (*Gateway).serveQuestionHistorySubscription,
	protocol.MethodAttentionNotificationSubscribe:        (*Gateway).serveAttentionNotificationSubscription,
	protocol.MethodAttentionSessionNotificationSubscribe: (*Gateway).serveSessionAttentionNotificationSubscription,
	protocol.MethodPromptFollowUpWatch:                   (*Gateway).servePromptFollowUpSubscription,
	protocol.MethodWorkflowSubscribe:                     (*Gateway).serveWorkflowSubscription,
	protocol.MethodWorkflowSubscribeProject:              (*Gateway).serveWorkflowProjectSubscription,
}

var gatewaySubscriptionHandlers = routeHandlersForKind(apicontract.KindSubscription, gatewaySubscriptionHandlerEntries)

func routeHandlersForKind[T any](kind apicontract.Kind, entries map[string]T) map[string]T {
	handlers := make(map[string]T)
	for _, route := range apicontract.Routes() {
		if route.Kind != kind {
			continue
		}
		handler, ok := entries[route.Method]
		if !ok {
			continue
		}
		handlers[route.Method] = handler
	}
	return handlers
}

func gatewayProgressHandlerForRoute(route apicontract.Route) (gatewayProgressHandler, bool) {
	if route.Kind != apicontract.KindProgress {
		return nil, false
	}
	handler, ok := gatewayProgressHandlers[route.Method]
	if !ok {
		return nil, false
	}
	return handler, true
}

func NewGateway(deps GatewayDependencies, identity protocol.ServerIdentity) (*Gateway, error) {
	if isNilGatewayDependencies(deps) {
		return nil, ErrGatewayDependenciesRequired
	}
	if strings.TrimSpace(identity.ProtocolVersion) == "" {
		return nil, errors.New("server identity is required")
	}
	registration, err := productionGatewayRegistration()
	if err != nil {
		return nil, fmt.Errorf("build Gateway registration: %w", err)
	}
	if err := registration.Validate(); err != nil {
		return nil, fmt.Errorf("validate Gateway registration: %w", err)
	}
	debugMode := invariant.NewPolicy().Mode() == invariant.ModePanic
	if debugDeps, ok := deps.(interface{ DebugEnabled() bool }); ok {
		debugMode = debugMode || debugDeps.DebugEnabled()
	}
	preparer := jsoncontract.NewPreparer(debugMode)
	sessionExecutionRequestContract, err := serverjsoncontract.PrepareSessionExecutionEnvironmentRequest(preparer)
	if err != nil {
		return nil, err
	}
	return &Gateway{
		deps:                            deps,
		identity:                        identity,
		registration:                    registration,
		sessionExecutionRequestContract: sessionExecutionRequestContract,
	}, nil
}

func isNilGatewayDependencies(deps GatewayDependencies) bool {
	if deps == nil {
		return true
	}
	value := reflect.ValueOf(deps)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (g *Gateway) Handler() http.Handler {
	return rpcwire.NewWebSocketTransport().Handler(g.handleConn)
}

func (g *Gateway) handleConn(ctx context.Context, conn rpcwire.Conn) {
	connCtx, cancel := context.WithCancel(ctx)
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			_ = conn.Close()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			stop()
		case <-conn.Closed():
			stop()
		}
	}()
	state := &connectionState{runtimeOwnerID: uuid.NewString()}
	const ordinaryAdmissionLimit = 16
	admission := make(chan struct{}, ordinaryAdmissionLimit)
	var ordinary sync.WaitGroup
	defer func() {
		stop()
		ordinary.Wait()
		g.cleanupConnectionRuntimes(state)
	}()
	for {
		if !state.handshakeDone {
			request, err := g.receiveEstablishedRequest(connCtx, conn)
			if err != nil {
				return
			}
			if request.failure != nil {
				_ = sendTransportFailure(connCtx, conn, request.failure)
				return
			}
			if request.binary == nil || !isBinaryHandshake(request.binary.binding) {
				return
			}
			if !g.serveBinaryRequest(conn, connCtx, state, *request.binary) {
				return
			}
			if !state.handshakeDone {
				return
			}
			continue
		}

		request, err := g.receiveEstablishedRequest(connCtx, conn)
		if err != nil {
			return
		}
		if request.failure != nil {
			if !sendTransportFailure(connCtx, conn, request.failure) {
				stop()
				return
			}
			continue
		}
		schedule := g.gatewayRequestScheduleForEstablished(request)
		if schedule.kind == gatewayRequestScheduleExclusive {
			ordinary.Wait()
			if !g.serveEstablishedRequest(conn, connCtx, state, request, schedule) {
				stop()
				return
			}
			continue
		}
		if schedule.kind == gatewayRequestScheduleProgress || schedule.kind == gatewayRequestScheduleSubscription {
			if !g.serveEstablishedRequest(conn, connCtx, state, request, schedule) {
				stop()
				return
			}
			continue
		}

		select {
		case admission <- struct{}{}:
		case <-connCtx.Done():
			return
		}

		ordinary.Add(1)
		go func(request gatewayEstablishedRequest, schedule gatewayRequestSchedule) {
			defer ordinary.Done()
			defer func() { <-admission }()
			g.serveOrdinaryEstablishedRequest(conn, connCtx, state, request, schedule, stop)
		}(request, schedule)
	}
}

func isBinaryHandshake(binding gatewayBinaryBinding) bool {
	return binding.operation.Descriptor.Parent().Name() == "ConnectionService" &&
		binding.operation.Descriptor.Name() == "Handshake"
}

func (g *Gateway) gatewayRequestScheduleForEstablished(request gatewayEstablishedRequest) gatewayRequestSchedule {
	if request.legacy != nil {
		return g.gatewayRequestScheduleFor(*request.legacy)
	}
	if request.binary != nil &&
		request.binary.binding.operation.Options.Kind == sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION {
		return gatewayRequestSchedule{kind: gatewayRequestScheduleSubscription}
	}
	if request.binary == nil {
		panic("established Gateway request is required")
	}
	binding := request.binary.binding
	if binding.operation.Options.Kind != sharedpb.OperationKind_OPERATION_KIND_UNARY {
		panic(fmt.Sprintf("unsupported binary operation kind %s", binding.operation.Options.Kind))
	}
	if isGatewayExclusiveBinaryBinding(binding) {
		return gatewayRequestSchedule{kind: gatewayRequestScheduleExclusive}
	}
	return gatewayRequestSchedule{kind: gatewayRequestScheduleOrdinary}
}

func (g *Gateway) gatewayRequestScheduleFor(req protocol.Request) gatewayRequestSchedule {
	operation, route, known := g.registration.LegacyOperation(strings.TrimSpace(req.Method))
	if !known {
		return gatewayRequestSchedule{kind: gatewayRequestScheduleOrdinary}
	}
	switch operation.Options.Kind {
	case sharedpb.OperationKind_OPERATION_KIND_PROGRESS:
		route.Scope = routeScopePolicy(operation.Options.ScopePolicy)
		handler, ok := gatewayProgressHandlerForRoute(route)
		if !ok {
			panic(fmt.Sprintf("legacy progress operation %q has no handler", operation.Name))
		}
		return gatewayRequestSchedule{
			kind:          gatewayRequestScheduleProgress,
			progress:      handler,
			progressRoute: route,
		}
	case sharedpb.OperationKind_OPERATION_KIND_SUBSCRIPTION:
		return gatewayRequestSchedule{kind: gatewayRequestScheduleSubscription}
	case sharedpb.OperationKind_OPERATION_KIND_UNARY:
		if isGatewayExclusiveOperation(operation, route) {
			return gatewayRequestSchedule{kind: gatewayRequestScheduleExclusive}
		}
	}
	return gatewayRequestSchedule{kind: gatewayRequestScheduleOrdinary}
}

func (g *Gateway) serveGatewayRequest(conn rpcwire.Conn, ctx context.Context, state *connectionState, req protocol.Request, schedule gatewayRequestSchedule) bool {
	switch schedule.kind {
	case gatewayRequestScheduleProgress:
		return schedule.progress(g, conn, ctx, state, schedule.progressRoute, req)
	case gatewayRequestScheduleSubscription:
		g.serveSubscription(conn, ctx, state, req)
		return false
	case gatewayRequestScheduleOrdinary, gatewayRequestScheduleExclusive:
		return sendResponse(ctx, conn, g.dispatch(ctx, state, req))
	default:
		panic(fmt.Sprintf("unknown Gateway request schedule kind %d", schedule.kind))
	}
}

func (g *Gateway) serveEstablishedRequest(
	conn rpcwire.Conn,
	ctx context.Context,
	state *connectionState,
	request gatewayEstablishedRequest,
	schedule gatewayRequestSchedule,
) bool {
	if request.legacy != nil {
		return g.serveGatewayRequest(conn, ctx, state, *request.legacy, schedule)
	}
	if request.binary != nil {
		return g.serveBinaryRequest(conn, ctx, state, *request.binary)
	}
	panic("established Gateway request is required")
}

func (g *Gateway) serveOrdinaryGatewayRequest(
	conn rpcwire.Conn,
	ctx context.Context,
	state *connectionState,
	req protocol.Request,
	schedule gatewayRequestSchedule,
	stop func(),
) {
	if !g.serveGatewayRequest(conn, ctx, state, req, schedule) {
		stop()
	}
}

func (g *Gateway) serveOrdinaryEstablishedRequest(
	conn rpcwire.Conn,
	ctx context.Context,
	state *connectionState,
	request gatewayEstablishedRequest,
	schedule gatewayRequestSchedule,
	stop func(),
) {
	if !g.serveEstablishedRequest(conn, ctx, state, request, schedule) {
		stop()
	}
}

func isGatewayExclusiveOperation(operation protoapi.Operation, _ apicontract.Route) bool {
	switch operation.Options.ScopePolicy {
	case sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_PROJECT,
		sharedpb.ScopePolicy_SCOPE_POLICY_ATTACH_SESSION:
		return true
	}
	return false
}

func (g *Gateway) requireCoreActive() error {
	lifecycle, ok := g.deps.(GatewayStartupLifecycle)
	if !ok {
		return nil
	}
	return lifecycle.RequireCoreActive()
}

const gatewayRuntimeCleanupTimeout = 3 * time.Second

func (g *Gateway) cleanupConnectionRuntimes(state *connectionState) {
	owned := state.takeOwnedRuntimes()
	if len(owned) == 0 || g == nil || isNilGatewayDependencies(g.deps) {
		return
	}
	client := g.deps.SessionRuntimeClient()
	if client == nil {
		return
	}
	ownerID := strings.TrimSpace(state.runtimeOwnerID)
	for _, attachment := range owned {
		ctx, cancel := context.WithTimeout(context.Background(), gatewayRuntimeCleanupTimeout)
		_, _ = client.ReleaseSessionRuntime(ctx, serverapi.SessionRuntimeReleaseRequest{
			Attachment:  attachment,
			DropOwner:   true,
			ClosePolicy: serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
			OwnerID:     ownerID,
		})
		cancel()
	}
}

func (g *Gateway) dispatch(ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
	if err := req.Validate(); err != nil {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, err.Error())
	}
	if !state.handshakeDone {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, "handshake is required before other methods")
	}
	operation, route, ok := g.registration.LegacyOperation(req.Method)
	if !ok {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method))
	}
	if err := g.requireCoreActive(); err != nil {
		return responseForError(req.ID, err)
	}
	if err := newRoutePolicyExecutor(g).requireAuthenticationStage(
		ctx,
		state,
		operation.Options.AuthenticationStage,
	); err != nil {
		return responseForError(req.ID, err)
	}
	route.Scope = routeScopePolicy(operation.Options.ScopePolicy)
	if _, resp, failed := g.preflightRouteRequest(ctx, state, route, req); failed {
		return resp
	}
	handler := gatewayUnaryHandlers[req.Method]
	return handler(g, ctx, state, req)
}

func decodeAndHandle[TReq any, TResp any](req protocol.Request, handler func(TReq) (TResp, error)) protocol.Response {
	params, err := decodeParams[TReq](req.Params)
	if err != nil {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error())
	}
	var validationErr error
	if validator, ok := any(params).(interface{ ValidateRPC() error }); ok {
		validationErr = validator.ValidateRPC()
	} else if validator, ok := any(params).(interface{ Validate() error }); ok {
		validationErr = validator.Validate()
	}
	if validationErr != nil {
		var rpcErr interface {
			RPCErrorCode() int
			RPCErrorData() json.RawMessage
		}
		if errors.As(validationErr, &rpcErr) {
			return responseForError(req.ID, validationErr)
		}
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, validationErr.Error())
	}
	resp, err := handler(params)
	if err != nil {
		return responseForError(req.ID, err)
	}
	if validator, ok := any(resp).(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return responseForError(req.ID, fmt.Errorf("handler returned an invalid response: %w", err))
		}
	}
	return protocol.NewSuccessResponse(req.ID, resp)
}

func receiveRequest(ctx context.Context, conn rpcwire.Conn) (protocol.Request, error) {
	for {
		select {
		case <-ctx.Done():
			return protocol.Request{}, ctx.Err()
		case event, ok := <-conn.Events():
			if !ok {
				return protocol.Request{}, io.EOF
			}
			if event.Err != nil {
				return protocol.Request{}, event.Err
			}
			request, err := event.Frame.DecodeRequest()
			if err != nil {
				return protocol.Request{}, err
			}
			return request, nil
		}
	}
}

func (g *Gateway) receiveEstablishedRequest(ctx context.Context, conn rpcwire.Conn) (gatewayEstablishedRequest, error) {
	frame, err := receiveFrame(ctx, conn)
	if err != nil {
		return gatewayEstablishedRequest{}, err
	}
	switch frame.Kind {
	case rpcwire.FrameText:
		request, err := frame.DecodeRequest()
		if err != nil {
			return gatewayEstablishedRequest{}, err
		}
		return gatewayEstablishedRequest{legacy: &request}, nil
	case rpcwire.FrameBinary:
		request, failure := g.resolveBinaryRequest(frame.Payload)
		if failure != nil {
			return gatewayEstablishedRequest{failure: failure}, nil
		}
		return gatewayEstablishedRequest{binary: request}, nil
	default:
		return gatewayEstablishedRequest{}, fmt.Errorf("unsupported Gateway frame kind %d", frame.Kind)
	}
}

func receiveFrame(ctx context.Context, conn rpcwire.Conn) (rpcwire.Frame, error) {
	select {
	case <-ctx.Done():
		return rpcwire.Frame{}, ctx.Err()
	case event, ok := <-conn.Events():
		if !ok {
			return rpcwire.Frame{}, io.EOF
		}
		if event.Err != nil {
			return rpcwire.Frame{}, event.Err
		}
		return event.Frame, nil
	}
}

func sendResponse(ctx context.Context, conn rpcwire.Conn, resp protocol.Response) bool {
	return conn.Send(ctx, rpcwire.FrameFromResponse(resp)) == nil
}

func responseForError(id string, err error) protocol.Response {
	var structured protocol.StructuredRPCError
	if errors.As(err, &structured) {
		return protocol.NewErrorResponseWithData(id, structured.RPCErrorCode(), err.Error(), structured.RPCErrorData())
	}
	code, message := protocolError(err)
	return protocol.NewErrorResponse(id, code, message)
}

func protocolError(err error) (int, string) {
	if err == nil {
		return protocol.ErrCodeInternalError, "internal error"
	}
	message := strings.TrimSpace(err.Error())
	if errors.Is(err, context.Canceled) {
		if message == "" || message == context.Canceled.Error() {
			message = canceledByClientMessage
		}
		return protocol.ErrCodeRequestCanceled, message
	}
	if errors.Is(err, llmerrors.ErrModelStreamStalled) {
		return protocol.ErrCodeModelStreamStalled, message
	}
	if errors.Is(err, serverapi.ErrStreamGap) {
		return protocol.ErrCodeStreamGap, message
	}
	if errors.Is(err, serverapi.ErrWorkspaceNotRegistered) {
		return protocol.ErrCodeWorkspaceNotRegistered, message
	}
	if errors.Is(err, serverapi.ErrProjectNotFound) {
		return protocol.ErrCodeProjectNotFound, message
	}
	if errors.Is(err, serverapi.ErrProjectUnavailable) {
		return protocol.ErrCodeProjectUnavailable, message
	}
	if errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		return protocol.ErrCodeRuntimeUnavailable, message
	}
	if errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		return protocol.ErrCodeRuntimeNoActiveRun, message
	}
	if errors.Is(err, serverapi.ErrRuntimeNoFinalAnswer) {
		return protocol.ErrCodeRuntimeNoFinalAnswer, message
	}
	if errors.Is(err, serverapi.ErrStreamUnavailable) {
		return protocol.ErrCodeStreamUnavailable, message
	}
	if errors.Is(err, serverapi.ErrStreamFailed) {
		return protocol.ErrCodeStreamFailed, message
	}
	if errors.Is(err, serverapi.ErrPromptNotFound) {
		return protocol.ErrCodePromptNotFound, message
	}
	if errors.Is(err, serverapi.ErrPromptAlreadyResolved) {
		return protocol.ErrCodePromptResolved, message
	}
	if errors.Is(err, serverapi.ErrPromptUnsupported) {
		return protocol.ErrCodePromptUnsupported, message
	}
	if errors.Is(err, serverapi.ErrWorkflowTaskNotFound) {
		return protocol.ErrCodeWorkflowTaskNotFound, message
	}
	if errors.Is(err, serverapi.ErrWorkflowTaskCompleteTargetNotFound) {
		return protocol.ErrCodeWorkflowTaskCompleteNotFound, message
	}
	if errors.Is(err, serverapi.ErrWorkflowTaskCompleteSelectorAmbiguous) {
		return protocol.ErrCodeWorkflowTaskCompleteAmbiguous, message
	}
	var resumeConflict *serverapi.WorkflowTaskResumeConflictError
	if errors.As(err, &resumeConflict) {
		return resumeConflict.RPCErrorCode(), message
	}
	if errors.Is(err, serverapi.ErrServerAuthRequired) || errors.Is(err, auth.ErrAuthNotConfigured) {
		return protocol.ErrCodeAuthRequired, message
	}
	return protocol.ErrCodeInternalError, message
}

func streamCompleteParams(err error) protocol.StreamCompleteParams {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return protocol.StreamCompleteParams{}
	}
	code, message := protocolError(err)
	params := protocol.StreamCompleteParams{Code: code, Message: message}
	if reason, ok := serverapi.TranscriptCloseReasonOf(err); ok {
		params.TranscriptCloseReason = string(reason)
	}
	return params
}

func decodeParams[T any](raw json.RawMessage) (T, error) {
	var zero T
	if len(raw) == 0 {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, fmt.Errorf("decode params: %w", err)
	}
	return out, nil
}
