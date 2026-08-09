package runtimecontrol

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type GoalCommand interface {
	goalCommand()
}

type GoalExecutionIdentity struct {
	RunID  *string
	StepID *string
}

type GoalSetCommand struct {
	SessionID runtimeids.SessionID
	Objective string
	Actor     session.GoalActor
	Execution GoalExecutionIdentity
}

func (GoalSetCommand) goalCommand() {}

type GoalStatusCommand struct {
	SessionID runtimeids.SessionID
	Status    session.GoalStatus
	Actor     session.GoalActor
	Execution GoalExecutionIdentity
}

func (GoalStatusCommand) goalCommand() {}

type GoalClearCommand struct {
	SessionID runtimeids.SessionID
	Actor     session.GoalActor
}

func (GoalClearCommand) goalCommand() {}

type GoalCommandResult struct {
	Goal            *session.GoalState
	Cleared         bool
	Disposition     runtime.GoalCommandDisposition
	MetadataReceipt session.CommitReceipt
	NoticeReceipt   session.CommitReceipt
	Err             error
}

func (r GoalCommandResult) Accepted() bool {
	return r.Disposition == runtime.GoalCommandQueued ||
		r.Disposition == runtime.GoalCommandNoop ||
		r.MetadataReceipt.Committed ||
		r.NoticeReceipt.Committed
}

func (s *Service) setGoalCommand(ctx context.Context, command GoalSetCommand) (GoalCommandResult, error) {
	if err := validateGoalCommand(command.SessionID, command.Actor, command.Execution); err != nil {
		return GoalCommandResult{}, err
	}
	command.Objective = strings.TrimSpace(command.Objective)
	if command.Objective == "" {
		return GoalCommandResult{}, errors.New("goal objective is required")
	}
	if isStepScoped(command.Actor, command.Execution) {
		return s.withExactLiveGoal(ctx, command.SessionID, command.Execution, func(engine *runtime.Engine) (GoalCommandResult, error) {
			goal, queued, err := queueSet(engine, command)
			if err != nil {
				return GoalCommandResult{Err: err}, err
			}
			if queued {
				return queuedGoalResult(goal), nil
			}
			return GoalCommandResult{Err: runtime.ErrAgentGoalStepInactive}, runtime.ErrAgentGoalStepInactive
		})
	}
	return s.withDormantGoalAdmission(ctx, command.SessionID, func(store *session.Store) (GoalCommandResult, error) {
		return dormantSet(store, command)
	}, func(engine *runtime.Engine) (GoalCommandResult, error) {
		return liveSet(engine, command)
	})
}

func (s *Service) statusGoalCommand(ctx context.Context, command GoalStatusCommand) (GoalCommandResult, error) {
	if err := validateGoalCommand(command.SessionID, command.Actor, command.Execution); err != nil {
		return GoalCommandResult{}, err
	}
	if isStepScoped(command.Actor, command.Execution) {
		return s.withExactLiveGoal(ctx, command.SessionID, command.Execution, func(engine *runtime.Engine) (GoalCommandResult, error) {
			goal, queued, err := queueStatus(engine, command)
			if err != nil {
				return GoalCommandResult{Err: err}, err
			}
			if queued {
				return queuedGoalResult(goal), nil
			}
			return GoalCommandResult{Err: runtime.ErrAgentGoalStepInactive}, runtime.ErrAgentGoalStepInactive
		})
	}
	return s.withDormantGoalAdmission(ctx, command.SessionID, func(store *session.Store) (GoalCommandResult, error) {
		return dormantStatus(store, command)
	}, func(engine *runtime.Engine) (GoalCommandResult, error) {
		return liveStatus(engine, command)
	})
}

func (s *Service) clearGoalCommand(ctx context.Context, command GoalClearCommand) (GoalCommandResult, error) {
	if err := validateGoalCommand(command.SessionID, command.Actor, GoalExecutionIdentity{}); err != nil {
		return GoalCommandResult{}, err
	}
	return s.withDormantGoalAdmission(ctx, command.SessionID, func(store *session.Store) (GoalCommandResult, error) {
		return dormantClear(store, command)
	}, func(engine *runtime.Engine) (GoalCommandResult, error) {
		return liveClear(engine, command)
	})
}

