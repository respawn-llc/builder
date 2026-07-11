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
		previousWidth := m.termWidth
		previousHeight := m.termHeight
		previousKnown := m.windowSizeKnown
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.windowSizeKnown = true
		m.layout().syncViewport()
		if !m.nativeOngoingSurfaceActive() {
			if m.ongoingSurface != nil {
				result := m.ongoingSurface.ObserveResize(ongoing.Size{Width: msg.Width, Height: msg.Height})
				if result.Action == ongoing.ResultScheduleWidthRehydration {
					m.pendingOngoingWidthReset = true
					m.pendingOngoingResizeRepaint = false
				} else if !previousKnown || previousWidth != msg.Width || previousHeight != msg.Height {
					m.pendingOngoingResizeRepaint = true
				}
			}
			return handledUIFeatureUpdate(m, nil)
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
		if !previousKnown {
			cmd = tea.Batch(cmd, m.setOngoingNormalBufferOwned(true))
		}
		return handledUIFeatureUpdate(m, cmd)
	}
	return uiFeatureUpdateResult{}
}
