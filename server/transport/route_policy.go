package transport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

type gatewayRouteError struct {
	code    int
	message string
}

func (e gatewayRouteError) Error() string {
	return e.message
}

func (e routePolicyExecutor) requireAuth(ctx context.Context, state *connectionState, method string) error {
	if !e.requiresServerAuth(method) {
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
	case rpccontract.ScopeProjectWorkspaceBinding:
		return errors.New("Project Workspace binding scope requires typed authorization")
	case rpccontract.ScopeAttachSession:
		return errors.New("Session attachment scope requires typed authorization")
	case rpccontract.ScopeSessionActiveProject:
		return errors.New("Session active-Project scope requires typed authorization")
	case rpccontract.ScopeSessionActiveProjectIfSet:
		return errors.New("optional Session active-Project scope requires typed authorization")
	case rpccontract.ScopeSessionAttachedProject:
		return errors.New("Session attached-Project scope requires a typed constraint")
	case rpccontract.ScopeAttachedSession:
		if state.attachedSession == nil || state.attachedSession.String() != scopeParams.sessionID {
			return gatewayRouteError{code: protocol.ErrCodeInvalidRequest, message: "session attach is required before subscribing"}
		}
		return nil
	case rpccontract.ScopeGoalSession:
		return errors.New("Goal Session scope requires its focused authorization path")
	case rpccontract.ScopeRuntimeLiveSessionOptional:
		return errors.New("Runtime Live scope requires its focused trusted-owner path")
	case rpccontract.ScopeProcessActiveProject:
		return errors.New("Process active-Project scope requires typed authorization")
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
	case protocol.AttachSessionRequest:
		return p.SessionID, true
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

func (g *Gateway) authorizeValidatedRouteRequest(ctx context.Context, state *connectionState, method string, params any) error {
	route, ok := rpccontract.RouteByMethod(method)
	if !ok {
		return fmt.Errorf("route %q is not registered", method)
	}
	return newRoutePolicyExecutor(g).authorizeScope(ctx, state, route, params)
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

func authorizeSessionActiveProject[Req any](
	sessionID func(Req) string,
) func(context.Context, *Gateway, *connectionState, rpccontract.Validated[Req]) (rpccontract.AuthorizedSessionInActiveProject, error) {
	return func(ctx context.Context, g *Gateway, state *connectionState, validated rpccontract.Validated[Req]) (rpccontract.AuthorizedSessionInActiveProject, error) {
		activeProjectID, err := g.activeProjectID(ctx, state)
		if err != nil {
			return rpccontract.AuthorizedSessionInActiveProject{}, err
		}
		metadataStore := g.deps.MetadataStore()
		if metadataStore == nil {
			return rpccontract.AuthorizedSessionInActiveProject{}, errors.New("metadata store is required")
		}
		resolved, err := metadataStore.ResolveActiveProjectSession(ctx, sessionID(validated.Value()))
		if err != nil {
			return rpccontract.AuthorizedSessionInActiveProject{}, err
		}
		if resolved.OwningProjectID != strings.TrimSpace(activeProjectID) {
			return rpccontract.AuthorizedSessionInActiveProject{}, sessionOutsideActiveProjectError{
				sessionID: resolved.SessionID.String(),
			}
		}
		return rpccontract.AuthorizedSessionInActiveProject{
			SessionID:       resolved.SessionID,
			ActiveProjectID: strings.TrimSpace(activeProjectID),
			OwningProjectID: resolved.OwningProjectID,
			ExecutionTarget: resolved.ExecutionTarget,
		}, nil
	}
}

func authorizeOptionalSessionActiveProject[Req any](
	sessionID func(Req) string,
) func(context.Context, *Gateway, *connectionState, rpccontract.Validated[Req]) (rpccontract.OptionalAuthorizedSessionInActiveProject, error) {
	required := authorizeSessionActiveProject(sessionID)
	return func(ctx context.Context, g *Gateway, state *connectionState, validated rpccontract.Validated[Req]) (rpccontract.OptionalAuthorizedSessionInActiveProject, error) {
		if strings.TrimSpace(sessionID(validated.Value())) == "" {
			return rpccontract.AbsentAuthorizedSessionInActiveProject(), nil
		}
		authorization, err := required(ctx, g, state, validated)
		if err != nil {
			return rpccontract.OptionalAuthorizedSessionInActiveProject{}, err
		}
		return rpccontract.PresentAuthorizedSessionInActiveProject(authorization), nil
	}
}

func authorizeProcessActiveProject[Req any](
	processID func(Req) string,
) func(context.Context, *Gateway, *connectionState, rpccontract.Validated[Req]) (rpccontract.AuthorizedProcessInActiveProject, error) {
	return func(ctx context.Context, g *Gateway, state *connectionState, validated rpccontract.Validated[Req]) (rpccontract.AuthorizedProcessInActiveProject, error) {
		resolver, ok := g.deps.ProcessViewClient().(rpccontract.ProcessViewTrustedService)
		if !ok {
			return rpccontract.AuthorizedProcessInActiveProject{}, errors.New("Process View trusted service is required")
		}
		candidate, err := resolver.ResolveProcessAuthorization(ctx, processID(validated.Value()))
		if err != nil {
			return rpccontract.AuthorizedProcessInActiveProject{}, err
		}
		if strings.TrimSpace(candidate.OwnerSessionID) == "" {
			return rpccontract.AuthorizedProcessInActiveProject{}, fmt.Errorf("process %q not available", candidate.ProcessID)
		}
		if err := g.requireSessionInActiveProject(ctx, state, candidate.OwnerSessionID); err != nil {
			return rpccontract.AuthorizedProcessInActiveProject{}, err
		}
		return rpccontract.AuthorizedProcessInActiveProject{
			ProcessID:      candidate.ProcessID,
			OwnerSessionID: candidate.OwnerSessionID,
			Process:        candidate.Process,
		}, nil
	}
}

func (g *Gateway) authorizeProjectWorkspaceBinding(ctx context.Context, state *connectionState, req serverapi.WorktreeWorkspaceListRequest) (rpccontract.AuthorizedProjectWorkspaceBinding, error) {
	activeProjectID, err := g.activeProjectID(ctx, state)
	if err != nil {
		return rpccontract.AuthorizedProjectWorkspaceBinding{}, err
	}
	if strings.TrimSpace(req.ProjectID) != strings.TrimSpace(activeProjectID) ||
		strings.TrimSpace(state.attachedWorkspaceID) != strings.TrimSpace(req.WorkspaceID) {
		return rpccontract.AuthorizedProjectWorkspaceBinding{}, serverapi.ErrWorkspaceNotRegistered
	}
	binding, err := g.deps.MetadataStore().LookupWorkspaceBindingByID(ctx, req.WorkspaceID)
	if err != nil {
		return rpccontract.AuthorizedProjectWorkspaceBinding{}, err
	}
	if strings.TrimSpace(binding.ProjectID) != strings.TrimSpace(activeProjectID) {
		return rpccontract.AuthorizedProjectWorkspaceBinding{}, serverapi.ErrWorkspaceNotRegistered
	}
	return rpccontract.AuthorizedProjectWorkspaceBinding{
		ProjectID:     binding.ProjectID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: binding.CanonicalRoot,
	}, nil
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
