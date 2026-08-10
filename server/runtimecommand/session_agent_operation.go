package runtimecommand

import (
	"context"
	"errors"
	"strings"

	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

// SessionAgentOperationDriver is the closed set of explicit Session operations
// that may start Agent execution. Workflow routing consumes this same contract
// later; ordinary Sessions execute it directly through ExecutionAdapter.
type SessionAgentOperationDriver interface {
	sessionAgentOperationDriver()
	RuntimeOperationRef() (clientui.RuntimeOperationRef, bool)
	StartOwner(context.Context, *runtime.Engine, SessionAgentOperationOwnerOrderingNotifier) (SessionAgentOperationOutcome, error)
	JoinLive(context.Context, *runtime.Engine) (SessionAgentOperationOutcome, error)
}

type SessionAgentOperationOutcome interface {
	sessionAgentOperationOutcome()
}

type UserTurnOperationOutcome struct {
	Response serverapi.RuntimeSubmitUserTurnResponse
}

func (UserTurnOperationOutcome) sessionAgentOperationOutcome() {}

type UserShellOperationOutcome struct {
	Result tools.Result
}

func (UserShellOperationOutcome) sessionAgentOperationOutcome() {}

type ManualCompactionOperationOutcome struct {
	Receipt session.CommitReceipt
}

func (ManualCompactionOperationOutcome) sessionAgentOperationOutcome() {}

type GoalMutationOperationOutcome struct {
	Result GoalCommandResult
}

func (GoalMutationOperationOutcome) sessionAgentOperationOutcome() {}

type UserTurnPromptHistoryRecorder func(
	context.Context,
	*runtime.Engine,
	runtimeids.SessionID,
	string,
	string,
)

type UserTurnDriverOptions struct {
	SessionID                       runtimeids.SessionID
	ExecutionText                   string
	HistoryText                     string
	ClientRequestID                 runtimeids.RuntimeClientRequestID
	OperationRef                    clientui.RuntimeOperationRef
	PreSubmitCompactionOperationRef clientui.RuntimeOperationRef
	Operations                      *runtimeops.Coordinator
	AttemptContext                  context.Context
	RecordPromptHistory             UserTurnPromptHistoryRecorder
}

type UserTurnDriver struct {
	options UserTurnDriverOptions
}

func NewUserTurnDriver(options UserTurnDriverOptions) (SessionAgentOperationDriver, error) {
	if options.SessionID.IsZero() {
		return nil, errors.New("session id is required")
	}
	if strings.TrimSpace(options.ExecutionText) == "" {
		return nil, errors.New("user turn text is required")
	}
	if options.Operations == nil {
		return nil, errors.New("runtime operation coordinator is required")
	}
	if err := options.OperationRef.Validate(); err != nil {
		return nil, err
	}
	if options.OperationRef.Kind != clientui.RuntimeOperationKindSubmit {
		return nil, errors.New("user turn driver requires a submit operation")
	}
	if err := options.PreSubmitCompactionOperationRef.Validate(); err != nil {
		return nil, err
	}
	if options.PreSubmitCompactionOperationRef.Kind != clientui.RuntimeOperationKindPreSubmitCompact {
		return nil, errors.New("user turn driver requires a pre-submit compaction operation")
	}
	if options.AttemptContext == nil {
		options.AttemptContext = context.Background()
	}
	return UserTurnDriver{options: options}, nil
}

func (UserTurnDriver) sessionAgentOperationDriver() {}

func (d UserTurnDriver) RuntimeOperationRef() (clientui.RuntimeOperationRef, bool) {
	return d.options.OperationRef, true
}

func (d UserTurnDriver) StartOwner(
	ctx context.Context,
	engine *runtime.Engine,
	ordering SessionAgentOperationOwnerOrderingNotifier,
) (outcome SessionAgentOperationOutcome, err error) {
	if engine == nil {
		return UserTurnOperationOutcome{}, errors.New("runtime engine is required")
	}
	options := d.options
	var response serverapi.RuntimeSubmitUserTurnResponse
	accepted := false
	defer func() {
		if err != nil && !accepted {
			recordRuntimeOperationFailure(options.Operations, options.SessionID.String(), options.OperationRef, err, options.AttemptContext)
		}
	}()
	recordAccepted := func(queued bool) {
		if accepted {
			return
		}
		accepted = true
		if queued {
			options.Operations.RecordQueuedMessageSubmitted(options.SessionID.String(), options.OperationRef)
		} else {
			options.Operations.RecordUserMessageFlushed(options.SessionID.String(), options.OperationRef)
		}
		ordering.Complete()
	}
	defer func() {
		if accepted && options.RecordPromptHistory != nil {
			options.RecordPromptHistory(
				context.Background(),
				engine,
				options.SessionID,
				options.ClientRequestID.String(),
				options.HistoryText,
			)
		}
	}()

	shouldCompact, err := engine.ShouldCompactBeforeUserMessage(ctx, options.ExecutionText)
	if err != nil {
		return UserTurnOperationOutcome{}, err
	}
	compacted := false
	compactionBusy := false
	if shouldCompact {
		compactErr := d.runPreSubmitCompaction(ctx, engine)
		if compactErr != nil {
			if !errors.Is(compactErr, runtime.ErrAgentBusy) {
				return UserTurnOperationOutcome{}, compactErr
			}
			compactionBusy = true
		} else {
			compacted = true
		}
	}
	if compactionBusy {
		queued, queueErr := engine.QueueUserMessageForAutoDrain(options.ExecutionText, options.ClientRequestID.String())
		if queueErr != nil {
			return UserTurnOperationOutcome{}, queueErr
		}
		recordAccepted(true)
		response = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, Steered: true, QueueItemID: queued.ID}
		return UserTurnOperationOutcome{Response: response}, nil
	}

	message, queued, err := engine.SubmitUserMessageOrSteerWithHooks(
		ctx,
		options.ExecutionText,
		options.ClientRequestID.String(),
		func() {
			options.Operations.MarkOperationActive(options.SessionID.String(), options.OperationRef)
		},
		recordAccepted,
	)
	if err != nil {
		return UserTurnOperationOutcome{}, err
	}
	if queued != nil {
		response = serverapi.RuntimeSubmitUserTurnResponse{Compacted: compacted, Steered: true, QueueItemID: queued.ID}
		return UserTurnOperationOutcome{Response: response}, nil
	}
	response.Compacted = compacted
	if message.Content != nil {
		response.Message = *message.Content
	}
	return UserTurnOperationOutcome{Response: response}, nil
}

