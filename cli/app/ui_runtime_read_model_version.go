package app

import (
	"strings"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type runtimeReadModelVersionDecision uint8

const (
	runtimeReadModelVersionApply runtimeReadModelVersionDecision = iota
	runtimeReadModelVersionIgnore
	runtimeReadModelVersionRefresh
)

func runtimeEventHasReadModelPayload(evt clientui.Event) bool {
	return evt.RuntimeActivity != nil ||
		evt.InputReconciliation != nil
}

func (m *uiModel) runtimeReadModelConflictDiagnosticCmd(evt clientui.Event) tea.Cmd {
	if m == nil || evt.ReadModelVersion != m.runtimeReadModelVersion {
		return nil
	}
	if evt.RuntimeActivity != nil && runtimeActivityConflictsWithProjection(*evt.RuntimeActivity, m) {
		return m.sendTransientStatusWithNoticeID("conflicting runtime activity read-model update ignored", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	return nil
}

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
	if runtimeRunModeFromActivityKind(activity.ActiveKind) != m.runtimeLifecycle.Run.Mode {
		return true
	}
	return strings.TrimSpace(activity.RunID) != strings.TrimSpace(m.currentRunID) ||
		strings.TrimSpace(activity.StepID) != strings.TrimSpace(m.currentStepID)
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
