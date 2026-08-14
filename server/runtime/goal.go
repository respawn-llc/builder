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
	GoalCommandQueued
	GoalCommandNoop
)

type GoalCommandResult struct {
	session.GoalState
	Disposition     GoalCommandDisposition
	Cleared         bool
	MetadataReceipt session.CommitReceipt
	NoticeReceipt   session.CommitReceipt
}

type GoalMutationKind uint8

const (
	GoalMutationSet GoalMutationKind = iota + 1
	GoalMutationStatus
	GoalMutationClear
)

type GoalMutation struct {
	Kind      GoalMutationKind
	Objective string
	Status    session.GoalStatus
	Actor     session.GoalActor
	StartLoop bool
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

func (e *Engine) SetGoal(objective string, actor session.GoalActor) (GoalCommandResult, error) {
	return e.ApplyGoalMutation(GoalMutation{
		Kind:      GoalMutationSet,
		Objective: objective,
		Actor:     actor,
	})
}

func (e *Engine) SetGoalStatus(status session.GoalStatus, actor session.GoalActor) (GoalCommandResult, error) {
	return e.ApplyGoalMutation(GoalMutation{
		Kind:      GoalMutationStatus,
		Status:    status,
		Actor:     actor,
		StartLoop: status == session.GoalStatusActive,
	})
}

func (e *Engine) SetGoalStatusWithoutGoalLoopStart(status session.GoalStatus, actor session.GoalActor) (GoalCommandResult, error) {
	return e.ApplyGoalMutation(GoalMutation{
		Kind:   GoalMutationStatus,
		Status: status,
		Actor:  actor,
	})
}

func (e *Engine) ClearGoal(actor session.GoalActor) (GoalCommandResult, error) {
	return e.ApplyGoalMutation(GoalMutation{Kind: GoalMutationClear, Actor: actor})
}

func (e *Engine) ApplyGoalMutation(mutation GoalMutation) (GoalCommandResult, error) {
	if e == nil || e.store == nil || e.steering == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	if err := e.workflowControl.validateSteering(steeringAdmissionGoal); err != nil {
		return GoalCommandResult{}, err
	}
	if err := e.validateGoalMutationForAdmission(mutation); err != nil {
		return GoalCommandResult{}, err
	}
	return e.enqueueGoalMutation(mutation)
}

func (e *Engine) ApplyGoalMutationDeferred(mutation GoalMutation) (GoalCommandResult, error) {
	if e == nil || e.store == nil || e.steering == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	if err := e.workflowControl.validateSteering(steeringAdmissionGoal); err != nil {
		return GoalCommandResult{}, err
	}
	if err := e.validateGoalMutationForAdmission(mutation); err != nil {
		return GoalCommandResult{}, err
	}
	projected, err := e.steering.projectGoal(e.Goal())
	if err != nil {
		return GoalCommandResult{}, err
	}
	if err := validateProjectedGoalMutation(projected, mutation); err != nil {
		return GoalCommandResult{}, err
	}
	if mutation.StartLoop {
		switch mutation.Kind {
		case GoalMutationSet:
			if !e.currentNodeExecutionActive() {
				if err := e.RequireGoalLoopStartAllowed(); err != nil {
					return GoalCommandResult{}, err
				}
			}
		case GoalMutationStatus:
			if mutation.Status == session.GoalStatusActive && !e.currentNodeExecutionActive() {
				if err := e.RequireGoalLoopStartAllowed(); err != nil {
					return GoalCommandResult{}, err
				}
			}
		}
	}
	if e.stepLifecycle.Snapshot() == nil {
		return e.enqueueGoalMutation(mutation)
	}
	var result GoalCommandResult
	entry := newRuntimeOutputSteeringQueueEntry(false, steeringIntent{items: []steeringMutation{
		&steeringGoalMutation{mutation: mutation, result: &result},
	}})
	if _, err := e.steering.appendDeferred(entry); err != nil {
		return GoalCommandResult{}, err
	}
	projected, err = e.steering.projectGoal(e.Goal())
	if err != nil {
		return GoalCommandResult{}, err
	}
	if projected == nil {
		return goalCommandResult(GoalCommandQueued, session.GoalState{}, true, session.CommitReceipt{}, session.CommitReceipt{}), nil
	}
	return goalCommandResult(GoalCommandQueued, *projected, false, session.CommitReceipt{}, session.CommitReceipt{}), nil
}

func (e *Engine) enqueueGoalMutation(mutation GoalMutation) (GoalCommandResult, error) {
	var result GoalCommandResult
	intent := steeringIntent{items: []steeringMutation{&steeringGoalMutation{
		mutation: mutation,
		result:   &result,
	}}}
	_, err := e.enqueueRuntimeSteering(false, intent)
	return result, err
}

func (e *Engine) ScheduleExactAgentGoalMutation(
	scopeID runtimeids.ExecutionScopeID,
	runID runtimeids.RunID,
	stepID runtimeids.StepID,
	mutation GoalMutation,
) (GoalCommandResult, error) {
	if e == nil || e.store == nil || e.steering == nil {
		return GoalCommandResult{}, ErrAgentGoalStepInactive
	}
	if scopeID.IsZero() || runID.IsZero() || stepID.IsZero() || mutation.Actor != session.GoalActorAgent {
		return GoalCommandResult{}, ErrAgentGoalStepInactive
	}
	if mutation.Kind != GoalMutationSet &&
		(mutation.Kind != GoalMutationStatus || mutation.Status != session.GoalStatusComplete) {
		return GoalCommandResult{}, errors.New("exact agent Goal mutation must set or complete a Goal")
	}
	if err := e.validateGoalMutationForAdmission(mutation); err != nil {
		return GoalCommandResult{}, err
	}
	var projected *session.GoalState
	err := e.stepLifecycle.ApplyForExactGoalStep(runID.String(), stepID.String(), func() error {
		execution := e.currentNodeExecutionSnapshot()
		if execution.config == nil || execution.config.ScopeID != scopeID {
			return ErrAgentGoalStepInactive
		}
		entry := newRuntimeOutputSteeringQueueEntry(false, steeringIntent{
			items: []steeringMutation{&steeringGoalMutation{mutation: mutation}},
		})
		var err error
		projected, err = e.steering.appendExactGoal(entry, e.Goal(), func(current *session.GoalState) error {
			return validateProjectedGoalMutation(current, mutation)
		})
		return err
	})
	if err != nil {
		if errors.Is(err, ErrActiveStepInactive) {
			return GoalCommandResult{}, ErrAgentGoalStepInactive
		}
		return GoalCommandResult{}, err
	}
	if projected == nil {
		return goalCommandResult(GoalCommandQueued, session.GoalState{}, true, session.CommitReceipt{}, session.CommitReceipt{}), nil
	}
	return goalCommandResult(GoalCommandQueued, *projected, false, session.CommitReceipt{}, session.CommitReceipt{}), nil
}

func validateProjectedGoalMutation(current *session.GoalState, mutation GoalMutation) error {
	switch mutation.Kind {
	case GoalMutationSet:
		if mutation.Actor == session.GoalActorAgent && current != nil && current.Status != session.GoalStatusComplete {
			return session.GoalAgentOverwriteBlockedError{Goal: *current}
		}
	case GoalMutationStatus:
		if current == nil {
			return errors.New("goal is not set")
		}
	}
	return nil
}

func projectGoalMutation(current *session.GoalState, mutation GoalMutation) *session.GoalState {
	switch mutation.Kind {
	case GoalMutationSet:
		now := time.Now().UTC()
		next := session.GoalState{
			Objective: strings.TrimSpace(mutation.Objective),
			Status:    session.GoalStatusActive,
			CreatedAt: now,
			UpdatedAt: now,
		}
		return &next
	case GoalMutationStatus:
		if current == nil {
			return nil
		}
		next := *current
		next.Status = mutation.Status
		next.UpdatedAt = time.Now().UTC()
		return &next
	case GoalMutationClear:
		return nil
	default:
		return current
	}
}

func (e *Engine) validateGoalMutationForAdmission(mutation GoalMutation) error {
	if mutation.Kind == GoalMutationSet {
		mutation.Objective = strings.TrimSpace(mutation.Objective)
		if mutation.Objective == "" {
			return errors.New("goal objective is required")
		}
	}
	if mutation.Kind == GoalMutationStatus && mutation.Status == session.GoalStatusActive && mutation.StartLoop {
		if e.WorkflowControlled() && !e.currentNodeExecutionActive() {
			current := e.Goal()
			if current == nil || current.Status != session.GoalStatusActive {
				return ErrSteeringUnavailable
			}
		}
		return e.RequireGoalLoopStartAllowed()
	}
	if mutation.Kind == GoalMutationSet && mutation.StartLoop &&
		e.WorkflowControlled() && !e.currentNodeExecutionActive() {
		return ErrSteeringUnavailable
	}
	return nil
}

func (e *Engine) applyGoalMutation(
	mutation GoalMutation,
	applyNotice func(*steeringGoalNoticeAndStatus, *session.CommitReceipt) error,
) (GoalCommandResult, error) {
	switch mutation.Kind {
	case GoalMutationSet:
		return e.applyGoalSet(mutation, applyNotice)
	case GoalMutationStatus:
		return e.applyGoalStatus(mutation, applyNotice)
	case GoalMutationClear:
		return e.applyGoalClear(mutation.Actor, applyNotice)
	default:
		return GoalCommandResult{}, fmt.Errorf("unsupported Goal mutation kind %d", mutation.Kind)
	}
}

func (e *Engine) applyGoalSet(
	mutation GoalMutation,
	applyNotice func(*steeringGoalNoticeAndStatus, *session.CommitReceipt) error,
) (GoalCommandResult, error) {
	objective := strings.TrimSpace(mutation.Objective)
	goal, metadataReceipt, err := e.store.SetGoal(objective, mutation.Actor)
	result := goalCommandResult(GoalCommandApplied, goal, false, metadataReceipt, session.CommitReceipt{})
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeSet, &goal)
	if err != nil {
		return result, err
	}
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	noticeReceipt := session.CommitReceipt{}
	err = applyNotice(&steeringGoalNoticeAndStatus{message: msg, update: goalStatusUpdateFromState(goal)}, &noticeReceipt)
	result.NoticeReceipt = noticeReceipt
	if err == nil && mutation.StartLoop && !e.currentNodeExecutionActive() {
		e.deferGoalLoopStart()
	}
	return result, err
}

