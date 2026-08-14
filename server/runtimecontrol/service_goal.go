package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/runtime"
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
	mutation := runtime.GoalMutation{
		Kind:      runtime.GoalMutationSet,
		Objective: strings.TrimSpace(req.Objective),
		Actor:     session.GoalActor(strings.TrimSpace(req.Actor)),
		StartLoop: true,
	}
	return s.mutateGoal(ctx, sessionID, req.RunID, req.StepID, mutation)
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

func (s *Service) setGoalStatus(
	ctx context.Context,
	req serverapi.RuntimeGoalStatusRequest,
	status session.GoalStatus,
) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	return s.mutateGoal(ctx, sessionID, req.RunID, req.StepID, runtime.GoalMutation{
		Kind:      runtime.GoalMutationStatus,
		Status:    status,
		Actor:     session.GoalActor(strings.TrimSpace(req.Actor)),
		StartLoop: status == session.GoalStatusActive,
	})
}

func (s *Service) ClearGoal(ctx context.Context, req serverapi.RuntimeGoalClearRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	return s.mutateGoal(ctx, sessionID, "", "", runtime.GoalMutation{
		Kind:  runtime.GoalMutationClear,
		Actor: session.GoalActor(strings.TrimSpace(req.Actor)),
	})
}

func (s *Service) mutateGoal(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	rawRunID string,
	rawStepID string,
	mutation runtime.GoalMutation,
) (serverapi.RuntimeGoalShowResponse, error) {
	if s == nil || s.authority == nil {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("session runtime authority is required")
	}
	runText, stepText := strings.TrimSpace(rawRunID), strings.TrimSpace(rawStepID)
	if mutation.Actor == session.GoalActorAgent && stepText != "" {
		if runText == "" {
			return serverapi.RuntimeGoalShowResponse{}, runtime.ErrAgentGoalStepInactive
		}
		runID, err := runtimeids.ParseRunID(runText)
		if err != nil {
			return serverapi.RuntimeGoalShowResponse{}, err
		}
		stepID, err := runtimeids.ParseStepID(stepText)
		if err != nil {
			return serverapi.RuntimeGoalShowResponse{}, err
		}
		result, err := s.authority.ScheduleAgentGoalMutation(ctx, sessionID, runID, stepID, mutation)
		return goalResponseFromRuntimeResult(result, goalMutationError(err))
	}
	if runText != "" || stepText != "" {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("Goal execution identity requires an agent Step")
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	var dormant runtime.GoalCommandResult
	var dormantErr error
	admission, err := s.authority.WithDormantSessionStore(ctx, descriptor, func(_ context.Context, store *session.Store) error {
		dormant, dormantErr = applyDormantGoalMutation(store, mutation)
		if goalResultAccepted(dormant) {
			return nil
		}
		return dormantErr
	})
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, goalMutationError(err)
	}
	if !admission.RuntimeAvailable {
		return goalResponseFromRuntimeResult(dormant, goalMutationError(dormantErr))
	}
	result, err := s.applyLiveGoalMutation(ctx, sessionID, mutation)
	return goalResponseFromRuntimeResult(result, goalMutationError(err))
}

func (s *Service) applyLiveGoalMutation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	mutation runtime.GoalMutation,
) (runtime.GoalCommandResult, error) {
	var result runtime.GoalCommandResult
	apply := func(_ context.Context, engine *runtime.Engine) error {
		var err error
		result, err = engine.ApplyGoalMutationDeferred(mutation)
		return err
	}
	err := s.authority.WithCurrentRuntime(ctx, sessionID, apply)
	if goalResultAccepted(result) {
		return result, err
	}
	return runtime.GoalCommandResult{}, err
}

func applyDormantGoalMutation(store *session.Store, mutation runtime.GoalMutation) (runtime.GoalCommandResult, error) {
	if store == nil {
		return runtime.GoalCommandResult{}, errors.New("session store is required")
	}
	switch mutation.Kind {
	case runtime.GoalMutationSet:
		goal, metadataReceipt, err := store.SetGoal(mutation.Objective, mutation.Actor)
		result := runtimeGoalResult(goal, false, runtime.GoalCommandApplied, metadataReceipt, session.CommitReceipt{})
		if err != nil || !metadataReceipt.Committed {
			return result, err
		}
		noticeReceipt, noticeErr := runtime.SteerPersistedGoalNotice(store, runtime.GoalNoticeSet, &goal)
		result.NoticeReceipt = noticeReceipt
		return result, noticeErr
	case runtime.GoalMutationStatus:
		if current := store.Meta().Goal; current != nil && current.Status == mutation.Status {
			return runtimeGoalResult(*current, false, runtime.GoalCommandNoop, session.CommitReceipt{}, session.CommitReceipt{}), nil
		}
		goal, transitioned, metadataReceipt, err := store.SetGoalStatus(mutation.Status, mutation.Actor)
		disposition := runtime.GoalCommandApplied
		if err == nil && !transitioned {
			disposition = runtime.GoalCommandNoop
		}
		result := runtimeGoalResult(goal, false, disposition, metadataReceipt, session.CommitReceipt{})
		if err != nil || !transitioned || !metadataReceipt.Committed {
			return result, err
		}
		noticeReceipt, noticeErr := runtime.SteerPersistedGoalNotice(store, runtime.GoalNoticeStatus, &goal)
		result.NoticeReceipt = noticeReceipt
		return result, noticeErr
	case runtime.GoalMutationClear:
		goal, metadataReceipt, err := store.ClearGoal(mutation.Actor)
		result := runtimeGoalResult(goal, true, runtime.GoalCommandApplied, metadataReceipt, session.CommitReceipt{})
		if err != nil || !metadataReceipt.Committed {
			return result, err
		}
		noticeReceipt, noticeErr := runtime.SteerPersistedGoalNotice(store, runtime.GoalNoticeClear, nil)
		result.NoticeReceipt = noticeReceipt
		return result, noticeErr
	default:
		return runtime.GoalCommandResult{}, fmt.Errorf("unsupported Goal mutation kind %d", mutation.Kind)
	}
}

func runtimeGoalResult(
	goal session.GoalState,
	cleared bool,
	disposition runtime.GoalCommandDisposition,
	metadataReceipt session.CommitReceipt,
	noticeReceipt session.CommitReceipt,
) runtime.GoalCommandResult {
	return runtime.GoalCommandResult{
		GoalState:       goal,
		Cleared:         cleared,
		Disposition:     disposition,
		MetadataReceipt: metadataReceipt,
		NoticeReceipt:   noticeReceipt,
	}
}

func goalResultAccepted(result runtime.GoalCommandResult) bool {
	return result.Disposition == runtime.GoalCommandQueued ||
		result.Disposition == runtime.GoalCommandNoop ||
		result.MetadataReceipt.Committed ||
		result.NoticeReceipt.Committed
}

func goalResponseFromRuntimeResult(
	result runtime.GoalCommandResult,
	err error,
) (serverapi.RuntimeGoalShowResponse, error) {
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	if result.Cleared {
		return serverapi.RuntimeGoalShowResponse{}, nil
	}
	if result.Disposition == 0 {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("accepted Goal mutation is missing a result")
	}
	return serverapi.RuntimeGoalShowResponse{Goal: runtimeGoalFromSessionGoal(result.GoalState)}, nil
}

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
