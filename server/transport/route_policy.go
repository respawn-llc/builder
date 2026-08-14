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
	"core/shared/runtimeids"
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

func authorizeGoalSession[Req any](
	sessionID func(Req) string,
) func(context.Context, *Gateway, *connectionState, rpccontract.Validated[Req]) (rpccontract.AuthorizedSessionInActiveProject, error) {
	required := authorizeSessionActiveProject(sessionID)
	return func(ctx context.Context, g *Gateway, state *connectionState, validated rpccontract.Validated[Req]) (rpccontract.AuthorizedSessionInActiveProject, error) {
		if strings.TrimSpace(state.attachedProject) != "" || strings.TrimSpace(g.deps.ProjectID()) != "" {
			return required(ctx, g, state, validated)
		}
		parsed, err := runtimeids.ParseSessionID(strings.TrimSpace(sessionID(validated.Value())))
		if err != nil {
			return rpccontract.AuthorizedSessionInActiveProject{}, err
		}
		return rpccontract.AuthorizedSessionInActiveProject{SessionID: parsed}, nil
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
