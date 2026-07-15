package app

import tea "github.com/charmbracelet/bubbletea"

type terminalGeometry struct {
	size *terminalSize
}

type terminalSize struct {
	width  int
	height int
}

func terminalGeometryUnknown() terminalGeometry {
	return terminalGeometry{}
}

func terminalGeometryKnown(width, height int) terminalGeometry {
	if width < 1 || height < 1 {
		panic("known terminal geometry requires positive dimensions")
	}
	return terminalGeometry{size: &terminalSize{width: width, height: height}}
}

func (g terminalGeometry) IsKnown() bool {
	return g.size != nil
}

func (g terminalGeometry) Size() *terminalSize {
	if g.size == nil {
		return nil
	}
	size := *g.size
	return &size
}

type terminalDestination string

const (
	terminalDestinationOngoing  terminalDestination = "ongoing"
	terminalDestinationDetail   terminalDestination = "detail"
	terminalDestinationStatus   terminalDestination = "status"
	terminalDestinationRollback terminalDestination = "rollback"
)

func desiredOngoingOwnership(geometry terminalGeometry, destination terminalDestination) bool {
	size := geometry.Size()
	return size != nil &&
		size.width >= 40 &&
		size.height >= 10 &&
		destination == terminalDestinationOngoing
}

type ownershipAppliedState struct {
	owned bool
}

type ownershipReconcileResult struct {
	Changed      bool
	DesiredOwned bool
	Err          error
}

type ongoingOwnershipReconciler struct {
	apply   func(bool) error
	applied *ownershipAppliedState
}

type ongoingOwnershipReconcileMsg struct{}

func terminalDestinationForSurface(surface uiSurface) terminalDestination {
	switch surface {
	case uiSurfaceOngoingTranscript:
		return terminalDestinationOngoing
	case uiSurfaceTranscriptDetail:
		return terminalDestinationDetail
	case uiSurfaceStatus:
		return terminalDestinationStatus
	case uiSurfaceRollbackSelection:
		return terminalDestinationRollback
	default:
		return terminalDestinationStatus
	}
}

func (m *uiModel) ensureOwnershipReconciler() *ongoingOwnershipReconciler {
	if m == nil {
		return nil
	}
	if m.ownershipReconciler == nil {
		m.ownershipReconciler = newOngoingOwnershipReconciler(func(owned bool) error {
			if m.ongoingTranscript == nil {
				return nil
			}
			m.pendingOwnershipCmd = m.setOngoingNormalBufferOwned(owned)
			return nil
		})
		m.ownershipReconciler.applied = &ownershipAppliedState{owned: true}
	}
	return m.ownershipReconciler
}

func (m *uiModel) reconcileOngoingOwnership() tea.Cmd {
	if m == nil || m.ongoingTranscript == nil {
		return nil
	}
	reconciler := m.ensureOwnershipReconciler()
	reconciler.Reconcile(m.terminalGeometry, terminalDestinationForSurface(m.surface()))
	cmd := m.pendingOwnershipCmd
	m.pendingOwnershipCmd = nil
	return cmd
}

func (m *uiModel) queueOngoingOwnershipReconciliation() tea.Cmd {
	return func() tea.Msg {
		return ongoingOwnershipReconcileMsg{}
	}
}

func newOngoingOwnershipReconciler(apply func(bool) error) *ongoingOwnershipReconciler {
	if apply == nil {
		panic("ongoing ownership reconciler requires an apply function")
	}
	return &ongoingOwnershipReconciler{apply: apply}
}

func (r *ongoingOwnershipReconciler) Reconcile(geometry terminalGeometry, destination terminalDestination) ownershipReconcileResult {
	if r == nil {
		return ownershipReconcileResult{}
	}
	desired := desiredOngoingOwnership(geometry, destination)
	if r.applied != nil && r.applied.owned == desired {
		return ownershipReconcileResult{DesiredOwned: desired}
	}
	if r.applied == nil && !desired {
		return ownershipReconcileResult{DesiredOwned: false}
	}
	if err := r.apply(desired); err != nil {
		return ownershipReconcileResult{DesiredOwned: desired, Err: err}
	}
	r.applied = &ownershipAppliedState{owned: desired}
	return ownershipReconcileResult{Changed: true, DesiredOwned: desired}
}
