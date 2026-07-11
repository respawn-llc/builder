package app

import (
	"strings"

	"core/shared/clientui"
)

type uiInterruptLifecycle string

const (
	uiInterruptIdle    uiInterruptLifecycle = "idle"
	uiInterruptPending uiInterruptLifecycle = "pending"
)

func (m *uiModel) isBusy() bool {
	return m != nil && (m.runtimeLifecycle.Run.IsRunning() || m.hasLocalDispatchPending() || len(m.pendingRuntimeOperations) > 0)
}

func (m *uiModel) runtimeActivityBusy() bool {
	return m != nil && m.runtimeLifecycle.Run.IsRunning()
}

func (m *uiModel) runtimeActivityBlocksControl() bool {
	return m != nil && m.runtimeActivityProjection.ActiveForControl()
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
	if !activity.ActiveForControl() {
		m.runtimeLifecycle.Run = clientui.IdleRunLifecycle()
		m.activity = uiActivityIdle
		m.currentRunID = ""
		m.currentStepID = ""
		return nil
	}
	if activity.State == clientui.RuntimeActivityRunning || activity.State == clientui.RuntimeActivityAwaitingPrompt {
		m.runtimeLifecycle.Run = clientui.MustRunLifecycle(clientui.RunLifecycleRunning, runtimeRunModeFromActivityKind(activity.ActiveKind))
	} else {
		m.runtimeLifecycle.Run = clientui.IdleRunLifecycle()
	}
	if activity.State == clientui.RuntimeActivityAwaitingPrompt {
		m.activity = uiActivityQuestion
	} else {
		m.activity = uiActivityRunning
	}
	m.currentRunID = strings.TrimSpace(activity.RunID)
	m.currentStepID = strings.TrimSpace(activity.StepID)
	m.bindPreActiveInterruptToken()
	return nil
}

func (m *uiModel) isGoalRun() bool {
	return m != nil && m.runtimeLifecycle.Run.IsGoalLoopRunning()
}

func runtimeRunModeFromActivityKind(kind clientui.RuntimeActivityActiveKind) clientui.RunMode {
	if kind == clientui.RuntimeActivityActiveKindGoalLoop {
		return clientui.RunModeGoalLoop
	}
	return clientui.RunModeTurn
}

func (m *uiModel) isCompacting() bool {
	return m != nil && m.runtimeLifecycle.Compaction.IsRunning()
}

func (m *uiModel) setCompacting(compacting bool) {
	if m == nil {
		return
	}
	m.runtimeLifecycle.Compaction = clientui.NewCompactionLifecycle(compacting)
}

func (m *uiModel) isReviewerRunning() bool {
	return m != nil && m.runtimeLifecycle.Reviewer.IsRunning()
}

func (m *uiModel) setReviewerRunning(running bool) {
	if m == nil {
		return
	}
	blocking := running && m.isReviewerBlocking()
	reviewer, err := clientui.NewReviewerLifecycle(running, blocking)
	if err != nil {
		panic(err)
	}
	m.runtimeLifecycle.Reviewer = reviewer
}

func (m *uiModel) isReviewerBlocking() bool {
	return m != nil && m.runtimeLifecycle.Reviewer.IsBlocking()
}

func (m *uiModel) setReviewerBlocking(blocking bool) {
	if m == nil {
		return
	}
	running := m.isReviewerRunning() || blocking
	reviewer, err := clientui.NewReviewerLifecycle(running, blocking)
	if err != nil {
		panic(err)
	}
	m.runtimeLifecycle.Reviewer = reviewer
}

func (m *uiModel) hasPendingInterrupt() bool {
	return m != nil && m.interruptLifecycle == uiInterruptPending
}

func (m *uiModel) hasLocalDispatchPending() bool {
	return m != nil && m.activeSubmit.token != 0
}

func (m *uiModel) blocksRuntimeInput() bool {
	return m != nil && (m.isBusy() || m.runtimeActivityBlocksInput() || m.hasLocalDispatchPending())
}

func (m *uiModel) setPendingInterrupt(pending bool) {
	if m == nil {
		return
	}
	if pending {
		m.interruptLifecycle = uiInterruptPending
		m.interruptRunID = strings.TrimSpace(m.currentRunID)
		m.interruptStepID = strings.TrimSpace(m.currentStepID)
		m.interruptPreActive = false
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
	m.interruptPreActive = false
}

func (m *uiModel) trackRuntimeActivityToken(evt clientui.Event) {
	if m == nil {
		return
	}
	switch evt.Kind {
	case clientui.EventRuntimeActivityChanged:
		if evt.RuntimeActivity == nil || !evt.RuntimeActivity.ActiveForControl() {
			m.currentRunID = ""
			m.currentStepID = ""
			return
		}
		m.currentRunID = strings.TrimSpace(evt.RuntimeActivity.RunID)
		m.currentStepID = strings.TrimSpace(evt.RuntimeActivity.StepID)
		m.bindPreActiveInterruptToken()
	}
}

func (m *uiModel) bindPreActiveInterruptToken() {
	if m == nil || !m.hasPendingInterrupt() || !m.interruptPreActive {
		return
	}
	if strings.TrimSpace(m.interruptRunID) != "" || strings.TrimSpace(m.interruptStepID) != "" {
		m.interruptPreActive = false
		return
	}
	if strings.TrimSpace(m.currentRunID) == "" || strings.TrimSpace(m.currentStepID) == "" {
		return
	}
	m.interruptRunID = strings.TrimSpace(m.currentRunID)
	m.interruptStepID = strings.TrimSpace(m.currentStepID)
	m.interruptPreActive = false
}
