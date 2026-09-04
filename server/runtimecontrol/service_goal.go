package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/prompts"
	"core/server/runtime"
	"core/server/runtimeview"
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
	record, err := session.ResolvePersistedSessionRecord(ctx, s.persisted, sessionID)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, fmt.Errorf("resolve persisted session %q: %w", sessionID, err)
	}
	if record.Meta == nil {
		return serverapi.RuntimeGoalShowResponse{}, fmt.Errorf("persisted session %q metadata is required", sessionID)
	}
	availability, err := session.GoalAvailabilityFromMeta(*record.Meta)
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	return serverapi.RuntimeGoalShowResponse{
		GoalEnvelope: clientui.GoalEnvelope{
			Goal:         runtimeview.GoalCoreFromSessionState(record.Meta.Goal),
			Availability: runtimeview.GoalAvailabilityFromSession(availability),
		},
	}, nil
}

func (s *Service) SetGoal(ctx context.Context, req serverapi.RuntimeGoalSetRequest) (serverapi.RuntimeGoalShowResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	sessionID, err := runtimeids.ParseSessionID(strings.TrimSpace(req.SessionID))
	if err != nil {
		return serverapi.RuntimeGoalShowResponse{}, err
	}
	mutation := goalMutation{
		kind:      goalMutationSet,
		Objective: strings.TrimSpace(req.Objective),
		Actor:     session.GoalActor(strings.TrimSpace(req.Actor)),
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
	return s.mutateGoal(ctx, sessionID, req.RunID, req.StepID, goalMutation{
		kind:   goalMutationStatus,
		Status: status,
		Actor:  session.GoalActor(strings.TrimSpace(req.Actor)),
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
	return s.mutateGoal(ctx, sessionID, "", "", goalMutation{
		kind:  goalMutationClear,
		Actor: session.GoalActor(strings.TrimSpace(req.Actor)),
	})
}

type goalMutationKind uint8

const (
	goalMutationSet goalMutationKind = iota + 1
	goalMutationStatus
	goalMutationClear
)

type goalMutation struct {
	kind      goalMutationKind
	Objective string
	Status    session.GoalStatus
	Actor     session.GoalActor
}

func (s *Service) mutateGoal(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	rawRunID string,
	rawStepID string,
	mutation goalMutation,
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
		result, err := s.applyExactAgentGoalMutation(ctx, sessionID, runID, stepID, mutation)
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
	mutation goalMutation,
) (runtime.GoalCommandResult, error) {
	var result runtime.GoalCommandResult
	apply := func(runtimeCtx context.Context, engine *runtime.Engine) error {
		var err error
		result, err = applyLiveGoalMutation(runtimeCtx, engine, mutation)
		return err
	}
	err := s.authority.WithCurrentRuntime(ctx, sessionID, apply)
	if goalResultAccepted(result) {
		return result, err
	}
	return runtime.GoalCommandResult{}, err
}

func (s *Service) applyExactAgentGoalMutation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	runID runtimeids.RunID,
	stepID runtimeids.StepID,
	mutation goalMutation,
) (runtime.GoalCommandResult, error) {
	var result runtime.GoalCommandResult
	err := s.authority.WithCurrentRuntime(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
		availability, err := engine.GoalAvailability()
		if err != nil {
			return err
		}
		active := engine.ActiveRun()
		if active == nil || active.RunID != runID.String() || active.StepID != stepID.String() {
			return runtime.ErrAgentGoalStepInactive
		}
		var (
			goal         session.GoalState
			queued       bool
			operationErr error
		)
		switch mutation.kind {
		case goalMutationSet:
			goal, queued, operationErr = engine.QueueAgentShellSetGoalForStep(stepID.String(), mutation.Objective, mutation.Actor)
		case goalMutationStatus:
			goal, queued, operationErr = engine.QueueGoalStatusForStep(stepID.String(), mutation.Status, mutation.Actor)
		default:
			return errors.New("agent Goal mutation kind is invalid")
		}
		if operationErr != nil {
			return operationErr
		}
		if !queued {
			return runtime.ErrAgentGoalStepInactive
		}
		result = runtimeGoalResult(goal, false, runtime.GoalCommandQueued, session.CommitReceipt{}, session.CommitReceipt{})
		result.Availability = &availability
		return nil
	})
	return result, err
}

func applyLiveGoalMutation(ctx context.Context, engine *runtime.Engine, mutation goalMutation) (runtime.GoalCommandResult, error) {
	switch mutation.kind {
	case goalMutationSet:
		if err := engine.ValidateGoalSet(mutation.Objective, mutation.Actor); err != nil {
			return runtime.GoalCommandResult{}, err
		}
		if err := engine.RequireGoalLoopStartAllowed(); err != nil {
			return runtime.GoalCommandResult{}, err
		}
		return engine.SetGoalAndStartLoop(ctx, mutation.Objective, mutation.Actor)
	case goalMutationStatus:
		if mutation.Status == session.GoalStatusActive {
			return engine.SetGoalStatusAndStartLoop(ctx, mutation.Status, mutation.Actor)
		}
		return engine.SetGoalStatus(ctx, mutation.Status, mutation.Actor)
	case goalMutationClear:
		return engine.ClearGoal(ctx, mutation.Actor)
	default:
		return runtime.GoalCommandResult{}, fmt.Errorf("unsupported Goal mutation kind %d", mutation.kind)
	}
}

func applyDormantGoalMutation(store *session.Store, mutation goalMutation) (runtime.GoalCommandResult, error) {
	if store == nil {
		return runtime.GoalCommandResult{}, errors.New("session store is required")
	}
	availability, err := store.GoalAvailability()
	if err != nil {
		return runtime.GoalCommandResult{}, err
	}
	switch mutation.kind {
	case goalMutationSet:
		goal, metadataReceipt, err := store.SetGoal(mutation.Objective, mutation.Actor)
		result := runtimeGoalResult(goal, false, runtime.GoalCommandApplied, metadataReceipt, session.CommitReceipt{})
		result.Availability = &availability
		if err != nil || !metadataReceipt.Committed {
			return result, err
		}
		noticeReceipt, noticeErr := runtime.SteerPersistedGoalNotice(store, runtime.GoalNoticeSet, &goal)
		result.NoticeReceipt = noticeReceipt
		return result, noticeErr
	case goalMutationStatus:
		if current := store.Meta().Goal; current != nil && current.Status == mutation.Status {
			result := runtimeGoalResult(*current, false, runtime.GoalCommandNoop, session.CommitReceipt{}, session.CommitReceipt{})
			result.Availability = &availability
			return result, nil
		}
		goal, transitioned, metadataReceipt, err := store.SetGoalStatus(mutation.Status, mutation.Actor)
		disposition := runtime.GoalCommandApplied
		if err == nil && !transitioned {
			disposition = runtime.GoalCommandNoop
		}
		result := runtimeGoalResult(goal, false, disposition, metadataReceipt, session.CommitReceipt{})
		result.Availability = &availability
		if err != nil || !transitioned || !metadataReceipt.Committed {
			return result, err
		}
		noticeReceipt, noticeErr := runtime.SteerPersistedGoalNotice(store, runtime.GoalNoticeStatus, &goal)
		result.NoticeReceipt = noticeReceipt
		return result, noticeErr
	case goalMutationClear:
		goal, metadataReceipt, err := store.ClearGoal(mutation.Actor)
		result := runtimeGoalResult(goal, true, runtime.GoalCommandApplied, metadataReceipt, session.CommitReceipt{})
		result.Availability = &availability
		if err != nil || !metadataReceipt.Committed {
			return result, err
		}
		noticeReceipt, noticeErr := runtime.SteerPersistedGoalNotice(store, runtime.GoalNoticeClear, nil)
		result.NoticeReceipt = noticeReceipt
		return result, noticeErr
	default:
		return runtime.GoalCommandResult{}, fmt.Errorf("unsupported Goal mutation kind %d", mutation.kind)
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
	if result.Availability == nil {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("accepted Goal mutation is missing availability")
	}
	availability := runtimeview.GoalAvailabilityFromSession(*result.Availability)
	if result.Cleared {
		return serverapi.RuntimeGoalShowResponse{
			GoalEnvelope: clientui.GoalEnvelope{Availability: availability},
		}, nil
	}
	if result.Disposition == 0 {
		return serverapi.RuntimeGoalShowResponse{}, errors.New("accepted Goal mutation is missing a result")
	}
	return serverapi.RuntimeGoalShowResponse{
		GoalEnvelope: clientui.GoalEnvelope{
			Goal:         runtimeview.GoalCoreFromSessionState(&result.GoalState),
			Availability: availability,
		},
	}, nil
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
