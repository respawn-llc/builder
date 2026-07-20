package sessionlaunch

import (
	"context"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/requestmemo"
	"core/server/subagentpolicy"
	servicecontract "core/shared/apicontract"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
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
	plans         *requestmemo.Memo[sessionPlanMemoRequest, PlanResult]
}

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
		if req.Mode == serverapi.SessionLaunchModeHeadless && selectedSessionID != nil && !roleOverride.Present {
			persistedRole, roleErr := planner.SelectedSessionContinuationAgentRole(*selectedSessionID)
			if roleErr != nil {
				return PlanResult{}, roleErr
			}
			if persistedRole != nil && caller != nil {
				lookup := config.LookupSubagentRole(planner.Config.Settings, *persistedRole)
				if lookup.Status == config.SubagentRoleLookupPresent {
					persistedOverride, overrideErr := (serverapi.RunPromptOverrides{AgentRole: persistedRole}).AgentRoleOverride()
					if overrideErr != nil {
						return PlanResult{}, overrideErr
					}
					if err := subagentpolicy.Authorize(planner.Config.Settings, caller, subagentpolicy.TargetFromOverride(persistedOverride)); err != nil {
						return PlanResult{}, err
					}
				}
			}
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
		if selectedSessionID != nil {
			selectedLocked, selectedErr := planner.SelectedSessionLockedContract(*selectedSessionID)
			if selectedErr != nil {
				return PlanResult{}, selectedErr
			}
			preparation.ModelLock = selectedLocked
			preparation.ToolLock = selectedLocked
			if !roleOverride.Present {
				target, targetErr := planner.SelectedSessionPromptFacingTarget(*selectedSessionID)
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
			Intent:                              req.Intent,
			SkipContinuationAgentRoleValidation: roleOverride.Default,
			PreparedPromptFacingTarget:          preparedPromptFacingTarget,
		})
		if err != nil {
			return PlanResult{}, err
		}
		plan, warnings, err := planner.ApplyPreparedRunPromptOverrides(plan, req.Overrides, preparedOverrides)
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
	})
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
		SessionName:         cloneOptionalString(result.Plan.SessionName),
		PromptHistory:       append([]string(nil), result.Plan.PromptHistory...),
		ModelContractLocked: result.Plan.ModelContractLocked,
		Source:              result.Plan.Source,
	}, Warnings: result.Warnings}
}

func cloneOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func sameSessionPlanMemoRequest(a sessionPlanMemoRequest, b sessionPlanMemoRequest) bool {
	return a.Mode == b.Mode &&
		a.Intent.Equal(b.Intent) &&
		a.CallerSessionID == b.CallerSessionID &&
		a.Overrides == b.Overrides
}

var _ servicecontract.SessionLaunchService = (*Service)(nil)
