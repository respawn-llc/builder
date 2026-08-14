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
	sessionID, err := serverapi.PrepareRuntimeGoalShowRequest(req)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	return s.showGoal(ctx, sessionID)
}

func (s *Service) ShowGoalValidated(ctx context.Context, _ servicecontract.Validated[serverapi.RuntimeGoalShowRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeGoalShowResponse, error) {
	return s.showGoal(ctx, authorization.SessionID)
}

func (s *Service) showGoal(ctx context.Context, sessionID runtimeids.SessionID) (serverapi.RuntimeGoalShowResponse, error) {
	if s == nil || s.persisted == nil {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("persisted session resolver is required")
	}
	record, err := s.persisted.ResolvePersistedSession(ctx, sessionID.String())
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, fmt.Errorf("resolve persisted session %q: %w", sessionID.String(), err)
	}
	if record.Meta == nil {
		return serverapi.RuntimeGoalShowResponse{}, fmt.Errorf("persisted session %q metadata is required", sessionID.String())
	}
	availability, err := session.GoalAvailabilityFromMeta(*record.Meta)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, fmt.Errorf("resolve Goal availability for session %q: %w", sessionID, err)
	}
	return serverapi.RuntimeGoalShowResponse{GoalEnvelope: clientui.GoalEnvelope{Goal: runtimeview.GoalCoreFromSessionState(record.Meta.Goal), Availability: runtimeview.GoalAvailabilityFromSession(availability)}}, nil
}

func (s *Service) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	sessionID, err := serverapi.PrepareRuntimeGoalSetRequest(req)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	return s.setGoal(ctx, req, sessionID)
}

func (s *Service) SetGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalSetRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoal(ctx, validated.Value(), authorization.SessionID)
}

func (s *Service) setGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest, sessionID runtimeids.SessionID) (serverapi.RuntimeGoalMutationResponse, error) {
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
	return s.validateAndSetGoalStatus(ctx, req, session.GoalStatusPaused)
}

func (s *Service) ResumeGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.validateAndSetGoalStatus(ctx, req, session.GoalStatusActive)
}

func (s *Service) CompleteGoal(ctx context.Context, req serverapi.RuntimeGoalStatusRequest) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.validateAndSetGoalStatus(ctx, req, session.GoalStatusComplete)
}

func (s *Service) validateAndSetGoalStatus(ctx context.Context, req serverapi.RuntimeGoalStatusRequest, status session.GoalStatus) (serverapi.RuntimeGoalMutationResponse, error) {
	sessionID, err := serverapi.PrepareRuntimeGoalStatusRequest(req)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	return s.setGoalStatus(ctx, req, sessionID, status)
}

func (s *Service) PauseGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatus(ctx, validated.Value(), authorization.SessionID, session.GoalStatusPaused)
}

func (s *Service) ResumeGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatus(ctx, validated.Value(), authorization.SessionID, session.GoalStatusActive)
}

func (s *Service) CompleteGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalStatusRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.setGoalStatus(ctx, validated.Value(), authorization.SessionID, session.GoalStatusComplete)
}

func (s *Service) setGoalStatus(ctx context.Context, req serverapi.RuntimeGoalStatusRequest, sessionID runtimeids.SessionID, status session.GoalStatus) (serverapi.RuntimeGoalMutationResponse, error) {
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
	sessionID, err := serverapi.PrepareRuntimeGoalClearRequest(req)
	if err != nil {
		return serverapi.RuntimeGoalMutationResponse{}, err
	}
	return s.clearGoal(ctx, req, sessionID)
}

func (s *Service) ClearGoalValidated(ctx context.Context, validated servicecontract.Validated[serverapi.RuntimeGoalClearRequest], authorization servicecontract.AuthorizedSessionInActiveProject) (serverapi.RuntimeGoalMutationResponse, error) {
	return s.clearGoal(ctx, validated.Value(), authorization.SessionID)
}

func (s *Service) clearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest, sessionID runtimeids.SessionID) (serverapi.RuntimeGoalMutationResponse, error) {
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
