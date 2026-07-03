package app

import tea "github.com/charmbracelet/bubbletea"

func (m *uiModel) requestRuntimeQueuedDrainAfterHydration() tea.Cmd {
	if m == nil {
		return nil
	}
	m.queuedDrainReadyAfterHydration = true
	return m.flushQueuedInputsAfterHydration()
}

func (m *uiModel) clearActiveAssistantStreamSource() {
	if m == nil {
		return
	}
	m.activeAssistantStreamSource = ""
	m.activeAssistantStreamIdentity = uiAssistantStreamIdentity{}
}
