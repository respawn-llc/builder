package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/prompts"
	"core/server/llm"
	"core/server/session"
	"core/shared/runtimeids"
	"core/shared/toolspec"
	"core/shared/transcript"
)

const goalObjectivePreviewMaxRunes = 120
const goalLoopBusyRetryDelay = 50 * time.Millisecond

var ErrGoalRequiresAskQuestion = errors.New("active goal requires ask_question tool visibility; start with ask_question available or pause/clear the goal")
var errGoalLoopInactive = errors.New("goal loop inactive")
var ErrAgentGoalStepInactive = errors.New("agent goal command originating step is no longer active")

type GoalCommandDisposition uint8

const (
	GoalCommandApplied GoalCommandDisposition = iota + 1
	GoalCommandNoop
)

type GoalCommandResult struct {
	session.GoalState
	Disposition     GoalCommandDisposition
	Cleared         bool
	Availability    *session.GoalAvailability
	MetadataReceipt session.CommitReceipt
	NoticeReceipt   session.CommitReceipt
}

type CurrentGoalOperation interface{ currentGoalOperation() }

type CurrentGoalSet struct {
	Objective string
	Actor     session.GoalActor
}

func (CurrentGoalSet) currentGoalOperation() {}

type CurrentGoalStatus struct {
	Status session.GoalStatus
	Actor  session.GoalActor
}

func (CurrentGoalStatus) currentGoalOperation() {}

type CurrentGoalClear struct{ Actor session.GoalActor }

func (CurrentGoalClear) currentGoalOperation() {}

type CurrentGoalExecutionOwnership uint8

const (
	CurrentGoalRetainedOnly CurrentGoalExecutionOwnership = iota + 1
	CurrentGoalExactExecution
)

type CurrentGoalOperationOutcome struct {
	Handled           *GoalCommandResult
	ExecutionRequired bool
}

func (o CurrentGoalOperationOutcome) Validate() error {
	if (o.Handled != nil) == o.ExecutionRequired {
		return errors.New("current Goal operation must be handled or require execution")
	}
	return nil
}

func currentGoalHandled(result GoalCommandResult, err error) (CurrentGoalOperationOutcome, error) {
	outcome := CurrentGoalOperationOutcome{Handled: &result}
	if validationErr := outcome.Validate(); validationErr != nil {
		return CurrentGoalOperationOutcome{}, validationErr
	}
	return outcome, err
}

func currentGoalExecutionRequired() (CurrentGoalOperationOutcome, error) {
	outcome := CurrentGoalOperationOutcome{ExecutionRequired: true}
	return outcome, outcome.Validate()
}

func (e *Engine) currentGoalDisposition(disposition GoalCommandDisposition, goal session.GoalState, cleared bool) (CurrentGoalOperationOutcome, error) {
	availability, err := e.GoalAvailability()
	if err != nil {
		return CurrentGoalOperationOutcome{}, err
	}
	result := goalCommandResult(disposition, goal, cleared, session.CommitReceipt{}, session.CommitReceipt{})
	result.Availability = &availability
	return currentGoalHandled(result, nil)
}

