package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/prompts"
	"core/server/runtime"
	"core/server/session"
	"core/shared/serverapi"
)

func (s *Service) ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	engine, err := s.resolve(ctx, req.SessionID)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	goal := engine.Goal()
	if goal == nil {
		return serverapi.RuntimeGoalShowResponse{}, nil
	}
	return serverapi.RuntimeGoalShowResponse{Goal: &serverapi.RuntimeGoal{
		ID:        strings.TrimSpace(goal.ID),
		Objective: goal.Objective,
		Status:    strings.TrimSpace(string(goal.Status)),
		Suspended: engine.GoalLoopSuspended(),
		CreatedAt: goal.CreatedAt,
		UpdatedAt: goal.UpdatedAt,
	}}, nil
}

func (s *Service) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	trimmedObjective := strings.TrimSpace(req.Objective)
	memoReq := goalSetMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Objective: trimmedObjective, Actor: strings.TrimSpace(req.Actor), RunID: strings.TrimSpace(req.RunID), StepID: strings.TrimSpace(req.StepID)}
	return s.goals.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameGoalSetMemoRequest, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		var response serverapi.RuntimeGoalShowResponse
		err := s.withGoalMutationAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			if engine.WorkflowRunConfigured() && !requestOriginatesFromAgentStep(req.Actor, req.StepID) {
				goal, err := engine.SetGoal(trimmedObjective, session.GoalActor(req.Actor))
				if err != nil {
					var blocked session.GoalAgentOverwriteBlockedError
					if errors.As(err, &blocked) {
						return goalAgentOverwriteDeniedError{Objective: blocked.Goal.Objective, Status: string(blocked.Goal.Status)}
					}
					return err
				}
				response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
				return nil
			}
			goal, queued, qErr := queueGoalSetForRequest(engine, req, trimmedObjective)
			if qErr != nil {
				var blocked session.GoalAgentOverwriteBlockedError
				if errors.As(qErr, &blocked) {
					return goalAgentOverwriteDeniedError{Objective: blocked.Goal.Objective, Status: string(blocked.Goal.Status)}
				}
				return qErr
			}
			if queued {
				response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
				return nil
			}
			if strings.TrimSpace(req.Actor) == string(session.GoalActorAgent) {
				current := engine.Goal()
				if current != nil && current.Status != session.GoalStatusComplete {
					return goalAgentOverwriteDeniedError{Objective: current.Objective, Status: string(current.Status)}
				}
			}
			if err := engine.RequireGoalLoopStartAllowed(); err != nil {
				return err
			}
			goal, err := engine.SetGoal(trimmedObjective, session.GoalActor(req.Actor))
			if err != nil {
				var blocked session.GoalAgentOverwriteBlockedError
				if errors.As(err, &blocked) {
					return goalAgentOverwriteDeniedError{Objective: blocked.Goal.Objective, Status: string(blocked.Goal.Status)}
				}
				return err
			}
			if err := engine.StartGoalLoop(); err != nil {
				return err
			}
			response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
			return nil
		})
		return response, err
	})
}

// goalAgentOverwriteDeniedError is returned when an agent attempts to overwrite an
// existing active or paused goal. It carries the existing goal's objective and status
// so callers can react to the specific denial, and renders the agent-facing denial
// prompt for the surfaced error message.
type goalAgentOverwriteDeniedError struct {
	Objective string
	Status    string
}

func (e goalAgentOverwriteDeniedError) Error() string {
	return strings.TrimSpace(prompts.RenderGoalAgentDuplicateSetDeniedPrompt(e.Objective, e.Status))
}

func (s *Service) PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusPaused)
}

func (s *Service) ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusActive)
}

func (s *Service) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusComplete)
}

