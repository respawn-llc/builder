package app

import (
	"core/cli/tui/ongoing"

	tea "github.com/charmbracelet/bubbletea"
)

type uiWindowFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) windowReducer() uiWindowFeatureReducer {
	return uiWindowFeatureReducer{model: m}
}

func (r uiWindowFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		wasNativeOngoing := m.nativeOngoingSurfaceActive()
		previousSize := m.terminalGeometry.Size()
		if msg.Width > 0 && msg.Height > 0 {
			m.terminalGeometry = terminalGeometryKnown(msg.Width, msg.Height)
		} else {
			m.terminalGeometry = terminalGeometryUnknown()
		}
		m.layout().syncViewport()
		desiredOngoing := m.ongoingSurface != nil &&
			desiredOngoingOwnership(m.terminalGeometry, terminalDestinationForSurface(m.surface()))
		if !desiredOngoing || m.ongoingSurface == nil {
			if m.ongoingSurface != nil {
				result := m.ongoingSurface.ObserveResize(ongoing.Size{Width: msg.Width, Height: msg.Height})
				if result.Action == ongoing.ResultScheduleWidthRehydration {
					m.pendingOngoingWidthReset = true
					m.pendingOngoingResizeRepaint = false
				} else if previousSize == nil || previousSize.width != msg.Width || previousSize.height != msg.Height {
					m.pendingOngoingResizeRepaint = true
				}
			}
			return handledUIFeatureUpdate(m, m.reconcileOngoingOwnership())
		}
		size := ongoing.Size{Width: msg.Width, Height: msg.Height}
		var result ongoing.Result
		var err error
		if m.ongoingTranscript != nil {
			result, err = m.ongoingTranscript.Resize(size)
		} else {
			result, err = m.ongoingSurface.Resize(size, m.ongoingFrameInput())
		}
		if err != nil {
			return handledUIFeatureUpdate(m, m.handleOngoingSurfaceError(err))
		}
		cmd := m.handleOngoingResult(result)
		if previousSize == nil || !wasNativeOngoing {
			cmd = tea.Batch(cmd, m.reconcileOngoingOwnership())
		}
		return handledUIFeatureUpdate(m, cmd)
	}
	return uiFeatureUpdateResult{}
}