func (e *Engine) ApplyCurrentGoalOperation(ctx context.Context, operation CurrentGoalOperation, ownership CurrentGoalExecutionOwnership) (CurrentGoalOperationOutcome, error) {
	if ownership != CurrentGoalRetainedOnly && ownership != CurrentGoalExactExecution {
		return CurrentGoalOperationOutcome{}, errors.New("current Goal execution ownership is required")
	}
	result, applied, err := e.applyGoalForActiveStep(nil, operation)
	if err != nil || applied {
		return currentGoalHandled(result, err)
	}
	switch operation := operation.(type) {
	case CurrentGoalSet:
		if ownership == CurrentGoalExactExecution {
			result, err := e.SetGoalAndStartLoop(ctx, operation.Objective, operation.Actor)
			return currentGoalHandled(result, err)
		}
		if operation.Actor == session.GoalActorAgent {
			if current := e.Goal(); current != nil && current.Status != session.GoalStatusComplete {
				return CurrentGoalOperationOutcome{}, session.GoalAgentOverwriteBlockedError{Goal: *current}
			}
		}
		return currentGoalExecutionRequired()
	case CurrentGoalStatus:
		status := operation.Status
		if current := e.Goal(); current != nil && current.Status == status {
			if status != session.GoalStatusActive || ownership == CurrentGoalExactExecution || e.CurrentNodeExecutionConfigured() || e.GoalLoopContinuationEnforced() {
				return e.currentGoalDisposition(GoalCommandNoop, *current, false)
			}
		}
		if ownership == CurrentGoalExactExecution {
			result, err := e.SetGoalStatusAndStartLoop(ctx, status, operation.Actor)
			return currentGoalHandled(result, err)
		}
		if status == session.GoalStatusActive {
			return currentGoalExecutionRequired()
		}
		return currentGoalHandled(e.SetGoalStatusWithoutGoalLoopStart(ctx, status, operation.Actor))
	case CurrentGoalClear:
		return currentGoalHandled(e.ClearGoal(ctx, operation.Actor))
	default:
		return CurrentGoalOperationOutcome{}, errors.New("current Goal operation is required")
	}
}

func goalCommandResult(
	disposition GoalCommandDisposition,
	goal session.GoalState,
	cleared bool,
	metadataReceipt session.CommitReceipt,
	noticeReceipt session.CommitReceipt,
) GoalCommandResult {
	return GoalCommandResult{
		GoalState:       goal,
		Disposition:     disposition,
		Cleared:         cleared,
		MetadataReceipt: metadataReceipt,
		NoticeReceipt:   noticeReceipt,
	}
}

func (e *Engine) Goal() *session.GoalState {
	if e == nil || e.store == nil {
		return nil
	}
	return cloneRuntimeGoal(e.store.Meta().Goal)
}

func (e *Engine) GoalAvailability() (session.GoalAvailability, error) {
	if e == nil || e.store == nil {
		return 0, errors.New("runtime session store is required")
	}
	return e.store.GoalAvailability()
}

func (e *Engine) GoalMutationAvailability() *session.GoalAvailability {
	if e == nil || e.store == nil {
		return nil
	}
	return e.store.GoalMutationAvailability()
}

func (e *Engine) GoalLoopSuspended() bool {
	if e == nil {
		return false
	}
	return e.goalLoopState().Suspended() && e.goalActive()
}

func (e *Engine) GoalLoopRunning() bool {
	return e != nil && e.goalLoopState().Running()
}

func (e *Engine) WaitForGoalLoop(ctx context.Context) error {
	if e == nil {
		return nil
	}
	return e.goalLoopState().Wait(ctx)
}

func (e *Engine) GoalLoopContinuationEnforced() bool {
	if e == nil {
		return false
	}
	return e.goalLoopState().ContinuationEnforced() && e.goalActive()
}

func (e *Engine) SetGoal(ctx context.Context, objective string, actor session.GoalActor) (GoalCommandResult, error) {
	return e.setGoalRuntime(ctx, objective, actor, false)
}

func (e *Engine) SetGoalAndStartLoop(ctx context.Context, objective string, actor session.GoalActor) (GoalCommandResult, error) {
	return e.setGoalRuntime(ctx, objective, actor, true)
}

func (e *Engine) setGoalRuntime(ctx context.Context, objective string, actor session.GoalActor, startLoop bool) (GoalCommandResult, error) {
	if e == nil {
		return GoalCommandResult{}, ErrEngineClosed
	}
	e.goalMutationMu.Lock()
	defer e.goalMutationMu.Unlock()
	if e.closed.Load() {
		return GoalCommandResult{}, ErrEngineClosed
	}
	if startLoop {
		if err := e.RequireGoalLoopStartAllowed(); err != nil {
			return GoalCommandResult{}, err
		}
	}
	return e.setGoalRaw(objective, actor, startLoop)
}

