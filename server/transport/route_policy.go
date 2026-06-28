package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"core/server/auth"
	"core/server/session"
	rpccontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/protocol"
	"core/shared/serverapi"
)

type routePolicyExecutor struct {
	gateway *Gateway
}

var errSessionOutsideActiveProject = errors.New("session outside active project")
var errActiveProjectRequired = errors.New("active project required")

type activeProjectRequiredError struct{}

func (e activeProjectRequiredError) Error() string {
	return "project attachment is required"
}

func (e activeProjectRequiredError) Is(target error) bool {
	return target == errActiveProjectRequired
}

type sessionOutsideActiveProjectError struct {
	sessionID string
}

func (e sessionOutsideActiveProjectError) Error() string {
	return fmt.Sprintf("session %q not available", e.sessionID)
}

func (e sessionOutsideActiveProjectError) Is(target error) bool {
	return target == errSessionOutsideActiveProject
}

func newRoutePolicyExecutor(gateway *Gateway) routePolicyExecutor {
	return routePolicyExecutor{gateway: gateway}
}

type routePreflightResult struct {
	params any
	resp   protocol.Response
	failed bool
}

func (e routePolicyExecutor) preflight(ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) routePreflightResult {
	params, err := decodeRouteParams(route, req.Params)
	if err != nil {
		return routePreflightResult{resp: protocol.NewErrorResponse(req.ID, protocol.ErrCodeInvalidParams, err.Error()), failed: true}
	}
	if err := e.authorizeScope(ctx, state, route, params); err != nil {
		var routeErr gatewayRouteError
		if errors.As(err, &routeErr) {
			return routePreflightResult{resp: protocol.NewErrorResponse(req.ID, routeErr.code, routeErr.message), failed: true}
		}
		return routePreflightResult{resp: responseForError(req.ID, err), failed: true}
	}
	return routePreflightResult{params: params}
}

type gatewayRouteError struct {
	code    int
	message string
}

func (e gatewayRouteError) Error() string {
	return e.message
}

func (e routePolicyExecutor) requireAuth(ctx context.Context, method string) error {
	if !e.requiresServerAuth(method) {
		return nil
	}
	ready, err := e.serverAuthReady(ctx)
	if err != nil {
		return err
	}
	if !ready {
		return serverapi.ErrServerAuthRequired
	}
	return nil
}

func (e routePolicyExecutor) requiresServerAuth(method string) bool {
	trimmed := strings.TrimSpace(method)
	if trimmed == "" {
		return false
	}
	route, ok := rpccontract.RouteByMethod(trimmed)
	if !ok {
		return true
	}
	switch route.Auth {
	case rpccontract.AuthNone, rpccontract.AuthPreServerAuth:
		return false
	default:
		return true
	}
}

func (e routePolicyExecutor) serverAuthReady(ctx context.Context) (bool, error) {
	g := e.gateway
	if g == nil || g.deps == nil {
		return false, nil
	}
	if !g.deps.ServerAuthRequired() {
		return true, nil
	}
	if g.deps.AuthManager() == nil {
		return false, nil
	}
	state, err := g.deps.AuthManager().Load(ctx)
	if err != nil {
		return false, err
	}
	return auth.EvaluateStartupGate(state).Ready, nil
}

func decodeRouteParams(route rpccontract.Route, raw json.RawMessage) (any, error) {
	if route.RequestType == nil {
		return nil, nil
	}
	ptr := reflect.New(route.RequestType)
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, ptr.Interface()); err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
	}
	params := ptr.Elem().Interface()
	if validator, ok := params.(interface{ Validate() error }); ok {
		if err := validator.Validate(); err != nil {
			return nil, err
		}
	}
	return params, nil
}

