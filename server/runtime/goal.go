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
	"core/shared/toolspec"
	"core/shared/transcript"
)

const goalObjectivePreviewMaxRunes = 120
const goalLoopBusyRetryDelay = 50 * time.Millisecond

var ErrGoalRequiresAskQuestion = errors.New("active goal requires ask_question tool visibility; start with ask_question available or pause/clear the goal")
var errGoalLoopInactive = errors.New("goal loop inactive")

type activeStepGoalMutationKind uint8

const (
	activeStepGoalMutationSet activeStepGoalMutationKind = iota
	activeStepGoalMutationComplete
)

type activeStepGoalMutation struct {
	kind      activeStepGoalMutationKind
	objective string
	actor     session.GoalActor
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

func (e *Engine) SetGoal(objective string, actor session.GoalActor) (session.GoalState, error) {
	return e.setGoalForStep("", objective, actor)
}

func (e *Engine) setGoalForStep(stepID string, objective string, actor session.GoalActor) (session.GoalState, error) {
	return e.setGoalStateForStep(stepID, session.GoalState{Objective: objective}, actor)
}

func (e *Engine) setGoalStateForStep(stepID string, goalState session.GoalState, actor session.GoalActor) (session.GoalState, error) {
	if e == nil || e.store == nil {
		return session.GoalState{}, fmt.Errorf("runtime engine is required")
	}
	msg := normalizeMessageForTranscript(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: prompts.RenderGoalSetPrompt(strings.TrimSpace(goalState.Objective)), CompactContent: goalSetCompactText(goalState.Objective)}, e.transcriptWorkingDir())
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	goal, err := e.store.SetActiveGoalWithEvents(goalState, actor, []session.EventInput{{Kind: "message", Payload: msg}})
	if err != nil {
		return session.GoalState{}, err
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, false, []llm.Message{msg}), steerGoalStatusUpdateIntent(goalStatusUpdateFromState(goal))); err != nil {
		return session.GoalState{}, err
	}
	return goal, nil
}

func (e *Engine) SetGoalStatus(status session.GoalStatus, actor session.GoalActor) (session.GoalState, error) {
	return e.setGoalStatusForStep("", status, actor)
}

func (e *Engine) setGoalStatusForStep(stepID string, status session.GoalStatus, actor session.GoalActor) (session.GoalState, error) {
	if e == nil || e.store == nil {
		return session.GoalState{}, fmt.Errorf("runtime engine is required")
	}
	if status == session.GoalStatusActive {
		if err := e.requireAskQuestionForGoalActivation(); err != nil {
			return session.GoalState{}, err
		}
	}
	transcriptWorkingDir := e.transcriptWorkingDir()
	var msg llm.Message
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	goal, err := e.store.SetGoalStatusWithEventBuilder(status, actor, func(goal session.GoalState) ([]session.EventInput, error) {
		msg = normalizeMessageForTranscript(llm.Message{
			Role:           llm.RoleDeveloper,
			MessageType:    llm.MessageTypeGoal,
			Content:        goalStatusPrompt(goal),
			CompactContent: goalStatusCompactText(goal),
		}, transcriptWorkingDir)
		return []session.EventInput{{Kind: "message", Payload: msg}}, nil
	})
	if err != nil {
		return session.GoalState{}, err
	}
	if err := e.steer(stepID, steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, false, []llm.Message{msg}), steerGoalStatusUpdateIntent(goalStatusUpdateFromState(goal))); err != nil {
		return session.GoalState{}, err
	}
	return goal, nil
}

func (e *Engine) QueueAgentShellSetGoal(objective string, actor session.GoalActor) (session.GoalState, bool, error) {
	if e == nil || e.store == nil {
		return session.GoalState{}, false, fmt.Errorf("runtime engine is required")
	}
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return session.GoalState{}, false, errors.New("goal objective is required")
	}
	if err := e.RequireGoalLoopStartAllowed(); err != nil {
		return session.GoalState{}, false, err
	}
	queued, err := e.enqueueActiveStepGoalMutation(activeStepGoalMutation{
		kind:      activeStepGoalMutationSet,
		objective: objective,
		actor:     actor,
	})
	if err != nil || !queued {
		return session.GoalState{}, queued, err
	}
	now := time.Now().UTC()
	return session.GoalState{Objective: objective, Status: session.GoalStatusActive, CreatedAt: now, UpdatedAt: now}, true, nil
}

func (e *Engine) QueueAgentShellCompleteGoal(actor session.GoalActor) (session.GoalState, bool, error) {
	if e == nil || e.store == nil {
		return session.GoalState{}, false, fmt.Errorf("runtime engine is required")
	}
	current := e.Goal()
	if current == nil {
		return session.GoalState{}, false, errors.New("goal is not set")
	}
	queued, err := e.enqueueActiveStepGoalMutation(activeStepGoalMutation{
		kind:  activeStepGoalMutationComplete,
		actor: actor,
	})
	if err != nil || !queued {
		return session.GoalState{}, queued, err
	}
	accepted := *current
	accepted.Status = session.GoalStatusComplete
	accepted.UpdatedAt = time.Now().UTC()
	return accepted, true, nil
}

