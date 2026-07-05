package app

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type uiKeyFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) keyReducer() uiKeyFeatureReducer {
	return uiKeyFeatureReducer{model: m}
}

func (r uiKeyFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	if keyMsg, ok, source := normalizeKeyMsgWithSource(msg); ok {
		if m.debugKeys {
			m.setDebugKeyTransientStatus(msg, keyMsg, source)
		}
		if m.helpVisible {
			m.helpVisible = false
			if isHelpKey(keyMsg, m) && m.canShowHelp() {
				m.lastEscAt = time.Time{}
				m.layout().syncViewport()
				return handledUIFeatureUpdate(m, m.renderNativeOngoingSurface())
			}
		}
		if isHelpKey(keyMsg, m) && m.canShowHelp() {
			m.lastEscAt = time.Time{}
			m.helpVisible = !m.helpVisible
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.renderNativeOngoingSurface())
		}
		switch m.inputModeState().Mode {
		case uiInputModeAsk:
			next, cmd := m.askController().handleKey(keyMsg)
			nextModel := next.(*uiModel)
			nextModel.layout().syncViewport()
			repaintCmd := nextModel.renderNativeOngoingSurface()
			return handledUIFeatureUpdate(nextModel, tea.Batch(cmd, repaintCmd))
		default:
			next, cmd := m.inputController().handleKey(keyMsg)
			nextModel := next.(*uiModel)
			nextModel.layout().syncViewport()
			repaintCmd := nextModel.renderNativeOngoingSurface()
			return handledUIFeatureUpdate(nextModel, tea.Batch(cmd, repaintCmd))
		}
	}
	if _, isKey := msg.(tea.KeyMsg); isKey {
		if m.helpVisible {
			m.helpVisible = false
		}
		m.lastEscAt = time.Time{}
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, m.renderNativeOngoingSurface())
	}
	return uiFeatureUpdateResult{}
}
