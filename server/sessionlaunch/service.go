package sessionlaunch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/subagentpolicy"
	servicecontract "core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type authStateReader interface {
	CurrentState(context.Context) (auth.State, error)
}

type promptHistoryReader interface {
	ReadPromptHistory(ctx context.Context, sessionID string) ([]string, error)
}

type Service struct {
	planner       launch.Planner
	authStates    authStateReader
	promptHistory promptHistoryReader
	runtime       *sessionruntime.Authority
	plans         *requestmemo.Memo[sessionPlanMemoRequest, PlanResult]
	workspaceID   string
	fastModeState *runtime.FastModeState
	draftOwner    *WorkspaceChatDraftOwner
}

var ErrExistingSessionAuthorityRequired = errors.New("session runtime authority is required for existing-session planning")

type PlanResult struct {
	Plan     launch.SessionPlan
	Warnings []string
}

type sessionPlanMemoRequest struct {
	Mode            serverapi.SessionLaunchMode
	Intent          serverapi.SessionLaunchIntent
	CallerSessionID serverapi.OptionalStringKey
	Overrides       serverapi.RunPromptOverridesKey
}

func NewService(planner launch.Planner) *Service {
	return &Service{planner: planner, plans: requestmemo.New[sessionPlanMemoRequest, PlanResult]()}
}
func (s *Service) WithAuthStateReader(reader authStateReader) *Service {
	if s == nil {
		return nil
	}
	s.authStates = reader
	return s
}

func (s *Service) WithPromptHistoryReader(reader promptHistoryReader) *Service {
	if s == nil {
		return nil
	}
	s.promptHistory = reader
	return s
}

func (s *Service) WithRuntimeAuthority(authority *sessionruntime.Authority) *Service {
	if s == nil {
		return nil
	}
	s.runtime = authority
	return s
}

func (s *Service) WithWorkspaceChatDraft(owner *WorkspaceChatDraftOwner, workspaceID string, state *runtime.FastModeState) *Service {
	if s != nil {
		s.workspaceID = strings.TrimSpace(workspaceID)
		s.fastModeState = state
		s.draftOwner = owner
	}
	return s
}
func (s *Service) workspaceChatDraftResolverInput(ctx context.Context) (WorkspaceChatDraftResolverInput, error) {
	planner := s.planner
	if planner.ReloadConfig != nil {
		snapshot, err := planner.ReloadConfig()
		if err != nil {
			return WorkspaceChatDraftResolverInput{}, err
		}
		planner.Config = snapshot
	}
	authState := auth.EmptyState()
	if s.authStates != nil {
		var err error
		authState, err = s.authStates.CurrentState(ctx)
		if err != nil {
			return WorkspaceChatDraftResolverInput{}, err
		}
	}
	return WorkspaceChatDraftResolverInput{Settings: planner.Config.Settings, Source: planner.Config.Source, AuthState: authState, FastModeState: s.fastModeState}, nil
}

func (s *Service) workspaceChatDraftOwner() (*WorkspaceChatDraftOwner, string, error) {
	if s == nil || s.draftOwner == nil {
		return nil, "", errors.New("workspace Chat draft service is required")
	}
	id := strings.TrimSpace(s.workspaceID)
	if id == "" {
		return nil, "", errors.New("workspace id is required")
	}
	return s.draftOwner, id, nil
}

func (s *Service) ResolveWorkspaceChatDraftAggregate(ctx context.Context) (WorkspaceChatDraftResolution, error) {
	owner, workspaceID, err := s.workspaceChatDraftOwner()
	if err != nil {
		return WorkspaceChatDraftResolution{}, err
	}
	return owner.ResolveWorkspaceChatDraft(ctx, workspaceID, s.workspaceChatDraftResolverInput)
}

func (s *Service) TransformWorkspaceChatDraftAggregate(ctx context.Context, transform WorkspaceChatDraftTransform) (WorkspaceChatDraft, error) {
	owner, workspaceID, err := s.workspaceChatDraftOwner()
	if err != nil {
		return WorkspaceChatDraft{}, err
	}
	return owner.TransformWorkspaceChatDraft(ctx, workspaceID, s.workspaceChatDraftResolverInput, transform)
}