func (e *Engine) applyGoalStatus(
	mutation GoalMutation,
	applyNotice func(*steeringGoalNoticeAndStatus, *session.CommitReceipt) error,
) (GoalCommandResult, error) {
	status := mutation.Status
	if e == nil || e.store == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	if current := e.Goal(); current != nil && current.Status == status {
		if status != session.GoalStatusActive || e.GoalLoopContinuationEnforced() {
			return goalCommandResult(GoalCommandNoop, *current, false, session.CommitReceipt{}, session.CommitReceipt{}), nil
		}
	}
	goal, transitioned, metadataReceipt, err := e.store.SetGoalStatus(status, mutation.Actor)
	disposition := GoalCommandApplied
	if err == nil && !transitioned {
		disposition = GoalCommandNoop
	}
	result := goalCommandResult(disposition, goal, false, metadataReceipt, session.CommitReceipt{})
	if err != nil || !transitioned || !metadataReceipt.Committed {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeStatus, &goal)
	if err != nil {
		return result, err
	}
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	noticeReceipt := session.CommitReceipt{}
	err = applyNotice(&steeringGoalNoticeAndStatus{message: msg, update: goalStatusUpdateFromState(goal)}, &noticeReceipt)
	result.NoticeReceipt = noticeReceipt
	if err == nil && mutation.StartLoop && status == session.GoalStatusActive && !e.currentNodeExecutionActive() {
		e.deferGoalLoopStart()
	}
	return result, err
}

