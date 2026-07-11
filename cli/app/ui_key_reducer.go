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
		if m.finalAnswerOperation != nil {
			return handledUIFeatureUpdate(m, nil)
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
			prevSurface := m.surface()
			next, cmd := m.askController().handleKey(keyMsg)
			nextModel := next.(*uiModel)
			nextModel.layout().syncViewport()
			repaintCmd := nextModel.renderNativeOngoingSurfaceAfterKey(prevSurface)
			return handledUIFeatureUpdate(nextModel, tea.Batch(cmd, repaintCmd))
		default:
			prevSurface := m.surface()
			next, cmd := m.inputController().handleKey(keyMsg)
			nextModel := next.(*uiModel)
			nextModel.layout().syncViewport()
			repaintCmd := nextModel.renderNativeOngoingSurfaceAfterKey(prevSurface)
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

func (m *uiModel) renderNativeOngoingSurfaceAfterKey(prevSurface uiSurface) tea.Cmd {
	if m == nil {
		return nil
	}
	nextSurface := m.surface()
	if isOngoingNormalBufferRestoreTransition(prevSurface, nextSurface) {
		return nil
	}
	return m.renderNativeOngoingSurface()
}
