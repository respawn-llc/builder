package runtime

import (
	"errors"
	"fmt"
	"strings"

	"core/server/llm"
	"core/server/runtimecommand"
	"core/server/session"
	"core/shared/clientui"
	"core/shared/textutil"
)

func (e *Engine) SteerWorktreeTransitionFailure(outcome clientui.WorktreeTransitionOutcome) error {
	return submitWorktreeTransitionFailure(e, outcome, func(intents ...steeringIntent) error {
		return e.steer("", intents...)
	})
}

type WorktreeOutcomeAdmission struct {
	admission runtimeEventAdmission
}

func (e *Engine) AdmitWorktreeOutcome(
	admission runtimecommand.Admission,
) (WorktreeOutcomeAdmission, error) {
	if e == nil || !admission.Owns(e.runtimeEvents) {
		return WorktreeOutcomeAdmission{}, errors.New("Worktree outcome requires this Engine's Runtime Event admission")
	}
	return WorktreeOutcomeAdmission{
		admission: runtimeEventAdmission{engine: e, command: admission},
	}, nil
}

func (a WorktreeOutcomeAdmission) ApplyFailure(
	outcome clientui.WorktreeTransitionOutcome,
) error {
	if a.admission.engine == nil {
		return errors.New("Worktree outcome admission is unavailable")
	}
	return submitWorktreeTransitionFailure(a.admission.engine, outcome, func(intents ...steeringIntent) error {
		return a.admission.applySteering("", intents...)
	})
}

func (a WorktreeOutcomeAdmission) ReduceAfterRelease(
	grant AgentStepReducerGrant,
) error {
	if a.admission.engine == nil {
		return errors.New("Worktree outcome admission is unavailable")
	}
	if grant == nil {
		return nil
	}
	if a.admission.engine.agentSteps.boundary == nil {
		releaseErr := grant.Release()
		if !a.admission.engine.boundaryAgenda.hasEligibleHuman(idleBoundarySelection()) {
			return releaseErr
		}
		return errors.Join(
			releaseErr,
			a.admission.engine.startRuntimeBoundHumanExecution(a.admission),
		)
	}
	_, err := a.admission.engine.resumeReducerBoundaryGrant(a.admission, grant, false)
	return err
}

func submitWorktreeTransitionFailure(
	e *Engine,
	outcome clientui.WorktreeTransitionOutcome,
	apply func(...steeringIntent) error,
) error {
	if e == nil {
		return errors.New("runtime engine is required")
	}
	if err := outcome.Validate(); err != nil {
		return fmt.Errorf("validate worktree transition outcome: %w", err)
	}
	if outcome.State != clientui.WorktreeTransitionFailed {
		return errors.New("failed worktree transition outcome is required")
	}
	diagnostic := strings.TrimSpace(outcome.Failure.Diagnostic)
	return apply(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:        llm.RoleDeveloper,
		MessageType: textutil.Value(llm.MessageTypeErrorFeedback),
		Content: textutil.Value(fmt.Sprintf(
			"Scheduled worktree %s transition %s failed: %s",
			outcome.Transition,
			outcome.OperationID.String(),
			diagnostic,
		)),
	}}))
}

func (e *Engine) materializePendingWorktreeReminder(stepID string) error {
	state := session.CloneWorktreeReminderState(e.store.Meta().WorktreeReminder)
	if state == nil {
		return nil
	}
	metaResult, err := e.activeMetaContextBuilder(e.currentModel(), e.cfg.SkillPolicy).Build(metaContextBuildOptions{WorktreeReminder: state})
	if err != nil {
		return err
	}
	return e.steerMetaContextIfChanged(stepID, steeringPriorityNormal, append(metaResult.Worktree, metaResult.WorktreeExit...))
}
