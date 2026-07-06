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
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.windowSizeKnown = true
		m.layout().syncViewport()
		if !m.nativeOngoingSurfaceActive() {
			if m.ongoingSurface != nil {
				result := m.ongoingSurface.ObserveResize(ongoing.Size{Width: msg.Width, Height: msg.Height})
				if result.Action == ongoing.ResultScheduleWidthRehydration {
					m.pendingOngoingWidthReset = true
				}
			}
			return handledUIFeatureUpdate(m, nil)
		}
		result, err := m.ongoingSurface.Resize(ongoing.Size{Width: msg.Width, Height: msg.Height}, m.ongoingFrameInput())
		if err != nil {
			return handledUIFeatureUpdate(m, m.handleOngoingSurfaceError(err))
		}
		return handledUIFeatureUpdate(m, m.handleOngoingResult(result))
	}
	return uiFeatureUpdateResult{}
}