func (d UserTurnDriver) JoinLive(ctx context.Context, engine *runtime.Engine) (outcome SessionAgentOperationOutcome, err error) {
	if engine == nil {
		return UserTurnOperationOutcome{}, errors.New("runtime engine is required")
	}
	options := d.options
	defer func() {
		if err != nil {
			recordRuntimeOperationFailure(options.Operations, options.SessionID.String(), options.OperationRef, err, options.AttemptContext)
		}
	}()
	var response serverapi.RuntimeSubmitUserTurnResponse
	committed, err := options.Operations.TryCommitOperationMutation(options.SessionID.String(), options.OperationRef, func() error {
		item, accepted, queueErr := engine.QueueUserMessageForActiveRun(
			ctx,
			options.ExecutionText,
			options.ClientRequestID,
			nil,
		)
		if errors.Is(queueErr, runtime.ErrNoActiveLiveRun) {
			if !activeExecutionAllowsUserTurnAutoDrain(runtimeactivity.ActiveStepFromProvider(engine)) {
				return serverapi.ErrSessionRunStarting
			}
			item, queueErr = engine.QueueUserMessageForAutoDrain(options.ExecutionText, options.ClientRequestID.String())
			if queueErr != nil {
				return queueErr
			}
			accepted = true
		} else if queueErr != nil {
			return queueErr
		}
		if !accepted {
			return serverapi.ErrSessionRunStarting
		}
		response = serverapi.RuntimeSubmitUserTurnResponse{Steered: true, QueueItemID: item.ID}
		return nil
	})
	if err != nil {
		return UserTurnOperationOutcome{}, err
	}
	if !committed {
		return UserTurnOperationOutcome{}, runtimeops.ErrOperationCanceled
	}
	options.Operations.RecordQueuedMessageSubmitted(options.SessionID.String(), options.OperationRef)
	if options.RecordPromptHistory != nil {
		options.RecordPromptHistory(
			context.Background(),
			engine,
			options.SessionID,
			options.ClientRequestID.String(),
			options.HistoryText,
		)
	}
	return UserTurnOperationOutcome{Response: response}, nil
}