func (s *Service) WorkspaceChatDraft(ctx context.Context, req serverapi.WorkspaceChatDraftRequest) (serverapi.WorkspaceChatDraftResponse, error) {
	if err := req.Operation.Validate(); err != nil {
		return serverapi.WorkspaceChatDraftResponse{}, err
	}
	switch req.Operation.Kind {
	case serverapi.WorkspaceChatDraftReadMessage:
		resolved, err := s.ResolveWorkspaceChatDraftAggregate(ctx)
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		return serverapi.WorkspaceChatDraftResponse{Message: resolved.Draft.Message, GoalAvailability: runtimeview.GoalAvailabilityFromSession(resolved.GoalAvailability)}, nil
	case serverapi.WorkspaceChatDraftUpdateMessage:
		message := *req.Operation.Message
		var availability session.GoalAvailability
		resolved, err := s.TransformWorkspaceChatDraftAggregate(ctx, func(current WorkspaceChatDraftResolution) (WorkspaceChatDraft, error) {
			availability = current.GoalAvailability
			next := current.Draft
			next.Message = message
			return next, nil
		})
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		return serverapi.WorkspaceChatDraftResponse{Message: resolved.Message, GoalAvailability: runtimeview.GoalAvailabilityFromSession(availability)}, nil
	case serverapi.WorkspaceChatDraftClear, serverapi.WorkspaceChatDraftConsume:
		resolved, err := s.ResolveWorkspaceChatDraftAggregate(ctx)
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		owner, workspaceID, err := s.workspaceChatDraftOwner()
		if err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		if err := owner.ClearWorkspaceChatDraft(ctx, workspaceID); err != nil {
			return serverapi.WorkspaceChatDraftResponse{}, err
		}
		return serverapi.WorkspaceChatDraftResponse{GoalAvailability: runtimeview.GoalAvailabilityFromSession(resolved.GoalAvailability)}, nil
	default:
		return serverapi.WorkspaceChatDraftResponse{}, fmt.Errorf("workspace Chat draft operation kind %q is invalid", req.Operation.Kind)
	}
}

func (s *Service) PlanSession(ctx context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	result, err := s.PlanLaunchSession(ctx, req)
	if err != nil {
		return serverapi.SessionPlanResponse{}, err
	}
	response := sessionPlanResponseFromResult(result)
	if err := response.Plan.Validate(); err != nil {
		return serverapi.SessionPlanResponse{}, err
	}
	return response, nil
}

