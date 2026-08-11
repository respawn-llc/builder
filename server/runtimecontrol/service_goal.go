package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimecommand"
	"core/server/session"
	"core/shared/clientui"
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
	availability, err := session.GoalAvailabilityFromMeta(*record.Meta)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, fmt.Errorf("resolve Goal availability for session %q: %w", sessionID, err)
	}
	return serverapi.RuntimeGoalShowResponse{Goal: session.GoalCoreFromState(record.Meta.Goal), Availability: availability}, nil
}

func (s *Service) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	memoReq := goalSetMemoRequest{
		SessionID: sessionID.String(),
		Objective: strings.TrimSpace(req.Objective),
		Actor:     strings.TrimSpace(req.Actor),
		RunID:     strings.TrimSpace(req.RunID),
		StepID:    strings.TrimSpace(req.StepID),
	}
	return memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goals, sameGoalSetMemoRequest, func(ctx context.Context) (runtimecommand.GoalCommandResult, error) {
		return s.goalAuthority.Set(ctx, runtimecommand.GoalSetCommand{
			SessionID: sessionID,
			Objective: memoReq.Objective,
			Actor:     session.GoalActor(memoReq.Actor),
			Execution: goalExecutionIdentity(memoReq.RunID, memoReq.StepID),
		})
	})
}

func (s *Service) PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusPaused)
}

func (s *Service) ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusActive)
}

func (s *Service) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatus(ctx, req, session.GoalStatusComplete)
}

func (s *Service) setGoalStatus(ctx context.Context, req serverapi.RuntimeGoalStatusRequest, status session.GoalStatus) (serverapi.RuntimeGoalMutationResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	memoReq := goalStatusMemoRequest{
		SessionID: sessionID.String(),
		Status:    string(status),
		Actor:     strings.TrimSpace(req.Actor),
		RunID:     strings.TrimSpace(req.RunID),
		StepID:    strings.TrimSpace(req.StepID),
	}
	mutation, err := memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goalStatuses, sameGoalStatusMemoRequest, func(ctx context.Context) (runtimecommand.GoalCommandResult, error) {
		return s.goalAuthority.Status(ctx, runtimecommand.GoalStatusCommand{
			SessionID: sessionID,
			Status:    status,
			Actor:     session.GoalActor(memoReq.Actor),
			Execution: goalExecutionIdentity(memoReq.RunID, memoReq.StepID),
		})
	})
	return mutation, err
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

func (s *Service) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	memoReq := goalClearMemoRequest{SessionID: sessionID.String(), Actor: strings.TrimSpace(req.Actor)}
	mutation, err := memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goalClears, sameGoalClearMemoRequest, func(ctx context.Context) (runtimecommand.GoalCommandResult, error) {
		return s.goalAuthority.Clear(ctx, runtimecommand.GoalClearCommand{
			SessionID: sessionID,
			Actor:     session.GoalActor(memoReq.Actor),
		})
	})
	return mutation, err
}

func memoizedGoalMutation[Req any](
	service *Service,
	ctx context.Context,
	requestID string,
	req Req,
	memo *requestmemo.Memo[Req, committedGoalMutationResult],
	same func(Req, Req) bool,
	run func(context.Context) (runtimecommand.GoalCommandResult, error),
) (serverapi.RuntimeGoalMutationResponse, error) {
	if service == nil || service.goalAuthority == nil {
		return serverapi.RuntimeGoalMutationResponse{}, errors.New("goal command authority is required")
	}
	result, err := memo.Do(ctx, requestID, req, same, func(ctx context.Context) (committedGoalMutationResult, error) {
		outcome, outerErr := run(ctx)
		if outerErr != nil {
			return committedGoalMutationResult{}, goalMutationError(outerErr)
		}
		response, responseErr := goalMutationResponseFromCommand(outcome)
		if responseErr != nil {
			return committedGoalMutationResult{Err: goalMutationError(responseErr)}, nil
		}
		return committedGoalMutationResult{
			Response: response,
			Err:      goalMutationError(outcome.Err),
		}, nil
	})
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, goalMutationError(err)
	}
	return result.Response, result.Err
}

func goalMutationResponseFromCommand(result runtimecommand.GoalCommandResult) (serverapi.RuntimeGoalMutationResponse, error) {
	if err := result.Availability.Validate(); err != nil {
		if result.Err != nil {
			return serverapi.RuntimeGoalMutationResponse{}, result.Err
		}
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	if result.Cleared {
		return serverapi.RuntimeGoalMutationResponse{Availability: result.Availability}, nil
	}
	if result.Disposition == runtime.GoalCommandQueued {
		if result.Goal == nil {
			return serverapi.RuntimeGoalMutationResponse{}, errors.New("queued goal command is missing preview")
		}
		if projected := session.GoalCoreFromState(result.Goal); projected != nil && strings.TrimSpace(projected.ID) != "" {
			return serverapi.RuntimeGoalMutationResponse{Goal: projected, Availability: result.Availability}, nil
		}
		return serverapi.RuntimeGoalMutationResponse{Pending: &clientui.GoalPreview{Objective: result.Goal.Objective, Status: clientui.RuntimeGoalStatus(result.Goal.Status)}, Availability: result.Availability}, nil
	}
	if result.Goal == nil {
		return serverapi.RuntimeGoalMutationResponse{}, errors.New("accepted goal command is missing projected goal")
	}
	return serverapi.RuntimeGoalMutationResponse{Goal: session.GoalCoreFromState(result.Goal), Availability: result.Availability}, nil
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