func (d UserTurnDriver) runPreSubmitCompaction(ctx context.Context, engine *runtime.Engine) error {
	options := d.options
	_, err := runtimeops.Do(
		options.Operations,
		ctx,
		options.SessionID.String(),
		options.PreSubmitCompactionOperationRef,
		options.SessionID,
		func(left, right runtimeids.SessionID) bool { return left == right },
		func(_ context.Context, attempt runtimeops.Attempt) (struct{}, error) {
			runCtx, stop := sessionruntime.MergeContexts(ctx, attempt.Context())
			defer stop()
			receipt, compactErr := engine.CompactContextForPreSubmitWithActiveHook(runCtx, func() {
				options.Operations.MarkOperationActive(options.SessionID.String(), options.PreSubmitCompactionOperationRef)
			})
			recordCompactionCompletion(
				options.Operations,
				options.SessionID.String(),
				options.PreSubmitCompactionOperationRef,
				receipt,
				compactErr,
				attempt.Context(),
			)
			return struct{}{}, compactErr
		},
	)
	return err
}

type UserShellDriverOptions struct {
	SessionID      runtimeids.SessionID
	Command        string
	OperationRef   clientui.RuntimeOperationRef
	Operations     *runtimeops.Coordinator
	AttemptContext context.Context
}

type UserShellDriver struct {
	options UserShellDriverOptions
}

func NewUserShellDriver(options UserShellDriverOptions) (SessionAgentOperationDriver, error) {
	if options.SessionID.IsZero() {
		return nil, errors.New("session id is required")
	}
	if strings.TrimSpace(options.Command) == "" {
		return nil, errors.New("shell command is required")
	}
	if options.Operations == nil {
		return nil, errors.New("runtime operation coordinator is required")
	}
	if err := options.OperationRef.Validate(); err != nil {
		return nil, err
	}
	if options.OperationRef.Kind != clientui.RuntimeOperationKindUserShell {
		return nil, errors.New("user shell driver requires a user-shell operation")
	}
	if options.AttemptContext == nil {
		options.AttemptContext = context.Background()
	}
	return UserShellDriver{options: options}, nil
}

func (UserShellDriver) sessionAgentOperationDriver() {}

func (d UserShellDriver) RuntimeOperationRef() (clientui.RuntimeOperationRef, bool) {
	return d.options.OperationRef, true
}

func (d UserShellDriver) StartOwner(ctx context.Context, engine *runtime.Engine, ordering SessionAgentOperationOwnerOrderingNotifier) (SessionAgentOperationOutcome, error) {
	return d.run(ctx, engine, ordering.Complete)
}

func (d UserShellDriver) JoinLive(ctx context.Context, engine *runtime.Engine) (SessionAgentOperationOutcome, error) {
	return d.run(ctx, engine, nil)
}