func (e *Engine) enqueueActiveStepGoalMutation(mutation activeStepGoalMutation) (bool, error) {
	if e == nil || e.stepLifecycle == nil {
		return false, nil
	}
	return e.stepLifecycle.WithActiveStep(func(stepID string) error {
		stepID = strings.TrimSpace(stepID)
		if stepID == "" {
			return nil
		}
		e.activeStepGoalMutationsMu.Lock()
		defer e.activeStepGoalMutationsMu.Unlock()
		if e.activeStepGoalMutations == nil {
			e.activeStepGoalMutations = make(map[string][]activeStepGoalMutation)
		}
		e.activeStepGoalMutations[stepID] = append(e.activeStepGoalMutations[stepID], mutation)
		return nil
	})
}

func (e *Engine) drainActiveStepGoalMutations(stepID string) error {
	stepID = strings.TrimSpace(stepID)
	if e == nil || stepID == "" {
		return nil
	}
	for {
		mutation, ok := e.peekActiveStepGoalMutation(stepID)
		if !ok {
			return nil
		}
		if err := e.applyActiveStepGoalMutation(stepID, mutation); err != nil {
			return err
		}
		e.shiftActiveStepGoalMutation(stepID)
	}
}

func (e *Engine) peekActiveStepGoalMutation(stepID string) (activeStepGoalMutation, bool) {
	e.activeStepGoalMutationsMu.Lock()
	defer e.activeStepGoalMutationsMu.Unlock()
	pending := e.activeStepGoalMutations[stepID]
	if len(pending) == 0 {
		return activeStepGoalMutation{}, false
	}
	return pending[0], true
}

func (e *Engine) shiftActiveStepGoalMutation(stepID string) {
	e.activeStepGoalMutationsMu.Lock()
	defer e.activeStepGoalMutationsMu.Unlock()
	pending := e.activeStepGoalMutations[stepID]
	if len(pending) <= 1 {
		delete(e.activeStepGoalMutations, stepID)
		return
	}
	e.activeStepGoalMutations[stepID] = pending[1:]
}

func (e *Engine) applyActiveStepGoalMutation(stepID string, mutation activeStepGoalMutation) error {
	switch mutation.kind {
	case activeStepGoalMutationSet:
		if _, err := e.setGoalForStep(stepID, mutation.objective, mutation.actor); err != nil {
			return err
		}
		e.deferGoalLoopStart()
		return nil
	case activeStepGoalMutationComplete:
		_, err := e.setGoalStatusForStep(stepID, session.GoalStatusComplete, mutation.actor)
		return err
	default:
		return fmt.Errorf("unsupported active-step goal mutation kind %d", mutation.kind)
	}
}

func (e *Engine) ClearGoal(actor session.GoalActor) (session.GoalState, error) {
	if e == nil || e.store == nil {
		return session.GoalState{}, fmt.Errorf("runtime engine is required")
	}
	msg := normalizeMessageForTranscript(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: prompts.GoalClearPrompt, CompactContent: "Goal cleared"}, e.transcriptWorkingDir())
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	goal, err := e.store.ClearGoalWithEvents(actor, []session.EventInput{{Kind: "message", Payload: msg}})
	if err != nil {
		return session.GoalState{}, err
	}
	if err := e.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, false, []llm.Message{msg}), steerGoalStatusUpdateIntent(goalStatusClearUpdate())); err != nil {
		return session.GoalState{}, err
	}
	return goal, nil
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
		_ = e.steer("", steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:       "Failed to auto-complete active goal on workflow completion: " + err.Error(),
		}))
	}
	transcriptWorkingDir := e.transcriptWorkingDir()
	var msg llm.Message
	e.controlMutationMu.Lock()
	defer e.controlMutationMu.Unlock()
	completed, transitioned, err := e.store.CompleteGoalIfActive(goal.ID, session.GoalActorSystem, func(g session.GoalState) ([]session.EventInput, error) {
		msg = normalizeMessageForTranscript(llm.Message{
			Role:           llm.RoleDeveloper,
			MessageType:    llm.MessageTypeGoal,
			Content:        goalStatusPrompt(g),
			CompactContent: goalStatusCompactText(g),
		}, transcriptWorkingDir)
		return []session.EventInput{{Kind: "message", Payload: msg}}, nil
	})
	if err != nil {
		reportErr(err)
		return
	}
	if !transitioned {
		return
	}
	if err := e.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, false, []llm.Message{msg}), steerGoalStatusUpdateIntent(goalStatusUpdateFromState(completed))); err != nil {
		reportErr(err)
	}
}

