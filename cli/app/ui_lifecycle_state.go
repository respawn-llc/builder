package app

import (
	"strings"

	"core/shared/clientui"
)

type uiRuntimeLifecycle struct {
	Run clientui.RunLifecycle
}

type uiInterruptLifecycle string

const (
	uiInterruptIdle    uiInterruptLifecycle = "idle"
	uiInterruptPending uiInterruptLifecycle = "pending"
)

func (m *uiModel) isBusy() bool {
	return m != nil && (m.runtimeLifecycle.Run.IsRunning() || m.hasLocalDispatchPending())
}

func (m *uiModel) runtimeActivityBusy() bool {
	return m != nil && m.runtimeLifecycle.Run.IsRunning()
}

func (m *uiModel) runtimeActivityBlocksInput() bool {
	if m == nil {
		return false
	}
	if m.runtimeActivityProjection.State == clientui.RuntimeActivityDraining {
		return false
	}
	return m.runtimeActivityProjection.ActiveForControl()
}

func (m *uiModel) applyRuntimeActivityProjection(activity clientui.RuntimeActivity) error {
	if m == nil {
		return nil
	}
	if err := activity.Validate(); err != nil {
		return err
	}
	m.runtimeActivityProjection = activity
	m.reconcileMissingPromptRecoveryScope()
	if !activity.ActiveForControl() {
		m.runtimeLifecycle.Run = clientui.IdleRunLifecycle()
		m.activity = uiActivityIdle
		m.currentRunID = ""
		m.currentStepID = ""
		return nil
	}
	if activity.State == clientui.RuntimeActivityRunning || activity.State == clientui.RuntimeActivityAwaitingPrompt {
		m.runtimeLifecycle.Run = clientui.MustRunLifecycle(clientui.RunLifecycleRunning, runtimeRunModeFromActivityKind(activity.ActiveStep.ActiveKind))
	} else {
		m.runtimeLifecycle.Run = clientui.IdleRunLifecycle()
	}
	if activity.State == clientui.RuntimeActivityAwaitingPrompt {
		m.activity = uiActivityQuestion
	} else {
		m.activity = uiActivityRunning
	}
	if activity.ActiveStep != nil {
		m.currentRunID = activity.ActiveStep.RunID.String()
		m.currentStepID = activity.ActiveStep.StepID.String()
	} else {
		m.currentRunID = ""
		m.currentStepID = ""
	}
	return nil
}

func runtimeRunModeFromActivityKind(kind clientui.RuntimeActivityActiveKind) clientui.RunMode {
	if kind == clientui.RuntimeActivityActiveKindGoalLoop {
		return clientui.RunModeGoalLoop
	}
	return clientui.RunModeTurn
}

func (m *uiModel) isCompacting() bool {
	if m == nil ||
		m.runtimeActivityProjection.State != clientui.RuntimeActivityRunning ||
		m.runtimeActivityProjection.ActiveStep == nil {
		return false
	}
	switch m.runtimeActivityProjection.ActiveStep.ActiveKind {
	case clientui.RuntimeActivityActiveKindCompaction,
		clientui.RuntimeActivityActiveKindPreSubmitCompaction:
		return true
	default:
		return false
	}
}

func (m *uiModel) isReviewerActive() bool {
	if m == nil {
		return false
	}
	switch m.runtimeActivityProjection.Reviewer {
	case clientui.ReviewerActivityInvoking, clientui.ReviewerActivityAddressingFeedback:
		return true
	default:
		return false
	}
}

func (m *uiModel) hasPendingInterrupt() bool {
	return m != nil && m.interruptLifecycle == uiInterruptPending
}

func (m *uiModel) hasLocalDispatchPending() bool {
	return m != nil && m.activeSubmit.token != 0
}

func (m *uiModel) blocksRuntimeInput() bool {
	return m != nil && (m.finalAnswerOperation != nil || m.isBusy() || m.runtimeActivityBlocksInput() || m.hasLocalDispatchPending())
}

func (m *uiModel) setPendingInterrupt(pending bool) {
	if m == nil {
		return
	}
	if pending {
		m.interruptLifecycle = uiInterruptPending
		m.interruptRunID = strings.TrimSpace(m.currentRunID)
		m.interruptStepID = strings.TrimSpace(m.currentStepID)
		m.completedRunID = ""
		m.completedStepID = ""
		return
	}
	if strings.TrimSpace(m.interruptRunID) != "" && strings.TrimSpace(m.interruptStepID) != "" {
		m.completedRunID = strings.TrimSpace(m.interruptRunID)
		m.completedStepID = strings.TrimSpace(m.interruptStepID)
	}
	m.interruptLifecycle = uiInterruptIdle
	m.interruptRunID = ""
	m.interruptStepID = ""
}