func (e routePolicyExecutor) authorizeScope(ctx context.Context, state *connectionState, route rpccontract.Route, params any) error {
	scopeParams, err := routeScopeParamsFor(route, params)
	if err != nil {
		return err
	}
	switch route.Scope {
	case rpccontract.ScopeNone, rpccontract.ScopeProjectView, rpccontract.ScopeAttachProject, rpccontract.ScopeNotification:
		return nil
	case rpccontract.ScopeProjectWorkspace:
		_, err := e.gateway.activeProjectID(ctx, state)
		return err
	case rpccontract.ScopeAttachSession, rpccontract.ScopeSessionActiveProject:
		return e.gateway.requireSessionInActiveProject(ctx, state, scopeParams.sessionID)
	case rpccontract.ScopeSessionActiveProjectIfSet:
		if strings.TrimSpace(scopeParams.sessionID) == "" {
			return nil
		}
		return e.gateway.requireSessionInActiveProject(ctx, state, scopeParams.sessionID)
	case rpccontract.ScopeSessionAttachedProject:
		return e.gateway.requireSessionInAttachedProject(ctx, state, scopeParams.sessionID)
	case rpccontract.ScopeAttachedSession:
		if state.attachedSession != scopeParams.sessionID {
			return gatewayRouteError{code: protocol.ErrCodeInvalidRequest, message: "session attach is required before subscribing"}
		}
		return nil
	case rpccontract.ScopeGoalSession:
		return e.gateway.requireGoalSessionAccess(ctx, state, scopeParams.sessionID)
	case rpccontract.ScopeProcessActiveProject:
		_, err := e.gateway.processInActiveProject(ctx, state, scopeParams.processID)
		return err
	case rpccontract.ScopeProcessListActiveProject:
		if strings.TrimSpace(scopeParams.ownerSessionID) != "" {
			return e.gateway.requireSessionInActiveProject(ctx, state, scopeParams.ownerSessionID)
		}
		return nil
	default:
		return fmt.Errorf("unsupported route scope %q for method %q", route.Scope, route.Method)
	}
}

type routeScopeParams struct {
	sessionID      string
	processID      string
	ownerSessionID string
}

func routeScopeParamsFor(route rpccontract.Route, params any) (routeScopeParams, error) {
	switch route.Scope {
	case rpccontract.ScopeAttachSession,
		rpccontract.ScopeSessionActiveProject,
		rpccontract.ScopeSessionActiveProjectIfSet,
		rpccontract.ScopeSessionAttachedProject,
		rpccontract.ScopeAttachedSession,
		rpccontract.ScopeGoalSession:
		sessionID, ok := routeSessionID(params)
		if !ok {
			return routeScopeParams{}, fmt.Errorf("route %q scope %q requires typed session id accessor", route.Method, route.Scope)
		}
		return routeScopeParams{sessionID: sessionID}, nil
	case rpccontract.ScopeProcessActiveProject:
		processID, ok := routeProcessID(params)
		if !ok {
			return routeScopeParams{}, fmt.Errorf("route %q scope %q requires typed process id accessor", route.Method, route.Scope)
		}
		return routeScopeParams{processID: processID}, nil
	case rpccontract.ScopeProcessListActiveProject:
		ownerSessionID, ok := routeOwnerSessionID(params)
		if !ok {
			return routeScopeParams{}, fmt.Errorf("route %q scope %q requires typed owner session id accessor", route.Method, route.Scope)
		}
		return routeScopeParams{ownerSessionID: ownerSessionID}, nil
	default:
		return routeScopeParams{}, nil
	}
}