func (e *Engine) ValidateGoalSet(objective string, actor session.GoalActor) error {
	if e == nil || e.store == nil {
		return fmt.Errorf("runtime engine is required")
	}
	return e.store.ValidateGoalSet(objective, actor)
}

func (e *Engine) setGoalRaw(objective string, actor session.GoalActor, startLoop bool) (GoalCommandResult, error) {
	if e == nil || e.store == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	objective = strings.TrimSpace(objective)
	availability, err := e.GoalAvailability()
	if err != nil {
		return GoalCommandResult{}, err
	}
	goal, metadataReceipt, err := e.store.SetGoal(objective, actor)
	result := goalCommandResult(GoalCommandApplied, goal, false, metadataReceipt, session.CommitReceipt{})
	result.Availability = &availability
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeSet, &goal)
	if err != nil {
		return result, err
	}
	return result, e.enqueueGoalNotice(msg, goalStatusUpdateFromState(goal, &availability), startLoop)
}

func (e *Engine) SetGoalStatus(ctx context.Context, status session.GoalStatus, actor session.GoalActor) (GoalCommandResult, error) {
	return e.setGoalStatusRuntime(ctx, status, actor, true, false)
}

func (e *Engine) SetGoalStatusWithoutGoalLoopStart(ctx context.Context, status session.GoalStatus, actor session.GoalActor) (GoalCommandResult, error) {
	return e.setGoalStatusRuntime(ctx, status, actor, false, false)
}

func (e *Engine) SetGoalStatusAndStartLoop(ctx context.Context, status session.GoalStatus, actor session.GoalActor) (GoalCommandResult, error) {
	return e.setGoalStatusRuntime(ctx, status, actor, true, true)
}

func (e *Engine) setGoalStatusRuntime(ctx context.Context, status session.GoalStatus, actor session.GoalActor, requireGoalLoopStart bool, startLoop bool) (GoalCommandResult, error) {
	if e == nil {
		return GoalCommandResult{}, ErrEngineClosed
	}
	e.goalMutationMu.Lock()
	defer e.goalMutationMu.Unlock()
	if e.closed.Load() {
		return GoalCommandResult{}, ErrEngineClosed
	}
	return e.setGoalStatusRaw(status, actor, requireGoalLoopStart, startLoop)
}

func (e *Engine) setGoalStatusRaw(status session.GoalStatus, actor session.GoalActor, requireGoalLoopStart bool, startLoop bool) (GoalCommandResult, error) {
	if e == nil || e.store == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	availability, err := e.GoalAvailability()
	if err != nil {
		return GoalCommandResult{}, err
	}
	if current := e.Goal(); current != nil && current.Status == status {
		if status != session.GoalStatusActive || e.GoalLoopContinuationEnforced() {
			result := goalCommandResult(GoalCommandNoop, *current, false, session.CommitReceipt{}, session.CommitReceipt{})
			result.Availability = &availability
			return result, nil
		}
	}
	if status == session.GoalStatusActive && requireGoalLoopStart {
		if err := e.RequireGoalLoopStartAllowed(); err != nil {
			return GoalCommandResult{}, err
		}
	}
	goal, transitioned, metadataReceipt, err := e.store.SetGoalStatus(status, actor)
	disposition := GoalCommandApplied
	if err == nil && !transitioned {
		disposition = GoalCommandNoop
	}
	result := goalCommandResult(disposition, goal, false, metadataReceipt, session.CommitReceipt{})
	result.Availability = &availability
	if err != nil || !transitioned || !metadataReceipt.Committed {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeStatus, &goal)
	if err != nil {
		return result, err
	}
	return result, e.enqueueGoalNotice(msg, goalStatusUpdateFromState(goal, &availability), startLoop && status == session.GoalStatusActive)
}

