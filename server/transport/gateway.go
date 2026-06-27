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
	"time"

	"core/server/auth"
	"core/server/metadata"
	rpccontract "core/shared/apicontract"
	"core/shared/client"
	"core/shared/protocol"
	"core/shared/rpcwire"
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
}

type GatewayDependencies interface {
	GatewayServerStatusDependencies
	GatewayAuthDependencies
	GatewayProjectDependencies
	GatewaySessionDependencies
	GatewayRuntimeDependencies
	GatewayPromptDependencies
	GatewayProcessDependencies
	GatewayWorktreeDependencies
}

type GatewayServerStatusDependencies interface {
	ServerStatusClient() client.ServerStatusClient
}

type GatewayAuthDependencies interface {
	AuthManager() *auth.Manager
	AuthBootstrapClient() client.AuthBootstrapClient
	AuthStatusClient() client.AuthStatusClient
	ServerAuthRequired() bool
}

type GatewayProjectDependencies interface {
	MetadataStore() *metadata.Store
	ProjectID() string
	ProjectExists(context.Context, string) error
	ProjectViewClient() client.ProjectViewClient
	WorkflowClient() client.WorkflowClient
}

type GatewaySessionDependencies interface {
	SessionBelongsToProject(context.Context, string, string) error
	SessionViewClient() client.SessionViewClient
	SessionLifecycleClient() client.SessionLifecycleClient
	SessionRuntimeClient() client.SessionRuntimeClient
	SessionActivityClient() client.SessionActivityClient
	SessionLaunchClientForProjectWorkspace(context.Context, string, string) (client.SessionLaunchClient, error)
	SessionLaunchClientForProjectWorkspaceID(context.Context, string, string) (client.SessionLaunchClient, error)
	RunPromptClientForProjectWorkspace(context.Context, string, string) (client.RunPromptClient, error)
	RunPromptClientForProjectWorkspaceID(context.Context, string, string) (client.RunPromptClient, error)
}

type GatewayRuntimeDependencies interface {
	RuntimeControlClient() client.RuntimeControlClient
}

type GatewayPromptDependencies interface {
	AskViewClient() client.AskViewClient
	ApprovalViewClient() client.ApprovalViewClient
	PromptControlClient() client.PromptControlClient
	PromptActivityClient() client.PromptActivityClient
}

type GatewayProcessDependencies interface {
	ProcessViewClient() client.ProcessViewClient
	ProcessControlClient() client.ProcessControlClient
	ProcessOutputClient() client.ProcessOutputClient
}

type GatewayWorktreeDependencies interface {
	WorktreeClient() client.WorktreeClient
}

var gatewaySubscriptionMethods = protocolSubscriptionMethodSet()

type gatewayUnaryHandler func(g *Gateway, ctx context.Context, state *connectionState, req protocol.Request) protocol.Response

var gatewayUnaryHandlers = routeHandlersForKind(rpccontract.KindUnary, gatewayUnaryHandlerEntries)

var gatewayProgressHandlerEntries = map[string]gatewayProgressHandler{
	protocol.MethodRunPrompt: (*Gateway).serveRunPrompt,
}

type gatewayProgressHandler func(g *Gateway, conn rpcwire.Conn, ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) bool

var gatewayProgressHandlers = routeHandlersForKind(rpccontract.KindProgress, gatewayProgressHandlerEntries)

func protocolSubscriptionMethodSet() map[string]struct{} {
	methods := rpccontract.SubscriptionMethods()
	set := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		set[strings.TrimSpace(method)] = struct{}{}
	}
	return set
}

type connectionState struct {
	handshakeDone         bool
	attachedProject       string
	attachedWorkspaceID   string
	attachedWorkspaceRoot string
	attachedSession       string
	runtimeOwnerID        string
	ownedRuntimes         map[string]struct{}
}

type gatewaySubscriptionHandler func(g *Gateway, conn rpcwire.Conn, ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request)

var gatewaySubscriptionHandlerEntries = map[string]gatewaySubscriptionHandler{
	protocol.MethodSessionSubscribeActivity: (*Gateway).serveSessionActivitySubscription,
	protocol.MethodProcessSubscribeOutput:   (*Gateway).serveProcessOutputSubscription,
	protocol.MethodPromptSubscribeActivity:  (*Gateway).servePromptActivitySubscription,
	protocol.MethodWorkflowSubscribe:        (*Gateway).serveWorkflowSubscription,
	protocol.MethodWorkflowSubscribeProject: (*Gateway).serveWorkflowProjectSubscription,
}

var gatewaySubscriptionHandlers = routeHandlersForKind(rpccontract.KindSubscription, gatewaySubscriptionHandlerEntries)

func routeHandlersForKind[T any](kind rpccontract.Kind, entries map[string]T) map[string]T {
	handlers := make(map[string]T)
	for _, route := range rpccontract.Routes() {
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

func gatewayProgressHandlerForMethod(method string) (gatewayProgressHandler, rpccontract.Route, bool) {
	route, ok := rpccontract.RouteByMethod(strings.TrimSpace(method))
	if !ok || route.Kind != rpccontract.KindProgress {
		return nil, rpccontract.Route{}, false
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
	return &Gateway{deps: deps, identity: identity}, nil
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
	defer func() { _ = conn.Close() }()
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
		case <-conn.Closed():
		}
		cancel()
	}()
	state := &connectionState{runtimeOwnerID: uuid.NewString()}
	defer g.cleanupConnectionRuntimes(state)
	for {
		req, err := receiveRequest(connCtx, conn)
		if err != nil {
			return
		}
		if handler, route, ok := gatewayProgressHandlerForMethod(req.Method); ok {
			if !handler(g, conn, connCtx, state, route, req) {
				return
			}
			continue
		}
		if _, ok := gatewaySubscriptionMethods[strings.TrimSpace(req.Method)]; ok {
			g.serveSubscription(conn, connCtx, state, req)
			return
		}
		resp := g.dispatch(connCtx, state, req)
		if !sendResponse(connCtx, conn, resp) {
			return
		}
	}
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
	for _, sessionID := range owned {
		ctx, cancel := context.WithTimeout(context.Background(), gatewayRuntimeCleanupTimeout)
		_, _ = client.ReleaseSessionRuntime(ctx, serverapi.SessionRuntimeReleaseRequest{
			ClientRequestID: uuid.NewString(),
			SessionID:       sessionID,
			OnlyIfIdle:      true,
			DropOwner:       true,
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
	if err := newRoutePolicyExecutor(g).requireAuth(ctx, req.Method); err != nil {
		return responseForError(req.ID, err)
	}
	handler, ok := gatewayUnaryHandlers[req.Method]
	if !ok {
		return protocol.NewErrorResponse(req.ID, protocol.ErrCodeMethodNotFound, fmt.Sprintf("method %q not found", req.Method))
	}
	route, ok := rpccontract.RouteByMethod(req.Method)
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
	if validator, ok := any(params).(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error())
		}
	}
	resp, err := handler(params)
	if err != nil {
		return responseForError(req.ID, err)
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
	return protocol.StreamCompleteParams{Code: code, Message: message}
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