func routeSessionID(params any) (string, bool) {
	switch p := params.(type) {
	case protocol.AttachSessionRequest:
		return p.SessionID, true
	case serverapi.SessionMainViewRequest:
		return p.SessionID, true
	case serverapi.SessionTranscriptPageRequest:
		return p.SessionID, true
	case serverapi.SessionCommittedTranscriptSuffixRequest:
		return p.SessionID, true
	case serverapi.SessionInitialInputRequest:
		return p.SessionID, true
	case serverapi.SessionPersistInputDraftRequest:
		return p.SessionID, true
	case serverapi.SessionRetargetWorkspaceRequest:
		return p.SessionID, true
	case serverapi.SessionResolveTransitionRequest:
		return p.SessionID, true
	case serverapi.SessionRuntimeActivateRequest:
		return p.SessionID, true
	case serverapi.SessionRuntimeReleaseRequest:
		return p.SessionID, true
	case serverapi.WorktreeListRequest:
		return p.SessionID, true
	case serverapi.WorktreeCreateTargetResolveRequest:
		return p.SessionID, true
	case serverapi.WorktreeCreateRequest:
		return p.SessionID, true
	case serverapi.WorktreeSwitchRequest:
		return p.SessionID, true
	case serverapi.WorktreeDeleteRequest:
		return p.SessionID, true
	case serverapi.RuntimeSetSessionNameRequest:
		return p.SessionID, true
	case serverapi.RuntimeSetThinkingLevelRequest:
		return p.SessionID, true
	case serverapi.RuntimeSetFastModeEnabledRequest:
		return p.SessionID, true
	case serverapi.RuntimeSetReviewerEnabledRequest:
		return p.SessionID, true
	case serverapi.RuntimeSetAutoCompactionEnabledRequest:
		return p.SessionID, true
	case serverapi.RuntimeSetQuestionsEnabledRequest:
		return p.SessionID, true
	case serverapi.RuntimeAppendCommittedEntryRequest:
		return p.SessionID, true
	case serverapi.RuntimeShouldCompactBeforeUserMessageRequest:
		return p.SessionID, true
	case serverapi.RuntimeSubmitUserTurnRequest:
		return p.SessionID, true
	case serverapi.RuntimeSubmitUserShellCommandRequest:
		return p.SessionID, true
	case serverapi.RuntimeCompactContextRequest:
		return p.SessionID, true
	case serverapi.RuntimeCompactContextForPreSubmitRequest:
		return p.SessionID, true
	case serverapi.RuntimeHasQueuedUserWorkRequest:
		return p.SessionID, true
	case serverapi.RuntimeSubmitQueuedUserMessagesRequest:
		return p.SessionID, true
	case serverapi.RuntimeInterruptRequest:
		return p.SessionID, true
	case serverapi.RuntimeQueueUserMessageRequest:
		return p.SessionID, true
	case serverapi.RuntimeDiscardQueuedUserMessageRequest:
		return p.SessionID, true
	case serverapi.RuntimeRecordPromptHistoryRequest:
		return p.SessionID, true
	case serverapi.RuntimeGoalShowRequest:
		return p.SessionID, true
	case serverapi.RuntimeGoalSetRequest:
		return p.SessionID, true
	case serverapi.RuntimeGoalStatusRequest:
		return p.SessionID, true
	case serverapi.RuntimeGoalClearRequest:
		return p.SessionID, true
	case serverapi.AskListPendingBySessionRequest:
		return p.SessionID, true
	case serverapi.AskAnswerRequest:
		return p.SessionID, true
	case serverapi.ApprovalListPendingBySessionRequest:
		return p.SessionID, true
	case serverapi.ApprovalAnswerRequest:
		return p.SessionID, true
	case serverapi.SessionActivitySubscribeRequest:
		return p.SessionID, true
	case serverapi.PromptActivitySubscribeRequest:
		return p.SessionID, true
	default:
		return "", false
	}
}

func routeProcessID(params any) (string, bool) {
	switch p := params.(type) {
	case serverapi.ProcessGetRequest:
		return p.ProcessID, true
	case serverapi.ProcessKillRequest:
		return p.ProcessID, true
	case serverapi.ProcessInlineOutputRequest:
		return p.ProcessID, true
	case serverapi.ProcessOutputSubscribeRequest:
		return p.ProcessID, true
	default:
		return "", false
	}
}