func (e *Engine) ApplyGoalForStep(stepID string, operation CurrentGoalOperation) (GoalCommandResult, error) {
	identity, err := runtimeids.ParseStepID(stepID)
	if err != nil {
		return GoalCommandResult{}, err
	}
	result, _, err := e.applyGoalForActiveStep(&identity, operation)
	return result, err
}

func (e *Engine) applyGoalForActiveStep(expectedStep *runtimeids.StepID, operation CurrentGoalOperation) (GoalCommandResult, bool, error) {
	if e == nil || e.stepLifecycle == nil {
		return GoalCommandResult{}, false, ErrEngineClosed
	}
	var result GoalCommandResult
	applied, err := e.stepLifecycle.WithActiveStep(func(stepID string) error {
		stepID = strings.TrimSpace(stepID)
		if stepID == "" {
			return errors.New("active Step identity is required")
		}
		if expectedStep != nil && stepID != expectedStep.String() {
			return ErrAgentGoalStepInactive
		}
		var err error
		startLoop := !e.currentNodeExecutionActive()
		switch operation := operation.(type) {
		case CurrentGoalSet:
			result, err = e.setGoalRuntime(context.Background(), operation.Objective, operation.Actor, startLoop)
		case CurrentGoalStatus:
			result, err = e.setGoalStatusRuntime(context.Background(), operation.Status, operation.Actor, startLoop, startLoop)
		case CurrentGoalClear:
			result, err = e.ClearGoal(context.Background(), operation.Actor)
		default:
			return errors.New("invalid goal mutation")
		}
		return err
	})
	if err == nil && expectedStep != nil && !applied {
		err = ErrAgentGoalStepInactive
	}
	return result, applied, err
}

func (e *Engine) ClearGoal(ctx context.Context, actor session.GoalActor) (GoalCommandResult, error) {
	if e == nil {
		return GoalCommandResult{}, ErrEngineClosed
	}
	e.goalMutationMu.Lock()
	defer e.goalMutationMu.Unlock()
	if e.closed.Load() {
		return GoalCommandResult{}, ErrEngineClosed
	}
	return e.clearGoalRaw(actor)
}

func (e *Engine) clearGoalRaw(actor session.GoalActor) (GoalCommandResult, error) {
	if e == nil || e.store == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	availability, err := e.GoalAvailability()
	if err != nil {
		return GoalCommandResult{}, err
	}
	goal, metadataReceipt, err := e.store.ClearGoal(actor)
	result := goalCommandResult(GoalCommandApplied, goal, true, metadataReceipt, session.CommitReceipt{})
	result.Availability = &availability
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeClear, nil)
	if err != nil {
		return result, err
	}
	return result, e.enqueueGoalNotice(msg, goalStatusClearUpdate(&availability), false)
}

// Goal metadata is committed before admission here. Only model-visible feedback
// waits for the Engine Intent Queue's next Step Boundary.
func (e *Engine) enqueueGoalNotice(message llm.Message, update GoalStatusUpdate, startLoop bool) error {
	_, accepted := trySubmitEngineRuntimeOperation(e, func(context.Context) (struct{}, error) {
		message = normalizeMessageForTranscript(message, e.transcriptWorkingDir())
		_, err := e.steerGoalNoticeAndStatusRaw(sessionSteeringProvenance(), message, update)
		if err == nil && startLoop {
			err = e.StartGoalLoop()
		}
		if err != nil {
			e.surfaceRunErrorRaw(err)
		}
		return struct{}{}, err
	})
	if !accepted {
		return ErrEngineClosed
	}
	return nil
}

