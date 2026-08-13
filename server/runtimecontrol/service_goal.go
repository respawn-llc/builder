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
	"core/server/runtimeview"
	"core/server/session"
	servicecontract "core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (s *Service) ShowGoal(ctx context.Context, req serverapi.RuntimeGoalShowRequest) (serverapi.RuntimeGoalShowResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeGoalShowRequest]) (serverapi.RuntimeGoalShowResponse, error) {
		return s.ShowGoalValidated(ctx, validated)
	})
}

func (s *Service) ShowGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalShowRequest]) (serverapi.RuntimeGoalShowResponse, error) {
	req := validated.Value()
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
	return serverapi.RuntimeGoalShowResponse{GoalEnvelope: clientui.GoalEnvelope{Goal: runtimeview.GoalCoreFromSessionState(record.Meta.Goal), Availability: runtimeview.GoalAvailabilityFromSession(availability)}}, nil
}

func (s *Service) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeGoalSetRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
		return s.SetGoalValidated(ctx, validated)
	})
}

func (s *Service) SetGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalSetRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
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
	return memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goals, sameGoalSetMemoRequest, true, func(ctx context.Context) (runtimecommand.GoalCommandResult, error) {
		return s.goalAuthority.Set(ctx, runtimecommand.GoalSetCommand{
			SessionID: sessionID,
			Objective: memoReq.Objective,
			Actor:     session.GoalActor(memoReq.Actor),
			Execution: goalExecutionIdentity(memoReq.RunID, memoReq.StepID),
		})
	})
}

func (s *Service) PauseGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
		return s.PauseGoalValidated(ctx, validated)
	})
}

func (s *Service) PauseGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatusValidated(ctx, validated, session.GoalStatusPaused)
}

func (s *Service) ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
		return s.ResumeGoalValidated(ctx, validated)
	})
}

func (s *Service) ResumeGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatusValidated(ctx, validated, session.GoalStatusActive)
}

func (s *Service) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
		return s.CompleteGoalValidated(ctx, validated)
	})
}

func (s *Service) CompleteGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatusValidated(ctx, validated, session.GoalStatusComplete)
}

func (s *Service) setGoalStatusValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest], status session.GoalStatus) (serverapi.RuntimeGoalMutationResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
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
	return memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goalStatuses, sameGoalStatusMemoRequest, false, func(ctx context.Context) (runtimecommand.GoalCommandResult, error) {
		return s.goalAuthority.Status(ctx, runtimecommand.GoalStatusCommand{
			SessionID: sessionID,
			Status:    status,
			Actor:     session.GoalActor(memoReq.Actor),
			Execution: goalExecutionIdentity(memoReq.RunID, memoReq.StepID),
		})
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

func (s *Service) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return servicecontract.WithValidated(req, servicecontract.SemanticValidationRequired, func(validated servicecontract.Validated[serverapi.RuntimeGoalClearRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
		return s.ClearGoalValidated(ctx, validated)
	})
}

func (s *Service) ClearGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalClearRequest]) (serverapi.RuntimeGoalMutationResponse, error) {
	req := validated.Value()
	sessionID, err := runtimeids.ParseSessionID(req.SessionID)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	memoReq := goalClearMemoRequest{SessionID: sessionID.String(), Actor: strings.TrimSpace(req.Actor)}
	return memoizedGoalMutation(s, ctx, strings.TrimSpace(req.ClientRequestID), memoReq, s.goalClears, sameGoalClearMemoRequest, false, func(ctx context.Context) (runtimecommand.GoalCommandResult, error) {
		return s.goalAuthority.Clear(ctx, runtimecommand.GoalClearCommand{
			SessionID: sessionID,
			Actor:     session.GoalActor(memoReq.Actor),
		})
	})
}

func memoizedGoalMutation[Req any](
	service *Service,
	ctx context.Context,
	requestID string,
	req Req,
	memo *requestmemo.Memo[Req, committedGoalMutationResult],
	same func(Req, Req) bool,
	allowPendingPreview bool,
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
		response, responseErr := goalMutationResponseFromCommand(outcome, allowPendingPreview)
		if responseErr != nil {
			return committedGoalMutationResult{}, goalMutationError(responseErr)
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

func goalMutationResponseFromCommand(result runtimecommand.GoalCommandResult, allowPendingPreview bool) (serverapi.RuntimeGoalMutationResponse, error) {
	var availability *clientui.GoalAvailability
	if result.Availability != nil {
		projected := runtimeview.GoalAvailabilityFromSession(*result.Availability)
		availability = &projected
	}
	if result.Cleared {
		return serverapi.RuntimeGoalMutationResponse{Availability: availability}, nil
	}
	if result.Disposition == runtime.GoalCommandQueued {
		if !allowPendingPreview {
			return serverapi.RuntimeGoalMutationResponse{Availability: availability}, nil
		}
		if result.Goal == nil {
			return serverapi.RuntimeGoalMutationResponse{}, errors.New("queued goal command is missing preview")
		}
		return serverapi.RuntimeGoalMutationResponse{Pending: &clientui.GoalPreview{Objective: result.Goal.Objective, Status: clientui.RuntimeGoalStatus(result.Goal.Status)}, Availability: availability}, nil
	}
	if result.Goal == nil {
		return serverapi.RuntimeGoalMutationResponse{}, errors.New("accepted goal command is missing projected goal")
	}
	return serverapi.RuntimeGoalMutationResponse{Goal: runtimeview.GoalCoreFromSessionState(result.Goal), Availability: availability}, nil
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