func validateGoalCommand(sessionID runtimeids.SessionID, actor session.GoalActor, execution GoalExecutionIdentity) error {
	if sessionID.IsZero() {
		return errors.New("session id is required")
	}
	switch actor {
	case session.GoalActorUser, session.GoalActorAgent, session.GoalActorSystem:
	default:
		return errors.New("goal actor must be user, agent, or system")
	}
	if execution.RunID != nil && *execution.RunID == "" {
		return errors.New("goal run id cannot be empty")
	}
	if execution.StepID != nil && *execution.StepID == "" {
		return errors.New("goal step id cannot be empty")
	}
	return nil
}

func isStepScoped(actor session.GoalActor, execution GoalExecutionIdentity) bool {
	return actor == session.GoalActorAgent && execution.StepID != nil
}

func (s *Service) withDormantGoalAdmission(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	dormant func(*session.Store) (GoalCommandResult, error),
	live func(*runtime.Engine) (GoalCommandResult, error),
) (GoalCommandResult, error) {
	if s == nil || s.authority == nil {
		return GoalCommandResult{}, errors.New("session runtime authority is required")
	}
	if dormant == nil || live == nil {
		return GoalCommandResult{}, errors.New("goal command handlers are required")
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		return GoalCommandResult{}, err
	}
	var result GoalCommandResult
	admission, err := s.authority.WithDormantSessionStore(ctx, descriptor, func(_ context.Context, store *session.Store) error {
		applied, applyErr := dormant(store)
		result = applied
		if result.Accepted() {
			result.Err = applyErr
			return nil
		}
		return applyErr
	})
	if err != nil {
		return GoalCommandResult{}, err
	}
	if !admission.RuntimeAvailable {
		return result, nil
	}
	return s.withLiveGoal(ctx, sessionID, live)
}

func (s *Service) withLiveGoal(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	mutate func(*runtime.Engine) (GoalCommandResult, error),
) (GoalCommandResult, error) {
	if s == nil || s.authority == nil {
		return GoalCommandResult{}, errors.New("session runtime authority is required")
	}
	var result GoalCommandResult
	err := s.runAgentExecution(ctx, sessionID.String(), func(_ context.Context, engine *runtime.Engine) error {
		applied, applyErr := mutate(engine)
		result = applied
		return applyErr
	})
	if result.Accepted() {
		result.Err = err
		return result, nil
	}
	if !errors.Is(err, serverapi.ErrSessionRunStarting) {
		return GoalCommandResult{}, err
	}
	err = s.withLiveExecutionRuntime(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
		applied, applyErr := mutate(engine)
		result = applied
		return applyErr
	})
	if result.Accepted() {
		result.Err = err
		return result, nil
	}
	return GoalCommandResult{}, err
}

func (s *Service) withExactLiveGoal(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	execution GoalExecutionIdentity,
	mutate func(*runtime.Engine) (GoalCommandResult, error),
) (GoalCommandResult, error) {
	if s == nil || s.authority == nil {
		return GoalCommandResult{}, errors.New("session runtime authority is required")
	}
	var result GoalCommandResult
	err := s.withLiveExecutionRuntime(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
		if active := runtimeactivity.ActiveStepFromProvider(engine); active == nil ||
			active.StepID != *execution.StepID ||
			(execution.RunID != nil && active.RunID != *execution.RunID) {
			return runtime.ErrAgentGoalStepInactive
		}
		applied, applyErr := mutate(engine)
		result = applied
		return applyErr
	})
	if result.Accepted() {
		result.Err = err
		return result, nil
	}
	if errors.Is(err, serverapi.ErrRuntimeUnavailable) || errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		return GoalCommandResult{}, runtime.ErrAgentGoalStepInactive
	}
	return GoalCommandResult{}, err
}

