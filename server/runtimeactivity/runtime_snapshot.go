package runtimeactivity

import (
	"fmt"

	"core/server/runtime"
	"core/shared/clientui"
)

func ActiveStepFromRuntimeSnapshot(snapshot *runtime.RunSnapshot) *ActiveStepSnapshot {
	if snapshot == nil {
		return nil
	}
	return &ActiveStepSnapshot{
		RunID:      snapshot.RunID,
		StepID:     snapshot.StepID,
		ActiveKind: MustClientActiveKindFromRuntime(snapshot.ActiveKind),
	}
}

type ActiveStepSnapshotProvider interface {
	ActiveStepSnapshot() *runtime.RunSnapshot
}

func ActiveStepFromProvider(provider ActiveStepSnapshotProvider) *ActiveStepSnapshot {
	if provider == nil {
		return nil
	}
	return ActiveStepFromRuntimeSnapshot(provider.ActiveStepSnapshot())
}

func ClientActiveKindFromRuntime(kind runtime.ActiveKind) (clientui.RuntimeActivityActiveKind, error) {
	switch kind {
	case runtime.ActiveKindUserTurn:
		return clientui.RuntimeActivityActiveKindUserTurn, nil
	case runtime.ActiveKindGoalLoop:
		return clientui.RuntimeActivityActiveKindGoalLoop, nil
	case runtime.ActiveKindWorkflowTurn:
		return clientui.RuntimeActivityActiveKindWorkflowTurn, nil
	case runtime.ActiveKindCompaction:
		return clientui.RuntimeActivityActiveKindCompaction, nil
	case runtime.ActiveKindPreSubmitCompaction:
		return clientui.RuntimeActivityActiveKindPreSubmitCompaction, nil
	case runtime.ActiveKindUserShell:
		return clientui.RuntimeActivityActiveKindUserShell, nil
	case runtime.ActiveKindBackground:
		return clientui.RuntimeActivityActiveKindBackground, nil
	case runtime.ActiveKindRuntimeMaintenance:
		return clientui.RuntimeActivityActiveKindRuntimeMaintenance, nil
	default:
		return "", fmt.Errorf("unmapped runtime active kind %q", kind)
	}
}

func MustClientActiveKindFromRuntime(kind runtime.ActiveKind) clientui.RuntimeActivityActiveKind {
	mapped, err := ClientActiveKindFromRuntime(kind)
	if err != nil {
		panic(err)
	}
	return mapped
}