func (e *Engine) cascadeCompleteActiveGoalOnWorkflowCompletion(stepID string) {
	if e == nil || e.store == nil {
		return
	}
	if !e.WorkflowTerminalState().Completed {
		return
	}
	e.goalMutationMu.Lock()
	defer e.goalMutationMu.Unlock()
	goal := e.Goal()
	if goal == nil || goal.Status != session.GoalStatusActive {
		return
	}
	reportErr := func(err error) {
		_ = e.steer(stepID, steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:       "Failed to auto-complete active goal on workflow completion: " + err.Error(),
		}))
	}
	availability, err := e.GoalAvailability()
	if err != nil {
		reportErr(err)
		return
	}
	completed, transitioned, _, err := e.store.CompleteGoalIfActive(goal.ID, session.GoalActorSystem)
	if err != nil {
		reportErr(err)
		return
	}
	if !transitioned {
		return
	}
	msg, err := goalNoticeMessage(GoalNoticeStatus, &completed)
	if err != nil {
		reportErr(err)
		return
	}
	if err := e.enqueueGoalNotice(msg, goalStatusUpdateFromState(completed, &availability), false); err != nil {
		reportErr(err)
	}
}

func goalStatusUpdateFromState(goal session.GoalState, availability *session.GoalAvailability) GoalStatusUpdate {
	return GoalStatusUpdate{State: goal}
}

func goalStatusClearUpdate(availability *session.GoalAvailability) GoalStatusUpdate {
	return GoalStatusUpdate{Cleared: true}
}

func steerGoalStatusUpdateIntent(update GoalStatusUpdate) steeringIntent {
	return steerEventIntent(Event{Kind: EventGoalStatusUpdated, GoalStatus: &update})
}

func (e *Engine) steerGoalNoticeAndStatusRaw(
	provenance steeringProvenance,
	message llm.Message,
	update GoalStatusUpdate,
) (session.CommitReceipt, error) {
	return e.steerWithCommitReceiptRaw(provenance, steerGoalNoticeAndStatusIntent(message, update))
}

func (e *Engine) StartGoalLoop() error {
	return e.startGoalLoop(true)
}

func (e *Engine) deferGoalLoopStart() {
	if e == nil {
		return
	}
	e.pendingGoalLoopStartMu.Lock()
	e.pendingGoalLoopStart = true
	e.pendingGoalLoopStartMu.Unlock()
}

func (e *Engine) resumeSuspendedGoalAfterSuccessfulUserTurn() {
	if e == nil || !e.goalActive() {
		return
	}
	state := e.goalLoopState()
	if !state.Suspended() {
		return
	}
	state.Resume()
	e.deferGoalLoopStart()
}

func (e *Engine) startPendingGoalLoop() error {
	if e == nil {
		return nil
	}
	e.pendingGoalLoopStartMu.Lock()
	pending := e.pendingGoalLoopStart
	e.pendingGoalLoopStart = false
	e.pendingGoalLoopStartMu.Unlock()
	if !pending {
		return nil
	}
	return e.startGoalLoop(true)
}

func (e *Engine) startGoalLoop(firstTurnAlreadyPrompted bool) error {
	if e == nil {
		return nil
	}
	e.ensureOrchestrationCollaborators()
	if !e.goalActive() {
		return nil
	}
	if e.currentNodeExecutionActive() {
		return nil
	}
	if err := e.RequireGoalLoopStartAllowed(); err != nil {
		return err
	}
	if !e.goalLoopState().Start() {
		return nil
	}

	e.launchGoalLoopTask(firstTurnAlreadyPrompted)
	return nil
}

func (e *Engine) launchGoalLoopTask(firstTurnAlreadyPrompted bool) {
	launched := e.launchLifecycleTask(func(ctx context.Context) *resultGroupFatal {
		defer e.finishGoalLoop()
		return e.runGoalLoop(ctx, firstTurnAlreadyPrompted)
	})
	if !launched {
		e.finishGoalLoop()
	}
}

func (e *Engine) finishGoalLoop() {
	if e.goalLoopState().Finish(e.goalActive()) {
		e.launchGoalLoopTask(true)
		return
	}
	e.finishLiveRunGoalLoop()
}