func (s *Service) setGoalStatus(ctx context.Context, req serverapi.RuntimeGoalStatusRequest, status session.GoalStatus) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	memoReq := goalStatusMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Status: strings.TrimSpace(string(status)), Actor: strings.TrimSpace(req.Actor), RunID: strings.TrimSpace(req.RunID), StepID: strings.TrimSpace(req.StepID)}
	return s.goalStatuses.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameGoalStatusMemoRequest, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		var response serverapi.RuntimeGoalShowResponse
		err := s.withGoalMutationAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			if engine.WorkflowRunConfigured() && !requestOriginatesFromAgentStep(req.Actor, req.StepID) {
				current := engine.Goal()
				if status == session.GoalStatusActive && current != nil && current.Status == session.GoalStatusActive {
					response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(*current, false)}
					return nil
				}
				if status == session.GoalStatusComplete && current != nil && current.Status == session.GoalStatusComplete {
					response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(*current, false)}
					return nil
				}
				goal, err := engine.SetGoalStatusWithoutGoalLoopStart(status, session.GoalActor(req.Actor))
				if err != nil {
					return err
				}
				response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
				return nil
			}
			goal, queued, qErr := queueGoalStatusForRequest(engine, req, status)
			if qErr != nil {
				return qErr
			}
			if queued {
				response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
				return nil
			}
			if status == session.GoalStatusActive {
				current := engine.Goal()
				if current != nil && current.Status == session.GoalStatusActive && engine.GoalLoopContinuationEnforced() {
					response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(*current, false)}
					return nil
				}
			}
			if status == session.GoalStatusComplete {
				current := engine.Goal()
				if current != nil && current.Status == session.GoalStatusComplete {
					response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(*current, false)}
					return nil
				}
			}
			if status == session.GoalStatusActive {
				if err := engine.RequireGoalLoopStartAllowed(); err != nil {
					return err
				}
			}
			goal, err := engine.SetGoalStatus(status, session.GoalActor(req.Actor))
			if err != nil {
				return err
			}
			if status == session.GoalStatusActive {
				if err := engine.StartGoalLoop(); err != nil {
					return err
				}
			}
			response = serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(goal, false)}
			return nil
		})
		return response, err
	})
}

func (s *Service) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	memoReq := goalClearMemoRequest{SessionID: strings.TrimSpace(req.SessionID), Actor: strings.TrimSpace(req.Actor)}
	return s.goalClears.Do(ctx, strings.TrimSpace(req.ClientRequestID), memoReq, sameGoalClearMemoRequest, func(ctx context.Context) (serverapi.RuntimeGoalShowResponse, error) {
		err := s.withGoalMutationAccess(ctx, req.SessionID, func(engine *runtime.Engine) error {
			_, queued, qErr := engine.QueueGoalClearForActiveStep(session.GoalActor(req.Actor))
			if qErr != nil {
				return qErr
			}
			if queued {
				return nil
			}
			_, err := engine.ClearGoal(session.GoalActor(req.Actor))
			return err
		})
		return serverapi.RuntimeGoalShowResponse{}, err
	})
}

func (s *Service) withGoalMutationAccess(ctx context.Context, sessionID string, fn func(*runtime.Engine) error) error {
	return s.withRuntimeAccess(ctx, sessionID, fn)
}

func queueGoalSetForRequest(engine *runtime.Engine, req serverapi.RuntimeGoalSetRequest, objective string) (session.GoalState, bool, error) {
	actor := session.GoalActor(req.Actor)
	if strings.TrimSpace(req.Actor) == string(session.GoalActorAgent) && strings.TrimSpace(req.StepID) != "" {
		return engine.QueueAgentShellSetGoalForStep(req.StepID, objective, actor)
	}
	return engine.QueueGoalSetForActiveStep(objective, actor)
}

func queueGoalStatusForRequest(engine *runtime.Engine, req serverapi.RuntimeGoalStatusRequest, status session.GoalStatus) (session.GoalState, bool, error) {
	actor := session.GoalActor(req.Actor)
	if requestOriginatesFromAgentStep(req.Actor, req.StepID) {
		return engine.QueueGoalStatusForStep(req.StepID, status, actor)
	}
	return engine.QueueGoalStatusForActiveStep(status, actor)
}

func requestOriginatesFromAgentStep(actor string, stepID string) bool {
	return strings.TrimSpace(actor) == string(session.GoalActorAgent) && strings.TrimSpace(stepID) != ""
}

func runtimeGoalFromSessionGoal(goal session.GoalState, suspended bool) *serverapi.RuntimeGoal {
	return &serverapi.RuntimeGoal{
		ID:        strings.TrimSpace(goal.ID),
		Objective: goal.Objective,
		Status:    strings.TrimSpace(string(goal.Status)),
		Suspended: suspended,
		CreatedAt: goal.CreatedAt,
		UpdatedAt: goal.UpdatedAt,
	}
}
