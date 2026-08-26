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

type activeStepGoalMutationKind uint8

const (
	activeStepGoalMutationSet activeStepGoalMutationKind = iota
	activeStepGoalMutationStatus
	activeStepGoalMutationClear
	activeStepGoalMutationRestartGoalLoop
)

type activeStepGoalMutation struct {
	kind      activeStepGoalMutationKind
	objective string
	actor     session.GoalActor
	status    session.GoalStatus
}

type goalStepExpectation struct {
	exactStep *runtimeids.StepID
}

func anyActiveGoalStep() goalStepExpectation {
	return goalStepExpectation{}
}

func exactGoalStep(stepID string) (goalStepExpectation, error) {
	identity, err := runtimeids.ParseStepID(stepID)
	if err != nil {
		return goalStepExpectation{}, err
	}
	return goalStepExpectation{exactStep: &identity}, nil
}

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
	result := goalCommandResult(disposition, goal, cleared, session.CommitReceipt{}, session.CommitReceipt{})
	result.Availability = e.GoalMutationAvailability()
	return currentGoalHandled(result, nil)
}

func (e *Engine) ApplyCurrentGoalOperation(operation CurrentGoalOperation, ownership CurrentGoalExecutionOwnership) (CurrentGoalOperationOutcome, error) {
	if ownership != CurrentGoalRetainedOnly && ownership != CurrentGoalExactExecution {
		return CurrentGoalOperationOutcome{}, errors.New("current Goal execution ownership is required")
	}
	switch operation := operation.(type) {
	case CurrentGoalSet:
		goal, queued, err := e.QueueGoalSetForActiveStep(operation.Objective, operation.Actor)
		if err != nil {
			return CurrentGoalOperationOutcome{}, err
		}
		if queued {
			return e.currentGoalDisposition(GoalCommandQueued, goal, false)
		}
		if ownership == CurrentGoalExactExecution {
			result, err := e.SetGoal(operation.Objective, operation.Actor)
			if err == nil && result.MetadataReceipt.Committed {
				err = e.StartGoalLoop()
			}
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
		goal, queued, err := e.QueueGoalStatusForActiveStep(status, operation.Actor)
		if err != nil {
			return CurrentGoalOperationOutcome{}, err
		}
		if queued {
			return e.currentGoalDisposition(GoalCommandQueued, goal, false)
		}
		if ownership == CurrentGoalExactExecution {
			result, err := e.SetGoalStatusWithoutGoalLoopStart(status, operation.Actor)
			if err == nil && status == session.GoalStatusActive && result.MetadataReceipt.Committed {
				err = e.StartGoalLoop()
			}
			return currentGoalHandled(result, err)
		}
		if status == session.GoalStatusActive {
			return currentGoalExecutionRequired()
		}
		return currentGoalHandled(e.SetGoalStatusWithoutGoalLoopStart(status, operation.Actor))
	case CurrentGoalClear:
		goal, queued, err := e.QueueGoalClearForActiveStep(operation.Actor)
		if err != nil {
			return CurrentGoalOperationOutcome{}, err
		}
		if queued {
			return e.currentGoalDisposition(GoalCommandQueued, goal, true)
		}
		return currentGoalHandled(e.ClearGoal(operation.Actor))
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

func (e *Engine) goalLoopRestartNeeded() bool {
	if e == nil {
		return false
	}
	return e.goalLoopState().RestartNeeded() && e.goalActive()
}

func (e *Engine) SetGoal(objective string, actor session.GoalActor) (GoalCommandResult, error) {
	return awaitEngineRuntimeOperation(
		context.Background(),
		e,
		func(context.Context) (GoalCommandResult, error) {
			return e.setGoalRaw(sessionSteeringProvenance(), objective, actor)
		},
	)
}

func (e *Engine) ValidateGoalSet(objective string, actor session.GoalActor) error {
	if e == nil || e.store == nil {
		return fmt.Errorf("runtime engine is required")
	}
	return e.store.ValidateGoalSet(objective, actor)
}

func (e *Engine) setGoalForStep(stepID string, objective string, actor session.GoalActor) (GoalCommandResult, error) {
	provenance, err := exactSteeringProvenance(stepID)
	if err != nil {
		return GoalCommandResult{}, err
	}
	return e.setGoalRaw(provenance, objective, actor)
}

func (e *Engine) setGoalRaw(provenance steeringProvenance, objective string, actor session.GoalActor) (GoalCommandResult, error) {
	if e == nil || e.store == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	objective = strings.TrimSpace(objective)
	availability := e.GoalMutationAvailability()
	goal, metadataReceipt, err := e.store.SetGoal(objective, actor)
	result := goalCommandResult(GoalCommandApplied, goal, false, metadataReceipt, session.CommitReceipt{})
	result.Availability = availability
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeSet, &goal)
	if err != nil {
		return result, err
	}
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	noticeReceipt, err := e.steerGoalNoticeAndStatusRaw(provenance, msg, goalStatusUpdateFromState(goal, availability))
	result.NoticeReceipt = noticeReceipt
	return result, err
}

func (e *Engine) SetGoalStatus(status session.GoalStatus, actor session.GoalActor) (GoalCommandResult, error) {
	return e.setGoalStatusRuntime(status, actor, true)
}

func (e *Engine) setGoalStatusForStep(stepID string, status session.GoalStatus, actor session.GoalActor) (GoalCommandResult, error) {
	return e.setGoalStatusForStepWithGoalLoopAdmission(stepID, status, actor, true)
}

func (e *Engine) SetGoalStatusWithoutGoalLoopStart(status session.GoalStatus, actor session.GoalActor) (GoalCommandResult, error) {
	return e.setGoalStatusRuntime(status, actor, false)
}

func (e *Engine) setGoalStatusForStepWithGoalLoopAdmission(stepID string, status session.GoalStatus, actor session.GoalActor, requireGoalLoopStart bool) (GoalCommandResult, error) {
	provenance, err := exactSteeringProvenance(stepID)
	if err != nil {
		return GoalCommandResult{}, err
	}
	return e.setGoalStatusRaw(provenance, status, actor, requireGoalLoopStart)
}

func (e *Engine) setGoalStatusRuntime(status session.GoalStatus, actor session.GoalActor, requireGoalLoopStart bool) (GoalCommandResult, error) {
	return awaitEngineRuntimeOperation(
		context.Background(),
		e,
		func(context.Context) (GoalCommandResult, error) {
			return e.setGoalStatusRaw(sessionSteeringProvenance(), status, actor, requireGoalLoopStart)
		},
	)
}

func (e *Engine) setGoalStatusRaw(provenance steeringProvenance, status session.GoalStatus, actor session.GoalActor, requireGoalLoopStart bool) (GoalCommandResult, error) {
	if e == nil || e.store == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	availability := e.GoalMutationAvailability()
	if current := e.Goal(); current != nil && current.Status == status {
		if status != session.GoalStatusActive || e.GoalLoopContinuationEnforced() {
			result := goalCommandResult(GoalCommandNoop, *current, false, session.CommitReceipt{}, session.CommitReceipt{})
			result.Availability = availability
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
	result.Availability = availability
	if err != nil || !transitioned || !metadataReceipt.Committed {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeStatus, &goal)
	if err != nil {
		return result, err
	}
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	noticeReceipt, err := e.steerGoalNoticeAndStatusRaw(provenance, msg, goalStatusUpdateFromState(goal, availability))
	result.NoticeReceipt = noticeReceipt
	return result, err
}

func (e *Engine) QueueAgentShellSetGoal(objective string, actor session.GoalActor) (session.GoalState, bool, error) {
	return e.QueueGoalSetForActiveStep(objective, actor)
}

func (e *Engine) QueueAgentShellSetGoalForStep(stepID string, objective string, actor session.GoalActor) (session.GoalState, bool, error) {
	expectation, err := exactGoalStep(stepID)
	if err != nil {
		return session.GoalState{}, false, err
	}
	return e.queueGoalSetForStep(expectation, objective, actor)
}

func (e *Engine) QueueGoalSetForActiveStep(objective string, actor session.GoalActor) (session.GoalState, bool, error) {
	return e.queueGoalSetForStep(anyActiveGoalStep(), objective, actor)
}

func (e *Engine) queueGoalSetForStep(expectation goalStepExpectation, objective string, actor session.GoalActor) (session.GoalState, bool, error) {
	if e == nil || e.store == nil {
		return session.GoalState{}, false, fmt.Errorf("runtime engine is required")
	}
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return session.GoalState{}, false, errors.New("goal objective is required")
	}
	accepted, queued, err := e.enqueueActiveStepGoalMutationForStep(expectation, activeStepGoalMutation{
		kind:      activeStepGoalMutationSet,
		objective: objective,
		actor:     actor,
	}, func(current *session.GoalState) (session.GoalState, error) {
		if actor == session.GoalActorAgent && current != nil && current.Status != session.GoalStatusComplete {
			return session.GoalState{}, session.GoalAgentOverwriteBlockedError{Goal: *current}
		}
		if !e.currentNodeExecutionActive() {
			if err := e.RequireGoalLoopStartAllowed(); err != nil {
				return session.GoalState{}, err
			}
		}
		now := time.Now().UTC()
		return session.GoalState{Objective: objective, Status: session.GoalStatusActive, CreatedAt: now, UpdatedAt: now}, nil
	})
	if err != nil || !queued {
		return session.GoalState{}, queued, err
	}
	return accepted, true, nil
}

func (e *Engine) QueueAgentShellCompleteGoal(actor session.GoalActor) (session.GoalState, bool, error) {
	return e.QueueGoalStatusForActiveStep(session.GoalStatusComplete, actor)
}

func (e *Engine) QueueAgentShellCompleteGoalForStep(stepID string, actor session.GoalActor) (session.GoalState, bool, error) {
	return e.QueueGoalStatusForStep(stepID, session.GoalStatusComplete, actor)
}

func (e *Engine) QueueGoalStatusForActiveStep(status session.GoalStatus, actor session.GoalActor) (session.GoalState, bool, error) {
	return e.queueGoalStatusForStep(anyActiveGoalStep(), status, actor)
}

func (e *Engine) QueueGoalStatusForStep(stepID string, status session.GoalStatus, actor session.GoalActor) (session.GoalState, bool, error) {
	expectation, err := exactGoalStep(stepID)
	if err != nil {
		return session.GoalState{}, false, err
	}
	return e.queueGoalStatusForStep(expectation, status, actor)
}

func (e *Engine) queueGoalStatusForStep(expectation goalStepExpectation, status session.GoalStatus, actor session.GoalActor) (session.GoalState, bool, error) {
	if e == nil || e.store == nil {
		return session.GoalState{}, false, fmt.Errorf("runtime engine is required")
	}
	if status == session.GoalStatusActive && !e.currentNodeExecutionActive() {
		if err := e.RequireGoalLoopStartAllowed(); err != nil {
			return session.GoalState{}, false, err
		}
	}
	mutation := activeStepGoalMutation{
		kind:   activeStepGoalMutationStatus,
		actor:  actor,
		status: status,
	}
	accepted, queued, err := e.enqueueActiveStepGoalMutationForStep(expectation, mutation, func(current *session.GoalState) (session.GoalState, error) {
		if current == nil {
			return session.GoalState{}, errors.New("goal is not set")
		}
		accepted := *current
		accepted.Status = status
		accepted.UpdatedAt = time.Now().UTC()
		return accepted, nil
	})
	if err != nil || !queued {
		return session.GoalState{}, queued, err
	}
	return accepted, true, nil
}

func (e *Engine) QueueGoalClearForActiveStep(actor session.GoalActor) (session.GoalState, bool, error) {
	if e == nil || e.store == nil {
		return session.GoalState{}, false, fmt.Errorf("runtime engine is required")
	}
	accepted, queued, err := e.enqueueActiveStepGoalMutation(activeStepGoalMutation{
		kind:  activeStepGoalMutationClear,
		actor: actor,
	}, func(current *session.GoalState) (session.GoalState, error) {
		if current == nil {
			return session.GoalState{}, errors.New("goal is not set")
		}
		return *current, nil
	})
	if err != nil || !queued {
		return session.GoalState{}, queued, err
	}
	return accepted, true, nil
}

func foldGoalMutations(goal *session.GoalState, mutations []activeStepGoalMutation) *session.GoalState {
	for _, mutation := range mutations {
		switch mutation.kind {
		case activeStepGoalMutationSet:
			now := time.Now().UTC()
			next := session.GoalState{Objective: mutation.objective, Status: session.GoalStatusActive, CreatedAt: now, UpdatedAt: now}
			goal = &next
		case activeStepGoalMutationStatus:
			if goal != nil {
				next := *goal
				next.Status = mutation.status
				next.UpdatedAt = time.Now().UTC()
				goal = &next
			}
		case activeStepGoalMutationClear:
			goal = nil
		case activeStepGoalMutationRestartGoalLoop:
		}
	}
	return goal
}

func (e *Engine) enqueueActiveStepGoalMutation(mutation activeStepGoalMutation, preview func(*session.GoalState) (session.GoalState, error)) (session.GoalState, bool, error) {
	return e.enqueueActiveStepGoalMutationForStep(anyActiveGoalStep(), mutation, preview)
}

func (e *Engine) enqueueActiveStepGoalMutationForStep(expectation goalStepExpectation, mutation activeStepGoalMutation, preview func(*session.GoalState) (session.GoalState, error)) (session.GoalState, bool, error) {
	if e == nil || e.stepLifecycle == nil {
		return session.GoalState{}, false, nil
	}
	var accepted session.GoalState
	queued, err := e.stepLifecycle.WithActiveStep(func(stepID string) error {
		stepID = strings.TrimSpace(stepID)
		if stepID == "" {
			return errors.New("active Step identity is required")
		}
		if expectation.exactStep != nil && stepID != expectation.exactStep.String() {
			return ErrAgentGoalStepInactive
		}
		e.activeStepGoalMutationsMu.Lock()
		defer e.activeStepGoalMutationsMu.Unlock()
		current := foldGoalMutations(e.Goal(), e.activeStepGoalMutations[stepID])
		next, err := preview(current)
		if err != nil {
			return err
		}
		accepted = next
		if mutation.kind == activeStepGoalMutationStatus && current != nil && current.Status == mutation.status {
			if mutation.status != session.GoalStatusActive || !e.goalLoopRestartNeeded() {
				return nil
			}
			mutation.kind = activeStepGoalMutationRestartGoalLoop
		}
		if e.activeStepGoalMutations == nil {
			e.activeStepGoalMutations = make(map[string][]activeStepGoalMutation)
		}
		e.activeStepGoalMutations[stepID] = append(e.activeStepGoalMutations[stepID], mutation)
		return nil
	})
	if err != nil {
		return session.GoalState{}, false, err
	}
	if expectation.exactStep != nil && !queued {
		return session.GoalState{}, false, ErrAgentGoalStepInactive
	}
	return accepted, queued, err
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
		if !e.currentNodeExecutionActive() {
			e.deferGoalLoopStart()
		}
		return nil
	case activeStepGoalMutationStatus:
		if _, err := e.setGoalStatusForStepWithGoalLoopAdmission(stepID, mutation.status, mutation.actor, !e.currentNodeExecutionActive()); err != nil {
			return err
		}
		if mutation.status == session.GoalStatusActive && !e.currentNodeExecutionActive() {
			e.deferGoalLoopStart()
		}
		return nil
	case activeStepGoalMutationClear:
		_, err := e.clearGoalForStep(stepID, mutation.actor)
		return err
	case activeStepGoalMutationRestartGoalLoop:
		if _, err := e.setGoalStatusForStepWithGoalLoopAdmission(stepID, session.GoalStatusActive, mutation.actor, !e.currentNodeExecutionActive()); err != nil {
			return err
		}
		e.deferGoalLoopStart()
		return nil
	default:
		return fmt.Errorf("unsupported active-step goal mutation kind %d", mutation.kind)
	}
}

func (e *Engine) ClearGoal(actor session.GoalActor) (GoalCommandResult, error) {
	return awaitEngineRuntimeOperation(
		context.Background(),
		e,
		func(context.Context) (GoalCommandResult, error) {
			return e.clearGoalRaw(sessionSteeringProvenance(), actor)
		},
	)
}

func (e *Engine) clearGoalForStep(stepID string, actor session.GoalActor) (GoalCommandResult, error) {
	provenance, err := exactSteeringProvenance(stepID)
	if err != nil {
		return GoalCommandResult{}, err
	}
	return e.clearGoalRaw(provenance, actor)
}

func (e *Engine) clearGoalRaw(provenance steeringProvenance, actor session.GoalActor) (GoalCommandResult, error) {
	if e == nil || e.store == nil {
		return GoalCommandResult{}, fmt.Errorf("runtime engine is required")
	}
	availability := e.GoalMutationAvailability()
	goal, metadataReceipt, err := e.store.ClearGoal(actor)
	result := goalCommandResult(GoalCommandApplied, goal, true, metadataReceipt, session.CommitReceipt{})
	result.Availability = availability
	if !metadataReceipt.Committed || err != nil {
		return result, err
	}
	msg, err := goalNoticeMessage(GoalNoticeClear, nil)
	if err != nil {
		return result, err
	}
	msg = normalizeMessageForTranscript(msg, e.transcriptWorkingDir())
	noticeReceipt, err := e.steerGoalNoticeAndStatusRaw(provenance, msg, goalStatusClearUpdate(availability))
	result.NoticeReceipt = noticeReceipt
	return result, err
}

func (e *Engine) cascadeCompleteActiveGoalOnWorkflowCompletion(stepID string) {
	if e == nil || e.store == nil {
		return
	}
	provenance, provenanceErr := exactSteeringProvenance(stepID)
	if provenanceErr != nil {
		panic("workflow completion Goal cascade requires exact Step identity")
	}
	if !e.WorkflowTerminalState().Completed {
		return
	}
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
	availability := e.GoalMutationAvailability()
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
	if _, err := e.steerGoalNoticeAndStatusRaw(provenance, msg, goalStatusUpdateFromState(completed, availability)); err != nil {
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
	e.activeStepGoalMutationsMu.Lock()
	e.pendingGoalLoopStart = true
	e.activeStepGoalMutationsMu.Unlock()
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

func (e *Engine) surfaceRunErrorRaw(err error) {
	e.surfaceRunErrorWith(
		err,
		func(intent steeringIntent) error {
			return e.steerOrderedRaw(sessionSteeringProvenance(), intent)
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
