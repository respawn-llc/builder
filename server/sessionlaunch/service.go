package sessionlaunch

import (
	"context"
	"strings"

	"core/server/auth"
	"core/server/launch"
	"core/server/requestmemo"
	"core/server/session"
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
	ParentSessionID   string
	Overrides         serverapi.RunPromptOverrides
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
	memoReq := sessionPlanMemoRequest{
		Mode:              req.Mode,
		SelectedSessionID: strings.TrimSpace(req.SelectedSessionID),
		ForceNewSession:   req.ForceNewSession,
		ParentSessionID:   strings.TrimSpace(req.ParentSessionID),
		Overrides:         req.Overrides,
	}
	return s.plans.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameSessionPlanMemoRequest, func(ctx context.Context) (PlanResult, error) {
		roleOverride, err := req.Overrides.AgentRoleOverride()
		if err != nil {
			return PlanResult{}, err
		}
		plan, err := s.planner.PlanSession(ctx, launch.SessionRequest{
			Mode:                                launch.Mode(req.Mode),
			SelectedSessionID:                   req.SelectedSessionID,
			ForceNewSession:                     req.ForceNewSession,
			ParentSessionID:                     req.ParentSessionID,
			SkipContinuationAgentRoleValidation: roleOverride.Default,
		})
		if err != nil {
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
		plan, warnings, err := launch.ApplyRunPromptOverrides(plan, req.Overrides, authState)
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
		a.ParentSessionID == b.ParentSessionID &&
		a.Overrides == b.Overrides
}

var _ servicecontract.SessionLaunchService = (*Service)(nil)