func (e *Engine) runGoalLoop(ctx context.Context, firstTurnAlreadyPrompted bool) *resultGroupFatal {
	appendNudge := !firstTurnAlreadyPrompted
	for {
		if !e.shouldContinueGoalLoop(ctx) {
			return nil
		}
		if _, err := e.runGoalTurn(ctx, appendNudge); err != nil {
			if errors.Is(err, ErrAgentBusy) {
				if !e.waitBeforeGoalLoopBusyRetry(ctx) {
					return nil
				}
				continue
			}
			if fatal, abort := resultGroupFatalFromError(err); abort {
				return fatal
			}
			e.surfaceRunError(err)
			return nil
		}
		appendNudge = true
	}
}

func (e *Engine) runGoalTurn(ctx context.Context, appendNudge bool) (assistant llm.Message, err error) {
	e.ensureOrchestrationCollaborators()
	err = e.stepLifecycle.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindGoalLoop}, func(stepCtx context.Context, stepID string) error {
		if err := e.ensureMetaContextForRequest(stepCtx, stepID); err != nil {
			return err
		}
		nudge, active := e.goalContinuation().nudgeMessage()
		if !active {
			return errGoalLoopInactive
		}
		if appendNudge {
			if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{nudge})); err != nil {
				return err
			}
		}
		msg, runErr := e.runStepLoop(stepCtx, stepID)
		assistant = msg
		return runErr
	})
	if errors.Is(err, errGoalLoopInactive) {
		return llm.Message{}, nil
	}
	return assistant, err
}

func (e *Engine) waitBeforeGoalLoopBusyRetry(ctx context.Context) bool {
	timer := time.NewTimer(goalLoopBusyRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (e *Engine) surfaceRunError(err error) {
	e.surfaceRunErrorWith(
		err,
		func(intent steeringIntent) error {
			return e.steerRuntime(intent)
		},
		func(message string) {
			e.SetStreamingError(message)
		},
	)
}

func (e *Engine) surfaceRunErrorForStep(stepID string, err error) {
	e.surfaceRunErrorWith(
		err,
		func(intent steeringIntent) error {
			return e.steer(stepID, intent)
		},
		func(message string) {
			_ = e.applyStreamingStateMutationForStep(stepID, func(state *transcriptRuntimeState) {
				state.SetStreamingError(message)
			})
		},
	)
}

func (e *Engine) surfaceRunErrorRaw(err error) {
	e.surfaceRunErrorWith(
		err,
		func(intent steeringIntent) error {
			return e.steerOrdered(sessionSteeringProvenance(), intent)
		},
		func(message string) {
			_ = e.applyStreamingStateMutationRaw(func(state *transcriptRuntimeState) {
				state.SetStreamingError(message)
			})
		},
	)
}

func (e *Engine) surfaceRunErrorWith(
	err error,
	steer func(steeringIntent) error,
	setStreamingError func(string),
) {
	if _, fatal := resultGroupFatalFromError(err); fatal {
		return
	}
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, ErrAgentBusy) ||
		errors.Is(err, errGoalLoopInactive) ||
		errors.Is(err, ErrEngineClosed) {
		return
	}
	message, feedback := runtimeErrorFeedback(err)
	appendErr := steer(feedback)
	if appendErr != nil {
		if message == "" {
			message = err.Error()
		}
		_ = steer(steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:       "Failed to persist run error: " + appendErr.Error(),
		}))
	}
	setStreamingError(message)
}

func runtimeAbortFeedbackMessage(fatal *resultGroupFatal) string {
	if fatal == nil {
		return ""
	}
	message := strings.TrimSpace(llm.UserFacingError(fatal.Cause))
	if message == "" && fatal.Cause != nil {
		message = fatal.Cause.Error()
	}
	if message == "" {
		message = fatal.Error()
	}
	return message
}

func (e *Engine) steerRuntimeErrorFeedback(err error) (string, error) {
	if err == nil {
		return "", errors.New("runtime error feedback requires an error")
	}
	message, feedback := runtimeErrorFeedback(err)
	return message, e.steerRuntime(feedback)
}

