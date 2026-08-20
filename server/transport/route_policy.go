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
	sharedpb "core/shared/protoapi/gen/kent/api/shared"
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
	params, err := e.decodeRouteParams(route, req.Params)
	if err != nil {
		var structured protocol.StructuredRPCError
		if errors.As(err, &structured) {
			return routePreflightResult{resp: responseForError(req.ID, err), failed: true}
		}
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

func (e routePolicyExecutor) decodeRouteParams(route rpccontract.Route, raw json.RawMessage) (any, error) {
	if route.Method == protocol.MethodSessionGetExecutionEnvironment && e.gateway != nil {
		params, err := e.gateway.sessionExecutionRequestContract.Decode(raw)
		if err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
		return params, nil
	}
	return decodeRouteParams(route, raw)
}

type gatewayRouteError struct {
	code    int
	message string
}

func (e gatewayRouteError) Error() string {
	return e.message
}

func (e routePolicyExecutor) requireAuth(ctx context.Context, state *connectionState, method string) error {
	stage, known := e.authenticationStage(method)
	if !known {
		stage = sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER
	}
	return e.requireAuthenticationStage(ctx, state, stage)
}

func (e routePolicyExecutor) requireAuthenticationStage(
	ctx context.Context,
	state *connectionState,
	stage sharedpb.AuthenticationStage,
) error {
	if stage != sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER {
		return nil
	}
	ready, err := e.serverAuthReady(ctx, state)
	if err != nil {
		return err
	}
	if !ready {
		return serverapi.ErrServerAuthRequired
	}
	return nil
}

func (e routePolicyExecutor) requiresServerAuth(method string) bool {
	stage, known := e.authenticationStage(method)
	return !known || stage == sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_SERVER
}

func (e routePolicyExecutor) authenticationStage(method string) (sharedpb.AuthenticationStage, bool) {
	trimmed := strings.TrimSpace(method)
	if trimmed == "" {
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_NONE, true
	}
	var registration gatewayRegistration
	if e.gateway != nil {
		registration = e.gateway.registration
	}
	if len(registration.operations) == 0 {
		var err error
		registration, err = productionGatewayRegistration()
		if err != nil {
			panic(err)
		}
	}
	if operation, exists := registration.operations[trimmed]; exists {
		if _, migrated := registration.BinaryBinding(trimmed); migrated {
			return operation.Options.AuthenticationStage, true
		}
	}
	operation, _, ok := registration.LegacyOperation(trimmed)
	if !ok {
		return sharedpb.AuthenticationStage_AUTHENTICATION_STAGE_UNSPECIFIED, false
	}
	return operation.Options.AuthenticationStage, true
}

func (e routePolicyExecutor) serverAuthReady(ctx context.Context, connection *connectionState) (bool, error) {
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
	if auth.EvaluateStartupGate(state).Ready {
		return true, nil
	}
	if connection != nil && connection.noAuthAccepted {
		stored, err := g.deps.AuthManager().StoredState(ctx)
		if err != nil {
			return false, err
		}
		return stored.IsNoAuthSelected(), nil
	}
	return false, nil
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
	if validator, ok := params.(interface{ ValidateRPC() error }); ok {
		if err := validator.ValidateRPC(); err != nil {
			return nil, err
		}
	} else if validator, ok := params.(interface{ Validate() error }); ok {
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
	return e.authorizeScopeFacts(ctx, state, route.Scope, route.Method, scopeParams)
}

func (e routePolicyExecutor) authorizeScopeFacts(
	ctx context.Context,
	state *connectionState,
	scope rpccontract.ScopePolicy,
	method string,
	scopeParams routeScopeParams,
) error {
	switch scope {
	case rpccontract.ScopeNone, rpccontract.ScopeProjectView, rpccontract.ScopeAttachProject, rpccontract.ScopeNotification:
		return nil
	case rpccontract.ScopeProjectWorkspace:
		_, err := e.gateway.activeProjectID(ctx, state)
		return err
	case rpccontract.ScopeProjectWorkspaceBinding:
		activeProjectID, err := e.gateway.activeProjectID(ctx, state)
		if err != nil {
			return err
		}
		if strings.TrimSpace(scopeParams.projectID) != strings.TrimSpace(activeProjectID) {
			return serverapi.ErrWorkspaceNotRegistered
		}
		if strings.TrimSpace(state.attachedWorkspaceID) != strings.TrimSpace(scopeParams.workspaceID) {
			return serverapi.ErrWorkspaceNotRegistered
		}
		binding, err := e.gateway.deps.MetadataStore().LookupWorkspaceBindingByID(ctx, scopeParams.workspaceID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(binding.ProjectID) != strings.TrimSpace(activeProjectID) {
			return serverapi.ErrWorkspaceNotRegistered
		}
		return nil
	case rpccontract.ScopeAttachSession:
		_, err := e.gateway.resolveSessionAttachment(ctx, state, scopeParams.sessionID)
		return err
	case rpccontract.ScopeSessionActiveProject:
		return e.gateway.requireSessionInActiveProject(ctx, state, scopeParams.sessionID)
	case rpccontract.ScopeSessionActiveProjectIfSet:
		if strings.TrimSpace(scopeParams.sessionID) == "" {
			return nil
		}
		return e.gateway.requireSessionInActiveProject(ctx, state, scopeParams.sessionID)
	case rpccontract.ScopeSessionAttachedProject:
		return e.gateway.requireSessionInAttachedProject(ctx, state, scopeParams.sessionID)
	case rpccontract.ScopeAttachedSession:
		if state.attachedSession == nil || state.attachedSession.String() != scopeParams.sessionID {
			return gatewayRouteError{code: protocol.ErrCodeInvalidRequest, message: "session attach is required before subscribing"}
		}
		return nil
	case rpccontract.ScopeGoalSession:
		return e.gateway.requireGoalSessionAccess(ctx, state, scopeParams.sessionID)
	case rpccontract.ScopeRuntimeLiveSessionRequired:
		return e.gateway.requireRuntimeLiveSession(ctx, scopeParams.sessionID)
	case rpccontract.ScopeRuntimeLiveSessionOptional:
		return nil
	case rpccontract.ScopeProcessActiveProject:
		_, err := e.gateway.processInActiveProject(ctx, state, scopeParams.processID)
		return err
	case rpccontract.ScopeProcessListActiveProject:
		if strings.TrimSpace(scopeParams.ownerSessionID) != "" {
			return e.gateway.requireSessionInActiveProject(ctx, state, scopeParams.ownerSessionID)
		}
		return nil
	default:
		return fmt.Errorf("unsupported route scope %q for method %q", scope, method)
	}
}

type routeScopeParams struct {
	sessionID      string
	processID      string
	ownerSessionID string
	projectID      string
	workspaceID    string
}

func routeScopeParamsFor(route rpccontract.Route, params any) (routeScopeParams, error) {
	switch route.Scope {
	case rpccontract.ScopeAttachSession,
		rpccontract.ScopeSessionActiveProject,
		rpccontract.ScopeSessionActiveProjectIfSet,
		rpccontract.ScopeSessionAttachedProject,
		rpccontract.ScopeAttachedSession,
		rpccontract.ScopeGoalSession,
		rpccontract.ScopeRuntimeLiveSessionRequired,
		rpccontract.ScopeRuntimeLiveSessionOptional:
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
	case rpccontract.ScopeProjectWorkspaceBinding:
		projectID, workspaceID, ok := routeProjectWorkspaceBinding(params)
		if !ok {
			return routeScopeParams{}, fmt.Errorf("route %q scope %q requires typed project/workspace accessor", route.Method, route.Scope)
		}
		return routeScopeParams{projectID: projectID, workspaceID: workspaceID}, nil
	default:
		return routeScopeParams{}, nil
	}
}

func routeProjectWorkspaceBinding(params any) (string, string, bool) {
	switch request := params.(type) {
	case serverapi.WorktreeWorkspaceListRequest:
		return request.ProjectID, request.WorkspaceID, true
	default:
		return "", "", false
	}
}

func routeSessionID(params any) (string, bool) {
	switch p := params.(type) {
	case serverapi.ChatContextRequest:
		sessionID, selected := p.Target.SessionID()
		if !selected {
			return "", true
		}
		return sessionID.String(), true
	case serverapi.SessionMainViewRequest:
		return p.SessionID, true
	case serverapi.SessionTranscriptPageRequest:
		return p.SessionID, true
	case serverapi.SessionLatestCommittedAssistantFinalAnswerRequest:
		return p.SessionID, true
	case serverapi.SessionExecutionEnvironmentRequest:
		return p.SessionID.String(), true
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
		return p.Attachment.SessionID, true
	case serverapi.WorktreeListRequest:
		return p.SessionID, true
	case serverapi.WorktreeStatusRequest:
		return p.SessionID, true
	case serverapi.WorktreeSelectorPreviewRequest:
		return p.SessionID, true
	case serverapi.WorktreeDeletePreviewRequest:
		return p.SessionID, true
	case serverapi.WorktreeCreateTargetResolveRequest:
		return p.SessionID, true
	case serverapi.WorktreeCreateRequest:
		return p.SessionID, true
	case serverapi.WorktreeEnterRequest:
		return p.SessionID, true
	case serverapi.WorktreeLeaveRequest:
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
	case serverapi.RuntimeInterruptRequest:
		return p.SessionID, true
	case serverapi.RuntimeLiveSteerRequest:
		return p.SessionID, true
	case serverapi.RuntimeLiveStopRequest:
		return p.SessionID, true
	case serverapi.RuntimeLiveWaitRequest:
		return p.SessionID, true
	case serverapi.RuntimeLiveWatchRequest:
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
	case serverapi.PromptAnswerBatchRequest:
		return p.SessionID.String(), true
	case serverapi.PromptFollowUpWatchRequest:
		return p.SessionID.String(), true
	case serverapi.ApprovalListPendingBySessionRequest:
		return p.SessionID, true
	case serverapi.TranscriptSubscribeRequest:
		return p.SessionID, true
	case serverapi.AttentionSessionNotificationSubscribeRequest:
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

func (g *Gateway) requireRuntimeLiveSession(ctx context.Context, sessionID string) error {
	trimmedSessionID := strings.TrimSpace(sessionID)
	if trimmedSessionID == "" {
		return serverapi.ErrRuntimeUnavailable
	}
	metadataStore := g.deps.MetadataStore()
	if metadataStore == nil {
		return serverapi.ErrRuntimeUnavailable
	}
	if _, err := metadataStore.ResolvePersistedSession(ctx, trimmedSessionID); err != nil {
		return fmt.Errorf("%w: %w", serverapi.ErrRuntimeUnavailable, err)
	}
	return nil
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
