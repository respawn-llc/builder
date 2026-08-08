package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"core/server/auth"
	"core/server/metadata"
	"core/shared/apicontract"
	"core/shared/invariant"
	"core/shared/llmerrors"
	"core/shared/protocol"
	"core/shared/rpcwire"
	"core/shared/runtimeids"
	"core/shared/serverapi"

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
	deps     GatewayDependencies
	identity protocol.ServerIdentity
	debug    bool
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

type GatewayDependencyAvailability interface {
	RouteDependencyAvailable(apicontract.Dependency) error
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
	SessionViewClient() apicontract.SessionViewService
	SessionLifecycleClient() apicontract.SessionLifecycleService
	SessionRuntimeClient() apicontract.SessionRuntimeService
	SessionTranscriptClient() apicontract.SessionTranscriptService
	SessionLaunchClientForProjectWorkspace(context.Context, string, string) (apicontract.SessionLaunchService, error)
	SessionLaunchClientForProjectWorkspaceID(context.Context, string, string) (apicontract.SessionLaunchService, error)
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
	ProcessOutputClient() apicontract.ProcessOutputService
}

type GatewayWorktreeDependencies interface {
	WorktreeClient() apicontract.WorktreeService
}

var gatewaySubscriptionMethods = protocolSubscriptionMethodSet()

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

const gatewayOrdinaryRequestOperation = "gateway.ordinary_request"

type gatewayRequestPanicDiagnostic struct {
	Operation string
	Method    string
	RequestID string
	Cause     any
	Stack     string
}

func (p gatewayRequestPanicDiagnostic) Error() string {
	return fmt.Sprintf(
		"gateway request panic operation=%q method=%q request_id=%q cause=%v\nstack:\n%s",
		p.Operation,
		p.Method,
		p.RequestID,
		p.Cause,
		p.Stack,
	)
}

var gatewayProgressHandlers = routeHandlersForKind(apicontract.KindProgress, gatewayProgressHandlerEntries)

func RuntimeLiveControlRoutesExecutable() bool {
	for _, method := range []string{
		protocol.MethodRuntimeLiveSteer,
		protocol.MethodRuntimeLiveStop,
		protocol.MethodRuntimeLiveWait,
		protocol.MethodRuntimeLiveWatch,
	} {
		if _, ok := gatewayUnaryHandlers[method]; !ok {
			return false
		}
	}
	return true
}

func protocolSubscriptionMethodSet() map[string]struct{} {
	methods := apicontract.SubscriptionMethods()
	set := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		set[strings.TrimSpace(method)] = struct{}{}
	}
	return set
}

type connectionState struct {
	handshakeDone         bool
	clientCapabilities    protocol.ClientCapabilities
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
	protocol.MethodProcessSubscribeOutput:                (*Gateway).serveProcessOutputSubscription,
	protocol.MethodAttentionNotificationSubscribe:        (*Gateway).serveAttentionNotificationSubscription,
	protocol.MethodAttentionSessionNotificationSubscribe: (*Gateway).serveSessionAttentionNotificationSubscription,
	protocol.MethodWorkflowSubscribe:                     (*Gateway).serveWorkflowSubscription,
	protocol.MethodWorkflowSubscribeProject:              (*Gateway).serveWorkflowProjectSubscription,
	protocol.MethodWorktreeSetupSubscribe:                (*Gateway).serveWorktreeSetupSubscription,
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

func gatewayProgressHandlerForMethod(method string) (gatewayProgressHandler, apicontract.Route, bool) {
	route, ok := apicontract.RouteByMethod(strings.TrimSpace(method))
	if !ok || route.Kind != apicontract.KindProgress {
		return nil, apicontract.Route{}, false
	}
	handler, ok := gatewayProgressHandlers[route.Method]
	return handler, route, ok
}

func NewGateway(deps GatewayDependencies, identity protocol.ServerIdentity) (*Gateway, error) {
	if isNilGatewayDependencies(deps) {
		return nil, ErrGatewayDependenciesRequired
	}
	if strings.TrimSpace(identity.ProtocolVersion) == "" {
		return nil, errors.New("server identity is required")
	}
	debugMode := invariant.NewPolicy().Mode() == invariant.ModePanic
	if debugDeps, ok := deps.(interface{ DebugEnabled() bool }); ok {
		debugMode = debugMode || debugDeps.DebugEnabled()
	}
	return &Gateway{deps: deps, identity: identity, debug: debugMode}, nil
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
			req, err := receiveRequest(connCtx, conn)
			if err != nil {
				return
			}
			if !g.serveGatewayRequest(conn, connCtx, state, req, gatewayRequestScheduleFor(req)) {
				return
			}
			continue
		}

		select {
		case admission <- struct{}{}:
		case <-connCtx.Done():
			return
		}

		req, err := receiveRequest(connCtx, conn)
		if err != nil {
			<-admission
			return
		}
		schedule := gatewayRequestScheduleFor(req)
		if schedule.kind != gatewayRequestScheduleOrdinary {
			<-admission
			ordinary.Wait()
			if !g.serveGatewayRequest(conn, connCtx, state, req, schedule) {
				stop()
				return
			}
			continue
		}

		ordinary.Add(1)
		go func(req protocol.Request, schedule gatewayRequestSchedule) {
			defer ordinary.Done()
			defer func() { <-admission }()
			g.serveOrdinaryGatewayRequest(conn, connCtx, state, req, schedule, stop)
		}(req, schedule)
	}
}