func runtimeErrorFeedback(err error) (string, steeringIntent) {
	message := strings.TrimSpace(llm.UserFacingError(err))
	if message == "" {
		message = err.Error()
	}
	return message, steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
		Text:       message,
	})
}

func (e *Engine) shouldContinueGoalLoop(ctx context.Context) bool {
	if e == nil {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return !e.goalLoopState().Suspended() && e.goalActive()
}

func (e *Engine) shouldHoldLiveRunForGoalLoopContinuation(snapshot *RunSnapshot, status RunStatus) bool {
	if e == nil || snapshot == nil || !snapshot.GoalLoop || status != RunStatusCompleted {
		return false
	}
	return e.goalLoopState().ContinuationEnforced() && e.shouldContinueGoalLoop(context.Background())
}

func (e *Engine) goalActive() bool {
	if e == nil || e.store == nil {
		return false
	}
	goal := e.store.Meta().Goal
	return goal != nil && goal.Status == session.GoalStatusActive
}

func (e *Engine) goalLoopState() *goalLoopState {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.goalLoop == nil {
		e.goalLoop = newGoalLoopState()
	}
	return e.goalLoop
}

func (e *Engine) RequireGoalLoopStartAllowed() error {
	shape, err := e.lockedRequestShape()
	if err != nil {
		return err
	}
	for _, id := range shape.EnabledTools {
		if id == toolspec.ToolAskQuestion {
			return nil
		}
	}
	return ErrGoalRequiresAskQuestion
}

func (e *Engine) requireAskQuestionWhenGoalActive() error {
	goal := e.Goal()
	if goal == nil || goal.Status != session.GoalStatusActive {
		return nil
	}
	return e.RequireGoalLoopStartAllowed()
}

func goalSetCompactText(objective string) string {
	return "Goal set: " + strconvQuoteForGoalPreview(objective)
}

func goalStatusPrompt(goal session.GoalState) string {
	switch goal.Status {
	case session.GoalStatusPaused:
		return prompts.GoalPausePrompt
	case session.GoalStatusActive:
		return prompts.RenderGoalResumePrompt(goal.Objective)
	case session.GoalStatusComplete:
		return prompts.GoalCompletePrompt
	default:
		return ""
	}
}

func goalStatusCompactText(goal session.GoalState) string {
	switch goal.Status {
	case session.GoalStatusPaused:
		return "Goal paused"
	case session.GoalStatusActive:
		return "Goal resumed: " + strconvQuoteForGoalPreview(goal.Objective)
	case session.GoalStatusComplete:
		return "Goal complete. Cooked for " + formatGoalDuration(goal.UpdatedAt.Sub(goal.CreatedAt))
	default:
		return "Goal updated"
	}
}

func formatGoalDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	totalSeconds := int64(duration / time.Second)
	hours := totalSeconds / int64(time.Hour/time.Second)
	minutes := totalSeconds % int64(time.Hour/time.Second) / int64(time.Minute/time.Second)
	seconds := totalSeconds % int64(time.Minute/time.Second)
	var out strings.Builder
	if hours > 0 {
		out.WriteString(fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 {
		out.WriteString(fmt.Sprintf("%dm", minutes))
	}
	if seconds > 0 || out.Len() == 0 {
		out.WriteString(fmt.Sprintf("%ds", seconds))
	}
	return out.String()
}

func strconvQuoteForGoalPreview(objective string) string {
	preview := strings.Join(strings.Fields(strings.TrimSpace(objective)), " ")
	runes := []rune(preview)
	if len(runes) > goalObjectivePreviewMaxRunes {
		preview = string(runes[:goalObjectivePreviewMaxRunes]) + "..."
	}
	return fmt.Sprintf("%q", preview)
}

func cloneRuntimeGoal(goal *session.GoalState) *session.GoalState {
	if goal == nil {
		return nil
	}
	copyGoal := *goal
	return &copyGoal
}
