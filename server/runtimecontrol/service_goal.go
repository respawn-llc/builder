package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/requestmemo"
	"core/server/runtimecommand"
	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (s *Service) ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	if s == nil || s.persisted == nil {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("persisted session resolver is required")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	record, err := s.persisted.ResolvePersistedSession(ctx, sessionID)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, fmt.Errorf("resolve persisted session %q: %w", sessionID, err)
	}
	if record.Meta == nil {
		return serverapi.RuntimeGoalShowResponse{}, fmt.Errorf("persisted session %q metadata is required", sessionID)
	}
	if record.Meta.Goal == nil {
		return serverapi.RuntimeGoalShowResponse{}, nil
	}
	return serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(*record.Meta.Goal)}, nil
}

func (s *Service) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	memoReq := goalSetMemoRequest{
		SessionID: sessionID.String(),
		Objective: strings.TrimSpace(req.Objective),
		Actor:     strings.TrimSpace(req.Actor),
		RunID:     strings.TrimSpace(req.RunID),
		StepID:    strings.TrimSpace(req.StepID),
	}
	return memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goals, sameGoalSetMemoRequest, func(ctx context.Context, admit runtimecommand.SessionAgentOperationAdmitter) (runtimecommand.GoalCommandResult, bool, error) {
		return s.goalAuthority.SetAdmitted(ctx, runtimecommand.GoalSetCommand{
			SessionID: sessionID,
			Objective: memoReq.Objective,
			Actor:     session.GoalActor(memoReq.Actor),
			Execution: goalExecutionIdentity(memoReq.RunID, memoReq.StepID),
		}, admit)
	})
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
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	memoReq := goalStatusMemoRequest{
		SessionID: sessionID.String(),
		Status:    string(status),
		Actor:     strings.TrimSpace(req.Actor),
		RunID:     strings.TrimSpace(req.RunID),
		StepID:    strings.TrimSpace(req.StepID),
	}
	return memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goalStatuses, sameGoalStatusMemoRequest, func(ctx context.Context, admit runtimecommand.SessionAgentOperationAdmitter) (runtimecommand.GoalCommandResult, bool, error) {
		return s.goalAuthority.StatusAdmitted(ctx, runtimecommand.GoalStatusCommand{
			SessionID: sessionID,
			Status:    status,
			Actor:     session.GoalActor(memoReq.Actor),
			Execution: goalExecutionIdentity(memoReq.RunID, memoReq.StepID),
		}, admit)
	})
}

func goalExecutionIdentity(runID string, stepID string) runtimecommand.GoalExecutionIdentity {
	return runtimecommand.GoalExecutionIdentity{
		RunID:  optionalGoalExecutionID(runID),
		StepID: optionalGoalExecutionID(stepID),
	}
}

func optionalGoalExecutionID(raw string) *string {
	normalized := strings.TrimSpace(raw)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func (s *Service) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	memoReq := goalClearMemoRequest{SessionID: sessionID.String(), Actor: strings.TrimSpace(req.Actor)}
	return memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goalClears, sameGoalClearMemoRequest, func(ctx context.Context, admit runtimecommand.SessionAgentOperationAdmitter) (runtimecommand.GoalCommandResult, bool, error) {
		return s.goalAuthority.ClearAdmitted(ctx, runtimecommand.GoalClearCommand{
			SessionID: sessionID,
			Actor:     session.GoalActor(memoReq.Actor),
		}, admit)
	})
}

func memoizedGoalMutation[Req any](
	service *Service,
	ctx context.Context,
	requestID string,
	req Req,
	memo *requestmemo.Memo[Req, committedGoalMutationResult],
	same func(Req, Req) bool,
	run func(context.Context, runtimecommand.SessionAgentOperationAdmitter) (runtimecommand.GoalCommandResult, bool, error),
) (serverapi.RuntimeGoalShowResponse, error) {
	if service == nil || service.goalAuthority == nil {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("goal command authority is required")
	}
	result, err := memo.DoAdmitted(ctx, requestID, req, same, func(prepareCtx context.Context, admission *requestmemo.Admission[committedGoalMutationResult]) {
		admitted := false
		outcome, routed, outerErr := run(prepareCtx, func(start runtimecommand.SessionAgentOperationStart) error {
			work, admitErr := admission.Admit()
			if admitErr != nil {
				return admitErr
			}
			admitted = true
			go func() {
				operationOutcome, runErr := start()
				goalOutcome, ok := operationOutcome.(runtimecommand.GoalMutationOperationOutcome)
				if !ok {
					_ = work.Complete(
						committedGoalMutationResult{},
						errors.New("admitted Goal driver returned the wrong outcome"),
					)
					return
				}
				if goalOutcome.Result.Accepted() {
					goalOutcome.Result.Err = runErr
					committed, resultErr := committedGoalMutation(goalOutcome.Result)
					_ = work.Complete(committed, resultErr)
					return
				}
				_ = work.Complete(committedGoalMutationResult{}, goalMutationError(runErr))
			}()
			return nil
		})
		if admitted || routed {
			if outerErr != nil && !admitted {
				_ = admission.Reject(goalMutationError(outerErr))
			}
			return
		}
		if outerErr != nil {
			_ = admission.Reject(goalMutationError(outerErr))
			return
		}
		committed, resultErr := committedGoalMutation(outcome)
		_ = admission.Complete(committed, resultErr)
	})
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, goalMutationError(err)
	}
	return result.Response, result.Err
}

func committedGoalMutation(outcome runtimecommand.GoalCommandResult) (committedGoalMutationResult, error) {
	response, err := goalResponseFromCommand(outcome)
	if err != nil {
		return committedGoalMutationResult{}, err
	}
	return committedGoalMutationResult{
		Response: response,
		Err:      goalMutationError(outcome.Err),
	}, nil
}

func goalResponseFromCommand(result runtimecommand.GoalCommandResult) (serverapi.RuntimeGoalShowResponse, error) {
	if result.Cleared {
		return serverapi.RuntimeGoalShowResponse{}, nil
	}
	if result.Goal == nil {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("accepted goal command is missing projected goal")
	}
	return serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(*result.Goal)}, nil
}

// goalAgentOverwriteDeniedError preserves the agent-facing policy response while
// keeping overwrite policy in the Store.
type goalAgentOverwriteDeniedError struct {
	Objective string
	Status    string
}

func (e goalAgentOverwriteDeniedError) Error() string {
	return strings.TrimSpace(prompts.RenderGoalAgentDuplicateSetDeniedPrompt(e.Objective, e.Status))
}

func goalMutationError(err error) error {
	if err == nil {
		return nil
	}
	var blocked session.GoalAgentOverwriteBlockedError
	if errors.As(err, &blocked) {
		return goalAgentOverwriteDeniedError{Objective: blocked.Goal.Objective, Status: string(blocked.Goal.Status)}
	}
	return err
}

func runtimeGoalFromSessionGoal(goal session.GoalState) *serverapi.RuntimeGoal {
	return &serverapi.RuntimeGoal{
		ID:        strings.TrimSpace(goal.ID),
		Objective: goal.Objective,
		Status:    strings.TrimSpace(string(goal.Status)),
		CreatedAt: goal.CreatedAt,
		UpdatedAt: goal.UpdatedAt,
	}
}