func liveSet(engine *runtime.Engine, command GoalSetCommand) (GoalCommandResult, error) {
	if engine == nil {
		return GoalCommandResult{}, errors.New("runtime engine is required")
	}
	if engine.CurrentNodeExecutionConfigured() {
		result, err := engine.SetGoal(command.Objective, command.Actor)
		return fromRuntimeResult(result, err), err
	}
	goal, queued, err := queueSet(engine, command)
	if err != nil {
		return GoalCommandResult{Err: err}, err
	}
	if queued {
		return queuedGoalResult(goal), nil
	}
	if command.Actor == session.GoalActorAgent {
		if current := engine.Goal(); current != nil && current.Status != session.GoalStatusComplete {
			err := session.GoalAgentOverwriteBlockedError{Goal: *current}
			return GoalCommandResult{Err: err}, err
		}
	}
	if err := engine.RequireGoalLoopStartAllowed(); err != nil {
		return GoalCommandResult{Err: err}, err
	}
	result, err := engine.SetGoal(command.Objective, command.Actor)
	out := fromRuntimeResult(result, err)
	if !out.Accepted() || err != nil {
		return out, err
	}
	if startErr := engine.StartGoalLoop(); startErr != nil {
		out.Err = startErr
		return out, startErr
	}
	return out, nil
}

func liveStatus(engine *runtime.Engine, command GoalStatusCommand) (GoalCommandResult, error) {
	if engine == nil {
		return GoalCommandResult{}, errors.New("runtime engine is required")
	}
	if current := engine.Goal(); current != nil && current.Status == command.Status {
		if command.Status != session.GoalStatusActive || engine.CurrentNodeExecutionConfigured() || engine.GoalLoopContinuationEnforced() {
			return noopGoalResult(*current), nil
		}
	}
	if engine.CurrentNodeExecutionConfigured() {
		result, err := engine.SetGoalStatusWithoutGoalLoopStart(command.Status, command.Actor)
		return fromRuntimeResult(result, err), err
	}
	goal, queued, err := queueStatus(engine, command)
	if err != nil {
		return GoalCommandResult{Err: err}, err
	}
	if queued {
		return queuedGoalResult(goal), nil
	}
	if command.Status == session.GoalStatusActive {
		if current := engine.Goal(); current != nil && current.Status == session.GoalStatusActive && engine.GoalLoopContinuationEnforced() {
			return noopGoalResult(*current), nil
		}
		if err := engine.RequireGoalLoopStartAllowed(); err != nil {
			return GoalCommandResult{Err: err}, err
		}
	}
	result, err := engine.SetGoalStatus(command.Status, command.Actor)
	out := fromRuntimeResult(result, err)
	if !out.Accepted() || err != nil {
		return out, err
	}
	if command.Status == session.GoalStatusActive {
		if startErr := engine.StartGoalLoop(); startErr != nil {
			out.Err = startErr
			return out, startErr
		}
	}
	return out, nil
}

func liveClear(engine *runtime.Engine, command GoalClearCommand) (GoalCommandResult, error) {
	if engine == nil {
		return GoalCommandResult{}, errors.New("runtime engine is required")
	}
	goal, queued, err := engine.QueueGoalClearForActiveStep(command.Actor)
	if err != nil {
		return GoalCommandResult{Err: err}, err
	}
	if queued {
		return queuedClearResult(goal), nil
	}
	result, err := engine.ClearGoal(command.Actor)
	return fromRuntimeResult(result, err), err
}

func queueSet(engine *runtime.Engine, command GoalSetCommand) (session.GoalState, bool, error) {
	if isStepScoped(command.Actor, command.Execution) {
		return engine.QueueAgentShellSetGoalForStep(*command.Execution.StepID, command.Objective, command.Actor)
	}
	return engine.QueueGoalSetForActiveStep(command.Objective, command.Actor)
}