func (s *Service) PlanLaunchSession(ctx context.Context, req serverapi.SessionPlanRequest) (PlanResult, error) {
	if err := req.Validate(); err != nil {
		return PlanResult{}, err
	}
	var selectedSessionID *runtimeids.SessionID
	var parentAgentSessionID *runtimeids.SessionID
	switch req.Intent.Kind() {
	case serverapi.SessionLaunchIntentOpenExisting:
		sessionID, _ := req.Intent.SessionID()
		selectedSessionID = &sessionID
	case serverapi.SessionLaunchIntentCreateNew:
		origin, _ := req.Intent.CreateOrigin()
		if origin.Kind() == serverapi.SessionCreateOriginParentAgent {
			sourceID, _ := origin.SessionID()
			parentAgentSessionID = &sourceID
		}
	}
	overrides, err := req.Overrides.CanonicalKey()
	if err != nil {
		return PlanResult{}, err
	}
	memoReq := sessionPlanMemoRequest{
		Mode:            req.Mode,
		Intent:          req.Intent,
		CallerSessionID: serverapi.CanonicalOptionalString(req.CallerSessionID),
		Overrides:       overrides,
	}
	return s.plans.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionPlanMemoRequest, func(ctx context.Context) (PlanResult, error) {
		planner := s.planner
		if planner.ReloadConfig != nil {
			snapshot, snapshotErr := planner.ReloadConfig()
			if snapshotErr != nil {
				return PlanResult{}, snapshotErr
			}
			planner.Config = snapshot
			planner.ReloadConfig = nil
		}
		roleOverride, err := req.Overrides.AgentRoleOverride()
		if err != nil {
			return PlanResult{}, err
		}
		var caller *subagentpolicy.Caller
		if req.Mode == serverapi.SessionLaunchModeHeadless {
			if req.CallerSessionID != nil {
				resolved, callerErr := launch.ResolveSessionCaller(planner.Config.PersistenceRoot, *req.CallerSessionID)
				if callerErr != nil {
					return PlanResult{}, &serverapi.SubagentLaunchDeniedError{Kind: serverapi.SubagentLaunchDenialCallerMissing}
				}
				caller = &resolved
				if parentAgentSessionID != nil {
					callerSessionID, parseErr := runtimeids.ParseSessionID(*req.CallerSessionID)
					if parseErr != nil || *parentAgentSessionID != callerSessionID {
						return PlanResult{}, &serverapi.SubagentLaunchDeniedError{Kind: serverapi.SubagentLaunchDenialInvalidTarget}
					}
				}
			}
			if parentAgentSessionID != nil && req.CallerSessionID == nil {
				if _, parentErr := launch.ResolveSessionCaller(planner.Config.PersistenceRoot, parentAgentSessionID.String()); parentErr != nil {
					return PlanResult{}, &serverapi.SubagentLaunchDeniedError{Kind: serverapi.SubagentLaunchDenialParentMissing}
				}
			}
		}
		target := subagentpolicy.TargetFromOverride(roleOverride)
		if err := subagentpolicy.Authorize(planner.Config.Settings, caller, target); err != nil {
			return PlanResult{}, err
		}
		authState := auth.EmptyState()
		if req.Overrides.NeedsAuthState() && s.authStates != nil {
			var authErr error
			authState, authErr = s.authStates.CurrentState(ctx)
			if authErr != nil {
				return PlanResult{}, authErr
			}
		}
		if selectedSessionID != nil {
			return s.planExistingSession(ctx, planner, req, *selectedSessionID, roleOverride, caller, authState)
		}
		preparation := launch.RunPromptPreparationContext{}
		preparedOverrides, err := launch.PrepareRunPromptOverridesWithContext(planner.Config, req.Overrides, authState, preparation)
		if err != nil {
			return PlanResult{}, err
		}
		preparedPromptFacingTarget := preparePromptFacingTarget(req.Mode, roleOverride, &preparedOverrides)
		return s.finalizeLaunchPlan(
			ctx,
			func() (launch.SessionPlan, []string, error) {
				return planner.PlanNewSessionWithPreparedOverrides(ctx, launch.SessionRequest{
					Mode:                                launch.Mode(req.Mode),
					Intent:                              req.Intent,
					SkipContinuationAgentRoleValidation: roleOverride.Default,
					PreparedPromptFacingTarget:          preparedPromptFacingTarget,
				}, req.Overrides, preparedOverrides)
			},
		)
	})
}

func (s *Service) planExistingSession(
	ctx context.Context,
	planner launch.Planner,
	req serverapi.SessionPlanRequest,
	sessionID runtimeids.SessionID,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	caller *subagentpolicy.Caller,
	authState auth.State,
) (result PlanResult, resultErr error) {
	if s.runtime == nil {
		return PlanResult{}, ErrExistingSessionAuthorityRequired
	}
	descriptor, err := session.NewScopedOpenSessionDescriptor(sessionID, planner.ContainerDir)
	if err != nil {
		return PlanResult{}, err
	}
	callback := func(ctx context.Context, store *session.Store) error {
		result, resultErr = s.planExistingSessionWithStore(ctx, planner, req, roleOverride, caller, authState, store)
		return resultErr
	}
	resultErr = s.runtime.WithSessionStore(ctx, descriptor, callback)
	return result, resultErr
}

