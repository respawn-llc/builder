package runtime

import (
	"fmt"
	"strings"
)

type RunLifecyclePhase string

const (
	RunLifecycleIdle     RunLifecyclePhase = "idle"
	RunLifecycleRunning  RunLifecyclePhase = "running"
	RunLifecycleFinished RunLifecyclePhase = "finished"
)

type RunMode string

const (
	RunModeTurn     RunMode = "turn"
	RunModeGoalLoop RunMode = "goal_loop"
)

type RunLifecycle struct {
	Phase RunLifecyclePhase
	Mode  RunMode
}

func NewRunLifecycle(phase RunLifecyclePhase, mode RunMode) (RunLifecycle, error) {
	phase = RunLifecyclePhase(strings.TrimSpace(string(phase)))
	mode = RunMode(strings.TrimSpace(string(mode)))
	if phase == "" {
		phase = RunLifecycleIdle
	}
	switch phase {
	case RunLifecycleIdle:
		if mode != "" {
			return RunLifecycle{}, fmt.Errorf("idle run lifecycle cannot carry run mode %q", mode)
		}
		return RunLifecycle{Phase: RunLifecycleIdle}, nil
	case RunLifecycleRunning, RunLifecycleFinished:
		if mode == "" {
			mode = RunModeTurn
		}
		switch mode {
		case RunModeTurn, RunModeGoalLoop:
			return RunLifecycle{Phase: phase, Mode: mode}, nil
		default:
			return RunLifecycle{}, fmt.Errorf("unsupported run mode %q", mode)
		}
	default:
		return RunLifecycle{}, fmt.Errorf("unsupported run lifecycle phase %q", phase)
	}
}

func MustRunLifecycle(phase RunLifecyclePhase, mode RunMode) RunLifecycle {
	lifecycle, err := NewRunLifecycle(phase, mode)
	if err != nil {
		panic(err)
	}
	return lifecycle
}

func IdleRunLifecycle() RunLifecycle {
	return RunLifecycle{Phase: RunLifecycleIdle}
}

func RunningRunLifecycle(mode RunMode) RunLifecycle {
	return MustRunLifecycle(RunLifecycleRunning, mode)
}

func FinishedRunLifecycle(mode RunMode) RunLifecycle {
	return MustRunLifecycle(RunLifecycleFinished, mode)
}

func (s RunLifecycle) Validate() error {
	_, err := NewRunLifecycle(s.Phase, s.Mode)
	return err
}

func (s RunLifecycle) IsRunning() bool {
	return s.Phase == RunLifecycleRunning
}
