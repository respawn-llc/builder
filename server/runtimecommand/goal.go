package runtimecommand

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/server/sessionruntime"
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
	Availability    *session.GoalAvailability
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

type GoalAuthority struct {
	authority *sessionruntime.Authority
	execution *ExecutionAdapter
}

func NewGoalAuthority(authority *sessionruntime.Authority, execution *ExecutionAdapter) *GoalAuthority {
	if execution == nil {
		execution = NewExecutionAdapter(authority)
	}
	return &GoalAuthority{authority: authority, execution: execution}
}

func (a *GoalAuthority) Set(ctx context.Context, command GoalSetCommand) (GoalCommandResult, error) {
	if err := validateGoalCommand(command.SessionID, command.Actor, command.Execution); err != nil {
		return GoalCommandResult{}, err
	}
	command.Objective = strings.TrimSpace(command.Objective)
	if command.Objective == "" {
		return GoalCommandResult{}, errors.New("goal objective is required")
	}
	if isStepScoped(command.Actor, command.Execution) {
		return a.withExactLive(ctx, command.SessionID, command.Execution, func(engine *runtime.Engine) (GoalCommandResult, error) {
			availability := engine.GoalMutationAvailability()
			goal, queued, err := queueSet(engine, command)
			if err != nil {
				return GoalCommandResult{Err: err}, err
			}
			if queued {
				return queuedGoalResult(goal, availability), nil
			}
			return GoalCommandResult{Err: runtime.ErrAgentGoalStepInactive}, runtime.ErrAgentGoalStepInactive
		})
	}
	return a.withDormantAdmission(ctx, command.SessionID, func(store *session.Store) (GoalCommandResult, error) {
		return dormantSet(store, command)
	}, runtime.CurrentGoalSet{Objective: command.Objective, Actor: command.Actor})
}

func (a *GoalAuthority) Status(ctx context.Context, command GoalStatusCommand) (GoalCommandResult, error) {
	if err := validateGoalCommand(command.SessionID, command.Actor, command.Execution); err != nil {
		return GoalCommandResult{}, err
	}
	if isStepScoped(command.Actor, command.Execution) {
		return a.withExactLive(ctx, command.SessionID, command.Execution, func(engine *runtime.Engine) (GoalCommandResult, error) {
			availability := engine.GoalMutationAvailability()
			goal, queued, err := queueStatus(engine, command)
			if err != nil {
				return GoalCommandResult{Err: err}, err
			}
			if queued {
				return queuedGoalResult(goal, availability), nil
			}
			return GoalCommandResult{Err: runtime.ErrAgentGoalStepInactive}, runtime.ErrAgentGoalStepInactive
		})
	}
	return a.withDormantAdmission(ctx, command.SessionID, func(store *session.Store) (GoalCommandResult, error) {
		return dormantStatus(store, command)
	}, runtime.CurrentGoalStatus{Status: command.Status, Actor: command.Actor})
}