func (e *Engine) applyGoalClear(
	actor session.GoalActor,
	applyNotice func(*steeringGoalNoticeAndStatus, *session.CommitReceipt) error,
) (GoalCommandResult, error) {
	goal, metadataReceipt, err := e.store.ClearGoal(actor)
	result := goalCommandResult(GoalCommandApplied, goal, true, metadataReceipt, session.CommitReceipt{})
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeClear, nil)
	if err != nil {
		return result, err
	}
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	noticeReceipt := session.CommitReceipt{}
	err = applyNotice(&steeringGoalNoticeAndStatus{message: msg, update: goalStatusClearUpdate()}, &noticeReceipt)
	result.NoticeReceipt = noticeReceipt
	return result, err
}

func (e *Engine) cascadeCompleteActiveGoalOnWorkflowCompletion() {
	if e == nil || e.store == nil {
		return
	}
	if !e.WorkflowTerminalState().Completed {
		return
	}
	goal := e.Goal()
	if goal == nil || goal.Status != session.GoalStatusActive {
		return
	}
	reportErr := func(err error) {
		_ = e.steerCurrentStepOrRuntime(steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:       "Failed to auto-complete active goal on workflow completion: " + err.Error(),
		}))
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
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	if err := e.steerCurrentStepOrRuntime(steerGoalNoticeAndStatusIntent(msg, goalStatusUpdateFromState(completed))); err != nil {
		reportErr(err)
	}
}