func gatewayRequestScheduleFor(req protocol.Request) gatewayRequestSchedule {
	if handler, route, ok := gatewayProgressHandlerForMethod(req.Method); ok {
		return gatewayRequestSchedule{
			kind:          gatewayRequestScheduleProgress,
			progress:      handler,
			progressRoute: route,
		}
	}
	if _, ok := gatewaySubscriptionMethods[strings.TrimSpace(req.Method)]; ok {
		return gatewayRequestSchedule{kind: gatewayRequestScheduleSubscription}
	}
	if isGatewayExclusiveRequest(req) {
		return gatewayRequestSchedule{kind: gatewayRequestScheduleExclusive}
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

func (g *Gateway) serveOrdinaryGatewayRequest(conn rpcwire.Conn, ctx context.Context, state *connectionState, req protocol.Request, schedule gatewayRequestSchedule, stop func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			stack := string(debug.Stack())
			slog.Error(
				"gateway request handler panicked",
				"method", req.Method,
				"request_id", req.ID,
				"panic", recovered,
				"stack", stack,
			)
			stop()
			if g.debug {
				panic(gatewayRequestPanicDiagnostic{
					Operation: gatewayOrdinaryRequestOperation,
					Method:    req.Method,
					RequestID: req.ID,
					Cause:     recovered,
					Stack:     stack,
				})
			}
		}
	}()
	if !g.serveGatewayRequest(conn, ctx, state, req, schedule) {
		stop()
	}
}

func isGatewayExclusiveRequest(req protocol.Request) bool {
	if req.Method == protocol.MethodHandshake {
		return true
	}
	route, ok := apicontract.RouteByMethod(req.Method)
	if !ok {
		return false
	}
	switch route.Dependency {
	case apicontract.DependencyAuthBootstrap, apicontract.DependencyAuthStatus:
		return true
	}
	switch route.Scope {
	case apicontract.ScopeAttachProject, apicontract.ScopeAttachSession:
		return true
	}
	return false
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
			ClientRequestID: uuid.NewString(),
			Attachment:      attachment,
			DropOwner:       true,
			ClosePolicy:     serverapi.SessionRuntimeReleaseClosePolicyCloseIfIdle,
			OwnerID:         ownerID,
		})
		cancel()
	}
}

func (g *Gateway) dispatch(ctx context.Context, state *connectionState, req protocol.Request) protocol.Response {
	if err := req.Validate(); err != nil {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, err.Error())
	}
	if req.Method != protocol.MethodHandshake && !state.handshakeDone {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidRequest, "handshake is required before other methods")
	}
	route, ok := apicontract.RouteByMethod(req.Method)
	if !ok {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method))
	}
	if availability, ok := g.deps.(GatewayDependencyAvailability); ok {
		if err := availability.RouteDependencyAvailable(route.Dependency); err != nil {
			return responseForError(req.ID, err)
		}
	}
	if err := newRoutePolicyExecutor(g).requireAuth(ctx, state, req.Method); err != nil {
		return responseForError(req.ID, err)
	}
	handler, ok := gatewayUnaryHandlers[req.Method]
	if !ok {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method))
	}
	if _, resp, failed := g.preflightRouteRequest(ctx, state, route, req); failed {
		return resp
	}
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
			return event.Frame.Request(), nil
		}
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
	if errors.Is(err, context.Canceled) || errors.Is(err, serverapi.ErrRuntimeOperationCanceled) {
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
	if errors.Is(err, serverapi.ErrWorktreeBlocked) {
		return protocol.ErrCodeWorktreeBlocked, message
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
	if errors.Is(err, serverapi.ErrUnsupportedProvider) {
		return protocol.ErrCodeUnsupportedProvider, message
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
