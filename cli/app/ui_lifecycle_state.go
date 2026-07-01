package app

import (
	"core/cli/app/internal/runtimestate"
	"core/shared/clientui"
)

type uiInterruptLifecycle string

const (
	uiInterruptIdle    uiInterruptLifecycle = "idle"
	uiInterruptPending uiInterruptLifecycle = "pending"
)

func (m *uiModel) isBusy() bool {
	return m != nil && m.runtimeLifecycle.Run.IsRunning()
}

func (m *uiModel) setBusy(busy bool) {
	if m == nil {
		return
	}
	if !busy {
		m.runtimeLifecycle.Run = clientui.IdleRunLifecycle()
		return
	}
	mode := clientui.RunModeTurn
	if m.isGoalRun() {
		mode = clientui.RunModeGoalLoop
	}
	m.runtimeLifecycle.Run = clientui.MustRunLifecycle(clientui.RunLifecycleRunning, mode)
}

func (m *uiModel) isGoalRun() bool {
	return m != nil && m.runtimeLifecycle.Run.IsGoalLoopRunning()
}

func (m *uiModel) setGoalRun(goalRun bool) {
	if m == nil {
		return
	}
	if !goalRun {
		if m.isBusy() {
			m.runtimeLifecycle.Run = clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)
		}
		return
	}
	m.runtimeLifecycle.Run = clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeGoalLoop)
}

func (m *uiModel) setRunLifecycle(lifecycle clientui.RunLifecycle) error {
	if m == nil {
		return nil
	}
	if err := lifecycle.Validate(); err != nil {
		return err
	}
	m.runtimeLifecycle.Run = lifecycle
	return nil
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

func (m *uiModel) isInputSubmitLocked() bool {
	return m != nil && m.inputSubmission == runtimestate.InputSubmissionLocked
}

func (m *uiModel) setInputSubmitLocked(locked bool) {
	if m == nil {
		return
	}
	if locked {
		m.inputSubmission = runtimestate.InputSubmissionLocked
		return
	}
	m.inputSubmission = runtimestate.InputSubmissionUnlocked
}

func (m *uiModel) hasPendingInterrupt() bool {
	return m != nil && m.interruptLifecycle == uiInterruptPending
}

func (m *uiModel) setPendingInterrupt(pending bool) {
	if m == nil {
		return
	}
	if pending {
		m.interruptLifecycle = uiInterruptPending
		return
	}
	m.interruptLifecycle = uiInterruptIdle
}