func (d UserShellDriver) run(ctx context.Context, engine *runtime.Engine, ordered func() bool) (SessionAgentOperationOutcome, error) {
	if engine == nil {
		return UserShellOperationOutcome{}, errors.New("runtime engine is required")
	}
	options := d.options
	result, err := engine.SubmitUserShellCommandWithActiveHook(ctx, options.Command, func() {
		options.Operations.MarkOperationActive(options.SessionID.String(), options.OperationRef)
		if ordered != nil {
			ordered()
		}
	})
	if err != nil && options.AttemptContext.Err() != nil {
		options.Operations.RecordCanceledNotCommitted(options.SessionID.String(), options.OperationRef)
	} else {
		options.Operations.RecordShellCompletion(options.SessionID.String(), options.OperationRef, err)
	}
	return UserShellOperationOutcome{Result: result}, err
}

type ManualCompactionDriverOptions struct {
	SessionID      runtimeids.SessionID
	Arguments      string
	OperationRef   clientui.RuntimeOperationRef
	Operations     *runtimeops.Coordinator
	AttemptContext context.Context
}

type ManualCompactionDriver struct {
	options ManualCompactionDriverOptions
}

func NewManualCompactionDriver(options ManualCompactionDriverOptions) (SessionAgentOperationDriver, error) {
	if options.SessionID.IsZero() {
		return nil, errors.New("session id is required")
	}
	if options.Operations == nil {
		return nil, errors.New("runtime operation coordinator is required")
	}
	if err := options.OperationRef.Validate(); err != nil {
		return nil, err
	}
	if options.OperationRef.Kind != clientui.RuntimeOperationKindCompact {
		return nil, errors.New("manual compaction driver requires a compact operation")
	}
	if options.AttemptContext == nil {
		options.AttemptContext = context.Background()
	}
	return ManualCompactionDriver{options: options}, nil
}

func (ManualCompactionDriver) sessionAgentOperationDriver() {}

func (d ManualCompactionDriver) RuntimeOperationRef() (clientui.RuntimeOperationRef, bool) {
	return d.options.OperationRef, true
}

func (d ManualCompactionDriver) StartOwner(ctx context.Context, engine *runtime.Engine, ordering SessionAgentOperationOwnerOrderingNotifier) (SessionAgentOperationOutcome, error) {
	return d.run(ctx, engine, ordering.Complete)
}

func (d ManualCompactionDriver) JoinLive(ctx context.Context, engine *runtime.Engine) (SessionAgentOperationOutcome, error) {
	return d.run(ctx, engine, nil)
}

func (d ManualCompactionDriver) run(ctx context.Context, engine *runtime.Engine, ordered func() bool) (SessionAgentOperationOutcome, error) {
	if engine == nil {
		return ManualCompactionOperationOutcome{}, errors.New("runtime engine is required")
	}
	options := d.options
	receipt, err := engine.CompactContextWithActiveHook(ctx, options.Arguments, func() {
		options.Operations.MarkOperationActive(options.SessionID.String(), options.OperationRef)
		if ordered != nil {
			ordered()
		}
	})
	recordCompactionCompletion(
		options.Operations,
		options.SessionID.String(),
		options.OperationRef,
		receipt,
		err,
		options.AttemptContext,
	)
	return ManualCompactionOperationOutcome{Receipt: receipt}, err
}

type GoalMutationDriver struct {
	command GoalCommand
}

func NewGoalMutationDriver(command GoalCommand) (SessionAgentOperationDriver, error) {
	if command == nil {
		return nil, errors.New("goal command is required")
	}
	return GoalMutationDriver{command: command}, nil
}

func (GoalMutationDriver) sessionAgentOperationDriver() {}

func (GoalMutationDriver) RuntimeOperationRef() (clientui.RuntimeOperationRef, bool) {
	return clientui.RuntimeOperationRef{}, false
}

func (d GoalMutationDriver) StartOwner(ctx context.Context, engine *runtime.Engine, ordering SessionAgentOperationOwnerOrderingNotifier) (SessionAgentOperationOutcome, error) {
	outcome, err := d.run(engine)
	if outcome.Result.Accepted() {
		ordering.Complete()
	}
	return outcome, err
}