func goalStatusUpdateFromState(goal session.GoalState) GoalStatusUpdate {
	return GoalStatusUpdate{State: goal}
}

func goalStatusClearUpdate() GoalStatusUpdate {
	return GoalStatusUpdate{Cleared: true}
}

func (e *Engine) steerGoalNoticeAndStatus(
	stepID string,
	message llm.Message,
	update GoalStatusUpdate,
) (session.CommitReceipt, error) {
	if e == nil || e.closed.Load() {
		return session.CommitReceipt{}, ErrEngineClosed
	}
	return e.steerWithCommitReceipt(stepID, steerGoalNoticeAndStatusIntent(message, update))
}

func (e *Engine) StartGoalLoop() error {
	return e.startGoalLoop(true)
}

func (e *Engine) deferGoalLoopStart() {
	if e == nil {
		return
	}
	e.goalContinuationMu.Lock()
	e.pendingGoalLoopStart = true
	e.goalContinuationMu.Unlock()
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
	e.goalContinuationMu.Lock()
	pending := e.pendingGoalLoopStart
	e.pendingGoalLoopStart = false
	e.goalContinuationMu.Unlock()
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
			if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{nudge})); err != nil {
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
	message, appendErr := e.steerRuntimeErrorFeedback(err)
	if appendErr != nil {
		if message == "" {
			message = err.Error()
		}
		_ = e.steerRuntime(steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:       "Failed to persist run error: " + appendErr.Error(),
		}))
	}
	e.SetStreamingError(message)
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
	message := strings.TrimSpace(llm.UserFacingError(err))
	if message == "" {
		message = err.Error()
	}
	return message, e.steerRuntime(steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
		Text:       message,
	}))
}

func (e *Engine) queueRuntimeErrorFeedback(err error) {
	if err == nil {
		return
	}
	message := strings.TrimSpace(llm.UserFacingError(err))
	if message == "" {
		message = err.Error()
	}
	entry := newRuntimeOutputSteeringQueueEntry(false, steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
		Text:       message,
	}))
	if _, appendErr := e.steering.append(entry); appendErr != nil {
		e.transcriptRuntimeState().SetStreamingError("Failed to persist run error: " + appendErr.Error())
		return
	}
	e.transcriptRuntimeState().SetStreamingError(message)
	_, _ = e.steering.append(newRuntimeOutputSteeringQueueEntry(false,
		steerEventIntent(Event{Kind: EventStreamingErrorUpdated})))
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