func queueStatus(engine *runtime.Engine, command GoalStatusCommand) (session.GoalState, bool, error) {
	if isStepScoped(command.Actor, command.Execution) {
		return engine.QueueGoalStatusForStep(*command.Execution.StepID, command.Status, command.Actor)
	}
	return engine.QueueGoalStatusForActiveStep(command.Status, command.Actor)
}

func dormantSet(store *session.Store, command GoalSetCommand) (GoalCommandResult, error) {
	goal, metadataReceipt, err := store.SetGoal(command.Objective, command.Actor)
	result := storedGoalResult(goal, false, runtime.GoalCommandApplied, metadataReceipt, session.CommitReceipt{}, err)
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	return appendDormantGoalNotice(store, runtime.GoalNoticeSet, &goal, result)
}

func dormantStatus(store *session.Store, command GoalStatusCommand) (GoalCommandResult, error) {
	if current := store.Meta().Goal; current != nil && current.Status == command.Status {
		return noopGoalResult(*current), nil
	}
	goal, transitioned, metadataReceipt, err := store.SetGoalStatus(command.Status, command.Actor)
	disposition := runtime.GoalCommandApplied
	if err == nil && !transitioned {
		disposition = runtime.GoalCommandNoop
	}
	result := storedGoalResult(goal, false, disposition, metadataReceipt, session.CommitReceipt{}, err)
	if err != nil || !transitioned || !metadataReceipt.Committed {
		return result, err
	}
	return appendDormantGoalNotice(store, runtime.GoalNoticeStatus, &goal, result)
}

func dormantClear(store *session.Store, command GoalClearCommand) (GoalCommandResult, error) {
	goal, metadataReceipt, err := store.ClearGoal(command.Actor)
	result := storedGoalResult(goal, true, runtime.GoalCommandApplied, metadataReceipt, session.CommitReceipt{}, err)
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	return appendDormantGoalNotice(store, runtime.GoalNoticeClear, nil, result)
}

func appendDormantGoalNotice(
	store *session.Store,
	kind runtime.GoalNoticeKind,
	goal *session.GoalState,
	result GoalCommandResult,
) (GoalCommandResult, error) {
	if store == nil {
		return GoalCommandResult{}, errors.New("session store is required")
	}
	receipt, err := runtime.SteerPersistedGoalNotice(store, kind, goal)
	result.NoticeReceipt = receipt
	result.Err = err
	return result, err
}

func fromRuntimeResult(result runtime.GoalCommandResult, err error) GoalCommandResult {
	goal := result.GoalState
	return storedGoalResult(
		goal,
		result.Cleared,
		result.Disposition,
		result.MetadataReceipt,
		result.NoticeReceipt,
		err,
	)
}

func queuedGoalResult(goal session.GoalState) GoalCommandResult {
	return storedGoalResult(goal, false, runtime.GoalCommandQueued, session.CommitReceipt{}, session.CommitReceipt{}, nil)
}

func queuedClearResult(goal session.GoalState) GoalCommandResult {
	return storedGoalResult(goal, true, runtime.GoalCommandQueued, session.CommitReceipt{}, session.CommitReceipt{}, nil)
}

func noopGoalResult(goal session.GoalState) GoalCommandResult {
	return storedGoalResult(goal, false, runtime.GoalCommandNoop, session.CommitReceipt{}, session.CommitReceipt{}, nil)
}

func storedGoalResult(
	goal session.GoalState,
	cleared bool,
	disposition runtime.GoalCommandDisposition,
	metadataReceipt session.CommitReceipt,
	noticeReceipt session.CommitReceipt,
	err error,
) GoalCommandResult {
	var projected *session.GoalState
	if !cleared {
		copied := goal
		projected = &copied
	}
	return GoalCommandResult{
		Goal:            projected,
		Cleared:         cleared,
		Disposition:     disposition,
		MetadataReceipt: metadataReceipt,
		NoticeReceipt:   noticeReceipt,
		Err:             err,
	}
}