func routeOwnerSessionID(params any) (string, bool) {
	switch p := params.(type) {
	case serverapi.ProcessListRequest:
		return p.OwnerSessionID, true
	default:
		return "", false
	}
}

func (g *Gateway) preflightRouteRequest(ctx context.Context, state *connectionState, route rpccontract.Route, req protocol.Request) (any, protocol.Response, bool) {
	result := newRoutePolicyExecutor(g).preflight(ctx, state, route, req)
	return result.params, result.resp, result.failed
}

func (g *Gateway) activeProjectID(ctx context.Context, state *connectionState) (string, error) {
	if trimmed := strings.TrimSpace(state.attachedProject); trimmed != "" {
		return trimmed, nil
	}
	if trimmed := strings.TrimSpace(g.deps.ProjectID()); trimmed != "" {
		return trimmed, nil
	}
	return "", activeProjectRequiredError{}
}

func (g *Gateway) requireSessionInActiveProject(ctx context.Context, state *connectionState, sessionID string) error {
	projectID, err := g.activeProjectID(ctx, state)
	if err != nil {
		return err
	}
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return fmt.Errorf("session id is required")
	}
	metadataStore := g.deps.MetadataStore()
	if metadataStore == nil {
		return errors.New("metadata store is required")
	}
	belongs, err := metadataStore.SessionBelongsToProject(ctx, trimmedSessionID, projectID)
	if err != nil {
		return err
	}
	if !belongs {
		return sessionOutsideActiveProjectError{sessionID: trimmedSessionID}
	}
	return nil
}

func (g *Gateway) requireGoalSessionAccess(ctx context.Context, state *connectionState, sessionID string) error {
	if strings.TrimSpace(state.attachedProject) == "" && strings.TrimSpace(g.deps.ProjectID()) == "" {
		return nil
	}
	return g.requireSessionInActiveProject(ctx, state, sessionID)
}

func (g *Gateway) requireSessionInAttachedProject(ctx context.Context, state *connectionState, sessionID string) error {
	projectID := strings.TrimSpace(state.attachedProject)
	if projectID == "" {
		return nil
	}
	return g.deps.SessionBelongsToProject(ctx, sessionID, projectID)
}

func (g *Gateway) processInActiveProject(ctx context.Context, state *connectionState, processID string) (serverapi.ProcessGetResponse, error) {
	resp, err := g.deps.ProcessViewClient().GetProcess(ctx, serverapi.ProcessGetRequest{ProcessID: processID})
	if err != nil {
		return serverapi.ProcessGetResponse{}, err
	}
	if resp.Process == nil {
		return serverapi.ProcessGetResponse{}, fmt.Errorf("process %q not available", strings.TrimSpace(processID))
	}
	ownerSessionID := strings.TrimSpace(resp.Process.OwnerSessionID)
	if ownerSessionID == "" {
		return serverapi.ProcessGetResponse{}, fmt.Errorf("process %q not available", strings.TrimSpace(processID))
	}
	if err := g.requireSessionInActiveProject(ctx, state, ownerSessionID); err != nil {
		return serverapi.ProcessGetResponse{}, err
	}
	return resp, nil
}

func (g *Gateway) filterProcessesForActiveProject(ctx context.Context, state *connectionState, processes []clientui.BackgroundProcess) ([]clientui.BackgroundProcess, error) {
	filtered := make([]clientui.BackgroundProcess, 0, len(processes))
	for _, process := range processes {
		ownerSessionID := strings.TrimSpace(process.OwnerSessionID)
		if ownerSessionID == "" {
			continue
		}
		err := g.requireSessionInActiveProject(ctx, state, ownerSessionID)
		if err == nil {
			filtered = append(filtered, process)
			continue
		}
		if !errors.Is(err, errSessionOutsideActiveProject) &&
			!errors.Is(err, errActiveProjectRequired) &&
			!errors.Is(err, session.ErrSessionNotFound) &&
			!errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return filtered, nil
}
