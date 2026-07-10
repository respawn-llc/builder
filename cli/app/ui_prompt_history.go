package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const promptHistoryLimit = 100

func (m *uiModel) loadPromptHistory(history []string) {
	for _, raw := range history {
		if text := preservePromptHistoryText(raw); text != "" {
			m.promptHistory = append(m.promptHistory, text)
		}
	}
	m.trimPromptHistory()
}

func preservePromptHistoryText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

func (m *uiModel) resetPromptHistoryNavigation() {
	m.promptHistorySelection = -1
	m.promptHistoryDraft = ""
	m.promptHistoryDraftCursor = -1
}

func (m *uiModel) promptHistorySelectionActive() bool {
	return m.promptHistorySelection >= 0 && m.promptHistorySelection < len(m.promptHistory)
}

func (m *uiModel) selectedPromptHistoryText() (string, bool) {
	if !m.promptHistorySelectionActive() {
		return "", false
	}
	return m.promptHistory[m.promptHistorySelection], true
}

func (m *uiModel) promptHistorySelectionMatchesInput() bool {
	selected, ok := m.selectedPromptHistoryText()
	if !ok {
		return false
	}
	return m.input == selected
}

func (m *uiModel) inputCursorAtBoundary() bool {
	cursor := clampCursor(m.inputCursor, len([]rune(m.input)))
	return cursor == 0 || cursor == len([]rune(m.input))
}

func (m *uiModel) inputCursorAtStart() bool {
	return clampCursor(m.inputCursor, len([]rune(m.input))) == 0
}

func (m *uiModel) promptHistoryCursorAtBoundary() bool {
	if !m.promptHistorySelectionMatchesInput() {
		return false
	}
	return m.inputCursorAtBoundary()
}

func (m *uiModel) shouldSuppressSlashCommandPicker() bool {
	if m.promptHistorySelectionMatchesInput() {
		return true
	}
	return m.slashCommandDisabledReason() != ""
}

func (m *uiModel) syncPromptHistorySelectionToInput() {
	if !m.promptHistorySelectionActive() {
		return
	}
	if m.promptHistorySelectionMatchesInput() {
		return
	}
	m.promptHistorySelection = -1
}

func (m *uiModel) shouldAttemptPromptHistoryNavigation(delta int) bool {
	if delta == 0 {
		return false
	}
	if len(m.promptHistory) == 0 {
		return false
	}
	if m.input == "" {
		return true
	}
	if m.promptHistorySelectionActive() {
		return m.promptHistoryCursorAtBoundary()
	}
	if m.hasPromptHistoryDraft() {
		return false
	}
	if delta < 0 {
		return m.inputCursorAtStart()
	}
	return false
}

func (m *uiModel) navigatePromptHistory(delta int) bool {
	if len(m.promptHistory) == 0 || delta == 0 {
		return false
	}
	if delta < 0 {
		return m.navigatePromptHistoryUp()
	}
	return m.navigatePromptHistoryDown()
}

func (m *uiModel) navigatePromptHistoryUp() bool {
	if !m.promptHistorySelectionActive() {
		if m.input != "" && !m.inputCursorAtStart() {
			return false
		}
		m.promptHistoryDraft = m.input
		m.promptHistoryDraftCursor = m.inputCursor
		m.promptHistorySelection = len(m.promptHistory) - 1
		m.applyPromptHistorySelection()
		return true
	}
	if !m.promptHistoryCursorAtBoundary() {
		return false
	}
	if m.promptHistorySelection == 0 {
		return false
	}
	m.promptHistorySelection--
	m.applyPromptHistorySelection()
	return true
}

func (m *uiModel) navigatePromptHistoryDown() bool {
	if !m.promptHistorySelectionActive() || m.input == "" || !m.promptHistoryCursorAtBoundary() {
		return false
	}
	if m.promptHistorySelection == len(m.promptHistory)-1 {
		m.restorePromptHistoryDraft()
		return true
	}
	m.promptHistorySelection++
	m.applyPromptHistorySelection()
	return true
}

func (m *uiModel) hasPromptHistoryDraft() bool {
	return m.promptHistoryDraft != "" || m.promptHistoryDraftCursor >= 0
}

func (m *uiModel) restorePromptHistoryDraft() {
	m.replaceMainInput(m.promptHistoryDraft, m.promptHistoryDraftCursor)
	m.resetPromptHistoryNavigation()
}

func (m *uiModel) capturePromptHistoryDraftForReuse() (string, int, bool) {
	if !m.hasPromptHistoryDraft() {
		return "", -1, false
	}
	return m.promptHistoryDraft, m.promptHistoryDraftCursor, true
}

func (m *uiModel) restoreCapturedPromptHistoryDraft(text string, cursor int, ok bool) bool {
	if !ok {
		return false
	}
	m.promptHistoryDraft = text
	m.promptHistoryDraftCursor = cursor
	m.restorePromptHistoryDraft()
	return true
}

func (m *uiModel) applyPromptHistorySelection() {
	if !m.promptHistorySelectionActive() {
		return
	}
	m.replaceMainInput(m.promptHistory[m.promptHistorySelection], -1)
}

func (m *uiModel) rememberPromptHistoryLocally(text string) bool {
	if text = preservePromptHistoryText(text); text == "" {
		return false
	}
	m.promptHistory = append(m.promptHistory, text)
	m.trimPromptHistory()
	m.resetPromptHistoryNavigation()
	return true
}

func (m *uiModel) trimPromptHistory() {
	if len(m.promptHistory) <= promptHistoryLimit {
		return
	}
	m.promptHistory = append([]string(nil), m.promptHistory[len(m.promptHistory)-promptHistoryLimit:]...)
}

func (m *uiModel) recordPromptHistory(text string) tea.Cmd {
	if !m.rememberPromptHistoryLocally(text) {
		return nil
	}
	text = preservePromptHistoryText(text)
	client := m.runtimeClient()
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		if err := client.RecordPromptHistory(text); err != nil {
			return promptHistoryPersistErrMsg{err: err}
		}
		return nil
	}
}

func ringBellCmd() tea.Cmd {
	return func() tea.Msg {
		if err := writeTerminalSequence(terminalBell); err != nil {
			return terminalSequenceWriteErrMsg{err: err}
		}
		return nil
	}
}