func goalStatusUpdateFromState(goal session.GoalState) GoalStatusUpdate {
	return GoalStatusUpdate{State: goal}
}

func goalStatusClearUpdate() GoalStatusUpdate {
	return GoalStatusUpdate{Cleared: true}
}

func steerGoalStatusUpdateIntent(update GoalStatusUpdate) steeringIntent {
	return steerEventIntent(Event{Kind: EventGoalStatusUpdated, GoalStatus: &update})
}

func (e *Engine) StartGoalLoop() error {
	return e.startGoalLoop(true)
}

func (e *Engine) deferGoalLoopStart() {
	if e == nil {
		return
	}
	e.activeStepGoalMutationsMu.Lock()
	e.pendingGoalLoopStart = true
	e.activeStepGoalMutationsMu.Unlock()
}

func (e *Engine) startPendingGoalLoop() error {
	if e == nil {
		return nil
	}
	e.activeStepGoalMutationsMu.Lock()
	pending := e.pendingGoalLoopStart
	e.pendingGoalLoopStart = false
	e.activeStepGoalMutationsMu.Unlock()
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
	if e.workflowRunActive() {
		return nil
	}
	if err := e.requireAskQuestionForGoalLoopStart(); err != nil {
		return err
	}
	if !e.goalLoopState().Start() {
		return nil
	}

	e.launchGoalLoopTask(firstTurnAlreadyPrompted)
	return nil
}

func (e *Engine) launchGoalLoopTask(firstTurnAlreadyPrompted bool) {
	launched := e.launchLifecycleTask(func(ctx context.Context) {
		defer e.finishGoalLoop()
		e.runGoalLoop(ctx, firstTurnAlreadyPrompted)
	})
	if !launched {
		e.finishGoalLoop()
	}
}

func (e *Engine) finishGoalLoop() {
	if e.goalLoopState().Finish(e.goalActive()) {
		e.launchGoalLoopTask(true)
	}
}

func (e *Engine) runGoalLoop(ctx context.Context, firstTurnAlreadyPrompted bool) {
	appendNudge := !firstTurnAlreadyPrompted
	for {
		if !e.shouldContinueGoalLoop(ctx) {
			return
		}
		if _, err := e.runGoalTurn(ctx, appendNudge); err != nil {
			if errors.Is(err, errExclusiveStepBusy) {
				if !e.waitBeforeGoalLoopBusyRetry(ctx) {
					return
				}
				continue
			}
			e.recordGoalLoopError(err)
			return
		}
		appendNudge = true
	}
}

func (e *Engine) runGoalTurn(ctx context.Context, appendNudge bool) (assistant llm.Message, err error) {
	e.ensureOrchestrationCollaborators()
	err = e.stepLifecycle.Run(ctx, exclusiveStepOptions{EmitRunState: true, PersistRunLifecycle: true, GoalLoop: true}, func(stepCtx context.Context, stepID string) error {
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

func (e *Engine) recordGoalLoopError(err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, errGoalLoopInactive) {
		return
	}
	message := "Goal loop stopped: " + err.Error()
	if appendErr := e.steer("", steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
		Text:       message,
	})); appendErr != nil {
		_ = e.steer("", steerLocalEntryIntent(storedLocalEntry{
			Visibility: transcript.EntryVisibilityAuto,
			Role:       string(transcript.EntryRoleDeveloperErrorFeedback),
			Text:       "Failed to persist goal loop error: " + appendErr.Error(),
		}))
	}
	e.SetStreamingError(message)
}

func (e *Engine) shouldContinueGoalLoop(ctx context.Context) bool {
	if e == nil {
		return false
	}
	if e.goalLoopState().Suspended() {
		return false
	}
	outcome, err := e.goalContinuation().Evaluate(ctx, llm.Message{})
	if err != nil {
		return false
	}
	return !outcome.Done
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

func (e *Engine) requireAskQuestionForActiveGoal() error {
	goal := e.Goal()
	if goal == nil || goal.Status != session.GoalStatusActive {
		return nil
	}
	return e.requireAskQuestionForGoalLoopStart()
}

func (e *Engine) RequireGoalLoopStartAllowed() error {
	return e.requireAskQuestionForGoalLoopStart()
}

func (e *Engine) requireAskQuestionForGoalActivation() error {
	goal := e.Goal()
	if goal == nil || goal.Status == session.GoalStatusActive {
		return nil
	}
	return e.requireAskQuestionForGoalLoopStart()
}

func (e *Engine) requireAskQuestionForGoalLoopStart() error {
	// Goals require ask_question to be part of the session's locked tool surface,
	// because goal work may need user decisions. The /questions command only
	// toggles whether ask_question calls are answered; it must not block or stop
	// an already-visible goal loop.
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

func goalNudgeCompactText(goal session.GoalState) string {
	return "Continue active goal: " + strconvQuoteForGoalPreview(goal.Objective)
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
