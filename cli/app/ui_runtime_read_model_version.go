package app

import (
	"strings"

	"core/shared/clientui"
)

type runtimeReadModelVersionDecision uint8

const (
	runtimeReadModelVersionApply runtimeReadModelVersionDecision = iota
	runtimeReadModelVersionIgnore
	runtimeReadModelVersionRefresh
)

func runtimeActivityConflictsWithProjection(activity clientui.RuntimeActivity, m *uiModel) bool {
	if m == nil {
		return false
	}
	if m.runtimeActivityProjection.State != "" {
		return activity != m.runtimeActivityProjection
	}
	if activity.ActiveForControl() != m.runtimeActivityBusy() {
		return true
	}
	if !activity.ActiveForControl() {
		return false
	}
	if activity.ActiveStep == nil {
		return strings.TrimSpace(m.currentRunID) != "" || strings.TrimSpace(m.currentStepID) != ""
	}
	if runtimeRunModeFromActivityKind(activity.ActiveStep.ActiveKind) != m.runtimeLifecycle.Run.Mode {
		return true
	}
	return activity.ActiveStep.RunID.String() != strings.TrimSpace(m.currentRunID) ||
		activity.ActiveStep.StepID.String() != strings.TrimSpace(m.currentStepID)
}

func (m *uiModel) acceptRuntimeReadModelVersion(version clientui.ReadModelVersion, hydration bool) runtimeReadModelVersionDecision {
	if m == nil || version.Validate() != nil {
		return runtimeReadModelVersionApply
	}
	current := m.runtimeReadModelVersion
	if current.Validate() != nil {
		m.runtimeReadModelVersion = version
		return runtimeReadModelVersionApply
	}
	if version.Epoch != current.Epoch {
		if hydration {
			m.runtimeReadModelVersion = version
			return runtimeReadModelVersionApply
		}
		return runtimeReadModelVersionRefresh
	}
	if version.Generation != current.Generation {
		if version.Generation < current.Generation {
			return runtimeReadModelVersionIgnore
		}
		if hydration {
			m.runtimeReadModelVersion = version
			return runtimeReadModelVersionApply
		}
		return runtimeReadModelVersionRefresh
	}
	if version.Sequence <= current.Sequence {
		return runtimeReadModelVersionIgnore
	}
	m.runtimeReadModelVersion = version
	return runtimeReadModelVersionApply
}

func runtimeReadModelResetMainViewRefreshRequest() runtimeMainViewRefreshRequest {
	return runtimeMainViewRefreshRequest{
		cause:    runtimeMainViewRefreshCauseManual,
		class:    runtimeSyncPolicyClassAllowed,
		priority: 100,
	}
}