func (d GoalMutationDriver) JoinLive(_ context.Context, engine *runtime.Engine) (SessionAgentOperationOutcome, error) {
	return d.run(engine)
}

func (d GoalMutationDriver) run(engine *runtime.Engine) (GoalMutationOperationOutcome, error) {
	result, err := executeLiveGoalCommand(engine, d.command)
	return GoalMutationOperationOutcome{Result: result}, err
}

func executeLiveGoalCommand(engine *runtime.Engine, command GoalCommand) (GoalCommandResult, error) {
	switch typed := command.(type) {
	case GoalSetCommand:
		if isStepScoped(typed.Actor, typed.Execution) {
			return executeExactGoalMutation(engine, typed.Execution, func() (GoalCommandResult, error) {
				goal, queued, err := queueSet(engine, typed)
				if err != nil {
					return GoalCommandResult{Err: err}, err
				}
				if queued {
					return queuedGoalResult(goal), nil
				}
				return GoalCommandResult{Err: runtime.ErrAgentGoalStepInactive}, runtime.ErrAgentGoalStepInactive
			})
		}
		return liveSet(engine, typed)
	case GoalStatusCommand:
		if isStepScoped(typed.Actor, typed.Execution) {
			return executeExactGoalMutation(engine, typed.Execution, func() (GoalCommandResult, error) {
				goal, queued, err := queueStatus(engine, typed)
				if err != nil {
					return GoalCommandResult{Err: err}, err
				}
				if queued {
					return queuedGoalResult(goal), nil
				}
				return GoalCommandResult{Err: runtime.ErrAgentGoalStepInactive}, runtime.ErrAgentGoalStepInactive
			})
		}
		return liveStatus(engine, typed)
	case GoalClearCommand:
		return liveClear(engine, typed)
	default:
		return GoalCommandResult{}, errors.New("unsupported goal command")
	}
}

func executeExactGoalMutation(
	engine *runtime.Engine,
	execution GoalExecutionIdentity,
	mutate func() (GoalCommandResult, error),
) (GoalCommandResult, error) {
	if engine == nil {
		return GoalCommandResult{}, errors.New("runtime engine is required")
	}
	active := runtimeactivity.ActiveStepFromProvider(engine)
	if active == nil ||
		active.StepID != *execution.StepID ||
		(execution.RunID != nil && active.RunID != *execution.RunID) {
		return GoalCommandResult{}, runtime.ErrAgentGoalStepInactive
	}
	return mutate()
}

func activeExecutionAllowsUserTurnAutoDrain(snapshot *runtimeactivity.ActiveStepSnapshot) bool {
	if snapshot == nil {
		return false
	}
	switch snapshot.ActiveKind {
	case clientui.RuntimeActivityActiveKindCompaction, clientui.RuntimeActivityActiveKindPreSubmitCompaction:
		return true
	default:
		return false
	}
}

func recordCompactionCompletion(
	operations *runtimeops.Coordinator,
	sessionID string,
	ref clientui.RuntimeOperationRef,
	receipt session.CommitReceipt,
	err error,
	attemptContext context.Context,
) {
	if !receipt.Committed && err != nil && attemptContext != nil && attemptContext.Err() != nil {
		operations.RecordCanceledNotCommitted(sessionID, ref)
		return
	}
	operations.RecordCompactCompletion(sessionID, ref, receipt, err)
}

func recordRuntimeOperationFailure(
	operations *runtimeops.Coordinator,
	sessionID string,
	ref clientui.RuntimeOperationRef,
	err error,
	attemptContext context.Context,
) {
	if err != nil && attemptContext != nil && attemptContext.Err() != nil {
		operations.RecordCanceledNotCommitted(sessionID, ref)
		return
	}
	operations.RecordRuntimeAccessFailure(sessionID, ref)
}