func (a *GoalAuthority) Clear(ctx context.Context, command GoalClearCommand) (GoalCommandResult, error) {
	if err := validateGoalCommand(command.SessionID, command.Actor, GoalExecutionIdentity{}); err != nil {
		return GoalCommandResult{}, err
	}
	return a.withDormantAdmission(ctx, command.SessionID, func(store *session.Store) (GoalCommandResult, error) {
		return dormantClear(store, command)
	}, runtime.CurrentGoalClear{Actor: command.Actor})
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

func (a *GoalAuthority) withDormantAdmission(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	dormant func(*session.Store) (GoalCommandResult, error),
	operation runtime.CurrentGoalOperation,
) (GoalCommandResult, error) {
	if a == nil || a.authority == nil {
		return GoalCommandResult{}, errors.New("session runtime authority is required")
	}
	if dormant == nil {
		return GoalCommandResult{}, errors.New("goal command handler is required")
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		return GoalCommandResult{}, err
	}
	var result GoalCommandResult
	admission, err := a.authority.WithDormantSessionStore(ctx, descriptor, func(_ context.Context, store *session.Store) error {
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
	return a.withLive(ctx, sessionID, operation)
}

func (a *GoalAuthority) withLive(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	operation runtime.CurrentGoalOperation,
) (GoalCommandResult, error) {
	if a == nil || a.execution == nil {
		return GoalCommandResult{}, errors.New("runtime execution adapter is required")
	}
	return applyGoalOperationWithAdmission(
		func() (runtime.CurrentGoalOperationOutcome, error) {
			return a.authority.ApplyCurrentGoalOperation(ctx, sessionID, operation)
		},
		func() (GoalCommandResult, AgentExecutionAdmission) {
			var result GoalCommandResult
			admission := a.execution.RunAgentExecutionAdmission(ctx, sessionID.String(), func(_ context.Context, engine *runtime.Engine) error {
				applied, err := executeGoalOperation(engine, operation)
				result = applied
				return err
			})
			return result, admission
		},
	)
}

func applyGoalOperationWithAdmission(
	apply func() (runtime.CurrentGoalOperationOutcome, error),
	admit func() (GoalCommandResult, AgentExecutionAdmission),
) (GoalCommandResult, error) {
	outcome, err := apply()
	if err != nil {
		return GoalCommandResult{}, err
	}
	if outcome.Handled != nil {
		return fromRuntimeResult(*outcome.Handled, nil), nil
	}
	result, admission := admit()
	err = admission.Err
	if result.Accepted() {
		result.Err = err
		return result, nil
	}
	if admission.CallbackEntered || !errors.Is(err, serverapi.ErrSessionRunStarting) {
		return GoalCommandResult{}, err
	}
	outcome, err = apply()
	if err != nil {
		return GoalCommandResult{}, err
	}
	if outcome.Handled != nil {
		return fromRuntimeResult(*outcome.Handled, nil), nil
	}
	return GoalCommandResult{}, errors.Join(serverapi.ErrSessionRunStarting, sessionruntime.ErrSessionRunActive)
}

func executeGoalOperation(engine *runtime.Engine, operation runtime.CurrentGoalOperation) (GoalCommandResult, error) {
	switch operation := operation.(type) {
	case runtime.CurrentGoalSet:
		if err := engine.RequireGoalLoopStartAllowed(); err != nil {
			return GoalCommandResult{}, err
		}
		result, err := engine.SetGoal(operation.Objective, operation.Actor)
		out := fromRuntimeResult(result, err)
		if !out.Accepted() || err != nil {
			return out, err
		}
		if startErr := engine.StartGoalLoop(); startErr != nil {
			out.Err = startErr
			return out, startErr
		}
		return out, nil
	case runtime.CurrentGoalStatus:
		if operation.Status == session.GoalStatusActive {
			if err := engine.RequireGoalLoopStartAllowed(); err != nil {
				return GoalCommandResult{}, err
			}
		}
		result, err := engine.SetGoalStatus(operation.Status, operation.Actor)
		out := fromRuntimeResult(result, err)
		if !out.Accepted() || err != nil {
			return out, err
		}
		if operation.Status == session.GoalStatusActive && out.Disposition != runtime.GoalCommandNoop {
			if startErr := engine.StartGoalLoop(); startErr != nil {
				out.Err = startErr
				return out, startErr
			}
		}
		return out, nil
	default:
		return GoalCommandResult{}, errors.New("ordinary execution requires Set or Resume")
	}
}

func (a *GoalAuthority) withExactLive(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	execution GoalExecutionIdentity,
	mutate func(*runtime.Engine) (GoalCommandResult, error),
) (GoalCommandResult, error) {
	if a == nil || a.execution == nil {
		return GoalCommandResult{}, errors.New("runtime execution adapter is required")
	}
	var result GoalCommandResult
	err := a.execution.WithLiveExecutionRuntime(ctx, sessionID, func(_ context.Context, engine *runtime.Engine) error {
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
	availability := store.GoalMutationAvailability()
	goal, metadataReceipt, err := store.SetGoal(command.Objective, command.Actor)
	result := storedGoalResult(goal, false, runtime.GoalCommandApplied, metadataReceipt, session.CommitReceipt{}, err)
	result.Availability = availability
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	result, err = appendDormantGoalNotice(store, runtime.GoalNoticeSet, &goal, result)
	if err != nil {
		return result, err
	}
	return result, nil
}

func dormantStatus(store *session.Store, command GoalStatusCommand) (GoalCommandResult, error) {
	availability := store.GoalMutationAvailability()
	if current := store.Meta().Goal; current != nil && current.Status == command.Status {
		return noopGoalResult(*current, availability), nil
	}
	goal, transitioned, metadataReceipt, err := store.SetGoalStatus(command.Status, command.Actor)
	disposition := runtime.GoalCommandApplied
	if err == nil && !transitioned {
		disposition = runtime.GoalCommandNoop
	}
	result := storedGoalResult(goal, false, disposition, metadataReceipt, session.CommitReceipt{}, err)
	result.Availability = availability
	if err != nil || !transitioned || !metadataReceipt.Committed {
		return result, err
	}
	result, err = appendDormantGoalNotice(store, runtime.GoalNoticeStatus, &goal, result)
	if err != nil {
		return result, err
	}
	return result, nil
}

func dormantClear(store *session.Store, command GoalClearCommand) (GoalCommandResult, error) {
	availability := store.GoalMutationAvailability()
	goal, metadataReceipt, err := store.ClearGoal(command.Actor)
	result := storedGoalResult(goal, true, runtime.GoalCommandApplied, metadataReceipt, session.CommitReceipt{}, err)
	result.Availability = availability
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	result, err = appendDormantGoalNotice(store, runtime.GoalNoticeClear, nil, result)
	if err != nil {
		return result, err
	}
	return result, nil
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
	out := storedGoalResult(
		goal,
		result.Cleared,
		result.Disposition,
		result.MetadataReceipt,
		result.NoticeReceipt,
		err,
	)
	out.Availability = result.Availability
	return out
}

func queuedGoalResult(goal session.GoalState, availability *session.GoalAvailability) GoalCommandResult {
	result := storedGoalResult(goal, false, runtime.GoalCommandQueued, session.CommitReceipt{}, session.CommitReceipt{}, nil)
	result.Availability = availability
	return result
}

func queuedClearResult(goal session.GoalState, availability *session.GoalAvailability) GoalCommandResult {
	result := storedGoalResult(goal, true, runtime.GoalCommandQueued, session.CommitReceipt{}, session.CommitReceipt{}, nil)
	result.Availability = availability
	return result
}

func noopGoalResult(goal session.GoalState, availability *session.GoalAvailability) GoalCommandResult {
	result := storedGoalResult(goal, false, runtime.GoalCommandNoop, session.CommitReceipt{}, session.CommitReceipt{}, nil)
	result.Availability = availability
	return result
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