func (s *Service) planExistingSessionWithStore(
	ctx context.Context,
	planner launch.Planner,
	req serverapi.SessionPlanRequest,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	caller *subagentpolicy.Caller,
	authState auth.State,
	store *session.Store,
) (PlanResult, error) {
	if req.Mode == serverapi.SessionLaunchModeHeadless && !roleOverride.Present {
		persistedRole, err := planner.SelectedSessionContinuationAgentRoleWithStore(store)
		if err != nil {
			return PlanResult{}, err
		}
		if persistedRole != nil && caller != nil {
			lookup := config.LookupSubagentRole(planner.Config.Settings, *persistedRole)
			if lookup.Status == config.SubagentRoleLookupPresent {
				persistedOverride, err := (serverapi.RunPromptOverrides{AgentRole: persistedRole}).AgentRoleOverride()
				if err != nil {
					return PlanResult{}, err
				}
				if err := subagentpolicy.Authorize(planner.Config.Settings, caller, subagentpolicy.TargetFromOverride(persistedOverride)); err != nil {
					return PlanResult{}, err
				}
			}
		}
	}
	locked, err := planner.SelectedSessionLockedContractWithStore(store)
	if err != nil {
		return PlanResult{}, err
	}
	preparation := launch.RunPromptPreparationContext{ModelLock: locked, ToolLock: locked}
	if !roleOverride.Present {
		target, targetErr := planner.SelectedSessionPromptFacingTargetWithStore(store)
		if targetErr != nil {
			return PlanResult{}, targetErr
		}
		preparation.OmittedTarget = &target
	}
	preparedOverrides, err := launch.PrepareRunPromptOverridesWithContext(planner.Config, req.Overrides, authState, preparation)
	if err != nil {
		return PlanResult{}, err
	}
	preparedPromptFacingTarget := preparePromptFacingTarget(req.Mode, roleOverride, &preparedOverrides)
	return s.finalizeLaunchPlan(
		ctx,
		func() (launch.SessionPlan, []string, error) {
			plan, err := planner.PlanSessionWithStore(ctx, launch.SessionRequest{
				Mode:                                launch.Mode(req.Mode),
				Intent:                              req.Intent,
				SkipContinuationAgentRoleValidation: roleOverride.Default,
				PreparedPromptFacingTarget:          preparedPromptFacingTarget,
			}, store)
			if err != nil {
				return launch.SessionPlan{}, nil, err
			}
			return planner.ApplyPreparedRunPromptOverridesWithStore(plan, store, req.Overrides, preparedOverrides, launch.RunPromptOverrideOptions{})
		},
	)
}

func preparePromptFacingTarget(
	mode serverapi.SessionLaunchMode,
	roleOverride serverapi.RunPromptAgentRoleOverride,
	preparedOverrides *launch.PreparedRunPromptOverrides,
) *launch.PreparedBaseTarget {
	if mode != serverapi.SessionLaunchModeHeadless {
		if !roleOverride.Present {
			preparedOverrides.BaseTarget = nil
		}
		return nil
	}
	if preparedOverrides.BaseTarget != nil {
		target := *preparedOverrides.BaseTarget
		return &target
	}
	if preparedOverrides.NamedTarget == nil {
		return nil
	}
	return &launch.PreparedBaseTarget{
		Settings:     preparedOverrides.NamedTarget.Settings,
		Source:       preparedOverrides.NamedTarget.Source,
		EnabledTools: preparedOverrides.NamedTarget.EnabledTools,
	}
}

func (s *Service) finalizeLaunchPlan(
	ctx context.Context,
	resolvePlan func() (launch.SessionPlan, []string, error),
) (PlanResult, error) {
	plan, warnings, err := resolvePlan()
	if err != nil {
		return PlanResult{}, err
	}
	if s.promptHistory != nil {
		history, err := s.promptHistory.ReadPromptHistory(ctx, plan.Descriptor.SessionID().String())
		if err != nil {
			return PlanResult{}, err
		}
		plan.PromptHistory = history
	}
	return PlanResult{Plan: plan, Warnings: warnings}, nil
}

func sessionPlanResponseFromResult(result PlanResult) serverapi.SessionPlanResponse {
	enabledToolIDs := make([]string, 0, len(result.Plan.EnabledTools))
	for _, id := range result.Plan.EnabledTools {
		enabledToolIDs = append(enabledToolIDs, string(id))
	}
	return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
		SessionID:           result.Plan.Descriptor.SessionID().String(),
		ActiveSettings:      result.Plan.ActiveSettings,
		EnabledToolIDs:      enabledToolIDs,
		ConfiguredModelName: result.Plan.ConfiguredModelName,
		SessionName:         textutil.Pointer(result.Plan.SessionName),
		PromptHistory:       append([]string(nil), result.Plan.PromptHistory...),
		ModelContractLocked: result.Plan.ModelContractLocked,
		Source:              result.Plan.Source,
	}, Warnings: result.Warnings}
}

func sameSessionPlanMemoRequest(a sessionPlanMemoRequest, b sessionPlanMemoRequest) bool {
	return a.Mode == b.Mode &&
		a.Intent.Equal(b.Intent) &&
		a.CallerSessionID == b.CallerSessionID &&
		a.Overrides == b.Overrides
}

var _ servicecontract.SessionLaunchService = (*Service)(nil)
