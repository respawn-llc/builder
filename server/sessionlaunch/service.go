package sessionlaunch

import (
	"context"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/requestmemo"
	"core/server/session"
	"core/server/subagentpolicy"
	servicecontract "core/shared/apicontract"
	"core/shared/serverapi"
)

type sessionStoreRegistrar interface {
	RegisterStore(store *session.Store)
}

type authStateReader interface {
	CurrentState(context.Context) (auth.State, error)
}

type promptHistoryReader interface {
	ReadPromptHistory(ctx context.Context, sessionID string) ([]string, error)
}

type Service struct {
	planner       launch.Planner
	stores        sessionStoreRegistrar
	authStates    authStateReader
	promptHistory promptHistoryReader
	plans         *requestmemo.Memo[sessionPlanMemoRequest, PlanResult]
}

type PlanResult struct {
	Plan     launch.SessionPlan
	Warnings []string
}

type sessionPlanMemoRequest struct {
	Mode              serverapi.SessionLaunchMode
	SelectedSessionID string
	ForceNewSession   bool
	CallerSessionID   serverapi.OptionalStringKey
	ParentSessionID   serverapi.OptionalStringKey
	Overrides         serverapi.RunPromptOverridesKey
}

func NewService(planner launch.Planner, stores sessionStoreRegistrar) *Service {
	return &Service{planner: planner, stores: stores, plans: requestmemo.New[sessionPlanMemoRequest, PlanResult]()}
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

func (s *Service) PlanSession(ctx context.Context, req serverapi.SessionPlanRequest) (serverapi.SessionPlanResponse, error) {
	result, err := s.PlanLaunchSession(ctx, req)
	if err != nil {
		return serverapi.SessionPlanResponse{}, err
	}
	return sessionPlanResponseFromResult(result), nil
}

func (s *Service) PlanLaunchSession(ctx context.Context, req serverapi.SessionPlanRequest) (PlanResult, error) {
	if err := req.Validate(); err != nil {
		return PlanResult{}, err
	}
	selectedSessionID := strings.TrimSpace(req.SelectedSessionID)
	forceNewSession := req.ForceNewSession
	parentSessionID := req.ParentSessionID
	if err := req.Intent.Validate(); err == nil {
		switch req.Intent.Kind() {
		case serverapi.SessionLaunchIntentOpenExisting:
			sessionID, _ := req.Intent.SessionID()
			selectedSessionID = sessionID.String()
			forceNewSession = false
		case serverapi.SessionLaunchIntentCreateNew:
			forceNewSession = true
			if parentID, ok := req.Intent.ParentID(); ok {
				parent := parentID.String()
				parentSessionID = &parent
			}
		}
	}
	overrides, err := req.Overrides.CanonicalKey()
	if err != nil {
		return PlanResult{}, err
	}
	memoReq := sessionPlanMemoRequest{
		Mode:              req.Mode,
		SelectedSessionID: selectedSessionID,
		ForceNewSession:   forceNewSession,
		CallerSessionID:   serverapi.CanonicalOptionalString(req.CallerSessionID),
		ParentSessionID:   serverapi.CanonicalOptionalString(parentSessionID),
		Overrides:         overrides,
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
				callerContext, callerErr := launch.ResolveSessionCallerContext(planner.Config.PersistenceRoot, *req.CallerSessionID)
				if callerErr != nil {
					return PlanResult{}, &serverapi.SubagentLaunchDeniedError{Kind: serverapi.SubagentLaunchDenialCallerMissing}
				}
				caller = &subagentpolicy.Caller{
					Workflow:  callerContext.WorkflowSession,
					AgentRole: callerContext.AgentRole,
				}
				if parentSessionID != nil && strings.TrimSpace(*parentSessionID) != strings.TrimSpace(*req.CallerSessionID) {
					return PlanResult{}, &serverapi.SubagentLaunchDeniedError{Kind: serverapi.SubagentLaunchDenialInvalidTarget}
				}
			}
			if parentSessionID != nil && req.CallerSessionID == nil {
				if _, parentErr := launch.ResolveSessionCallerContext(planner.Config.PersistenceRoot, *parentSessionID); parentErr != nil {
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
		preparation := launch.RunPromptPreparationContext{}
		if selectedSessionID != "" {
			selectedLocked, selectedErr := planner.SelectedSessionLockedContract(selectedSessionID)
			if selectedErr != nil {
				return PlanResult{}, selectedErr
			}
			preparation.ModelLock = selectedLocked
			preparation.ToolLock = selectedLocked
			if !roleOverride.Present {
				target, targetErr := planner.SelectedSessionPromptFacingTarget(selectedSessionID)
				if targetErr != nil {
					return PlanResult{}, targetErr
				}
				preparation.OmittedTarget = &target
			}
		}
		preparedOverrides, err := launch.PrepareRunPromptOverridesWithContext(planner.Config, req.Overrides, authState, preparation)
		if err != nil {
			return PlanResult{}, err
		}
		var preparedPromptFacingTarget *launch.PreparedBaseTarget
		if req.Mode == serverapi.SessionLaunchModeHeadless && preparedOverrides.BaseTarget != nil {
			target := *preparedOverrides.BaseTarget
			preparedPromptFacingTarget = &target
		} else if req.Mode == serverapi.SessionLaunchModeHeadless && preparedOverrides.NamedTarget != nil {
			preparedPromptFacingTarget = &launch.PreparedBaseTarget{
				Settings:     preparedOverrides.NamedTarget.Settings,
				Source:       preparedOverrides.NamedTarget.Source,
				EnabledTools: preparedOverrides.NamedTarget.EnabledTools,
			}
		}
		if req.Mode != serverapi.SessionLaunchModeHeadless && !roleOverride.Present {
			preparedOverrides.BaseTarget = nil
		}
		plan, err := planner.PlanSession(ctx, launch.SessionRequest{
			Mode:                                launch.Mode(req.Mode),
			SelectedSessionID:                   selectedSessionID,
			ForceNewSession:                     forceNewSession,
			ParentSessionID:                     parentSessionID,
			SkipContinuationAgentRoleValidation: roleOverride.Default,
			PreparedPromptFacingTarget:          preparedPromptFacingTarget,
		})
		if err != nil {
			return PlanResult{}, err
		}
		plan, warnings, err := launch.ApplyPreparedRunPromptOverrides(plan, req.Overrides, preparedOverrides)
		if err != nil {
			return PlanResult{}, err
		}
		if s.promptHistory != nil {
			history, err := s.promptHistory.ReadPromptHistory(ctx, plan.Store.Meta().SessionID)
			if err != nil {
				return PlanResult{}, err
			}
			plan.PromptHistory = history
		}
		if s.stores != nil {
			s.stores.RegisterStore(plan.Store)
		}
		return PlanResult{Plan: plan, Warnings: warnings}, nil
	})
}

func sessionPlanResponseFromResult(result PlanResult) serverapi.SessionPlanResponse {
	enabledToolIDs := make([]string, 0, len(result.Plan.EnabledTools))
	for _, id := range result.Plan.EnabledTools {
		enabledToolIDs = append(enabledToolIDs, string(id))
	}
	return serverapi.SessionPlanResponse{Plan: serverapi.SessionPlan{
		SessionID:           result.Plan.Store.Meta().SessionID,
		ActiveSettings:      result.Plan.ActiveSettings,
		EnabledToolIDs:      enabledToolIDs,
		ConfiguredModelName: result.Plan.ConfiguredModelName,
		SessionName:         result.Plan.SessionName,
		PromptHistory:       append([]string(nil), result.Plan.PromptHistory...),
		ModelContractLocked: result.Plan.ModelContractLocked,
		WorkspaceRoot:       result.Plan.WorkspaceRoot,
		Source:              result.Plan.Source,
	}, Warnings: result.Warnings}
}

func sameSessionPlanMemoRequest(a sessionPlanMemoRequest, b sessionPlanMemoRequest) bool {
	return a.Mode == b.Mode &&
		a.SelectedSessionID == b.SelectedSessionID &&
		a.ForceNewSession == b.ForceNewSession &&
		a.CallerSessionID == b.CallerSessionID &&
		a.ParentSessionID == b.ParentSessionID &&
		a.Overrides == b.Overrides
}

var _ servicecontract.SessionLaunchService = (*Service)(nil)
