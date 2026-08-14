package app

import (
	"strings"

	tuiinput "core/cli/tui/input"
	"core/cli/tui/ongoing"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) loadInitialPromptHistory(initial []string, rawCount int) {
	m.validateInitialPromptHistoryCount(rawCount)
	loaded := make([]string, 0, len(initial))
	for _, raw := range initial {
		if text := preservePromptHistoryText(raw); text != "" {
			loaded = append(loaded, text)
		}
	}
	m.promptHistory = loaded
}

func preservePromptHistoryText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

func (m *uiModel) resetPromptHistoryNavigation() {
	m.promptHistorySelection = nil
	m.promptHistoryDraft = nil
}

func (m *uiModel) promptHistorySelectionActive() bool {
	return m.promptHistorySelection != nil &&
		*m.promptHistorySelection >= 0 &&
		*m.promptHistorySelection < len(m.promptHistory)
}

func (m *uiModel) selectedPromptHistoryText() (string, bool) {
	if !m.promptHistorySelectionActive() {
		return "", false
	}
	return m.promptHistory[*m.promptHistorySelection], true
}

func (m *uiModel) promptHistorySelectionMatchesInput() bool {
	selected, ok := m.selectedPromptHistoryText()
	if !ok {
		return false
	}
	return m.mainEditor.Text() == selected
}

func (m *uiModel) inputCursorAtBoundary() bool {
	cursor := m.mainEditor.Cursor()
	return cursor == 0 || cursor == len(m.mainEditor.Text())
}

func (m *uiModel) inputCursorAtStart() bool {
	return m.mainEditor.Cursor() == 0
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
	m.promptHistorySelection = nil
}

func (m *uiModel) shouldAttemptPromptHistoryNavigation(delta int) bool {
	if delta == 0 {
		return false
	}
	if len(m.promptHistory) == 0 {
		return false
	}
	if m.mainEditor.Text() == "" {
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
		if m.mainEditor.Text() != "" && !m.inputCursorAtStart() {
			return false
		}
		snapshot := m.mainEditor.Snapshot()
		m.promptHistoryDraft = &snapshot
		selection := len(m.promptHistory) - 1
		m.promptHistorySelection = &selection
		m.applyPromptHistorySelection()
		return true
	}
	if !m.promptHistoryCursorAtBoundary() {
		return false
	}
	if *m.promptHistorySelection == 0 {
		return false
	}
	selection := *m.promptHistorySelection - 1
	m.promptHistorySelection = &selection
	m.applyPromptHistorySelection()
	return true
}

func (m *uiModel) navigatePromptHistoryDown() bool {
	if !m.promptHistorySelectionActive() || m.mainEditor.Text() == "" || !m.promptHistoryCursorAtBoundary() {
		return false
	}
	if *m.promptHistorySelection == len(m.promptHistory)-1 {
		m.restorePromptHistoryDraft()
		return true
	}
	selection := *m.promptHistorySelection + 1
	m.promptHistorySelection = &selection
	m.applyPromptHistorySelection()
	return true
}

func (m *uiModel) hasPromptHistoryDraft() bool {
	return m.promptHistoryDraft != nil
}

func (m *uiModel) restorePromptHistoryDraft() {
	if m.promptHistoryDraft == nil {
		return
	}
	m.mainEditor.Restore(*m.promptHistoryDraft)
	m.mainInputMutated()
	m.resetPromptHistoryNavigation()
}

func (m *uiModel) capturePromptHistoryDraftForReuse() *tuiinput.EditorSnapshot {
	if m.promptHistoryDraft == nil {
		return nil
	}
	draft := *m.promptHistoryDraft
	return &draft
}

func (m *uiModel) restoreCapturedPromptHistoryDraft(draft *tuiinput.EditorSnapshot) bool {
	if draft == nil {
		return false
	}
	captured := *draft
	m.promptHistoryDraft = &captured
	m.restorePromptHistoryDraft()
	return true
}

func (m *uiModel) applyPromptHistorySelection() {
	if !m.promptHistorySelectionActive() {
		return
	}
	m.replaceMainInputAtEnd(m.promptHistory[*m.promptHistorySelection])
}

func (m *uiModel) rememberPromptHistoryLocally(text string) bool {
	if text = preservePromptHistoryText(text); text == "" {
		return false
	}
	m.promptHistory = appendPromptHistoryTail(m.promptHistory, []string{text})
	m.resetPromptHistoryNavigation()
	return true
}

func (m *uiModel) rememberPromptCommandHistoryLocally(text string) bool {
	draft := m.capturePromptHistoryDraftForReuse()
	remembered := m.rememberPromptHistoryLocally(text)
	m.restoreCapturedPromptHistoryDraft(draft)
	return remembered
}

func appendPromptHistoryTail(tail []string, appended []string) []string {
	if len(appended) == 0 {
		return tail
	}
	maximum := serverapi.SessionPromptHistoryMaxEntries
	if len(tail) > maximum {
		tail = tail[len(tail)-maximum:]
	}
	if cap(tail) != maximum {
		bounded := make([]string, len(tail), maximum)
		copy(bounded, tail)
		tail = bounded
	}
	if len(appended) >= maximum {
		tail = tail[:0]
		return append(tail, appended[len(appended)-maximum:]...)
	}
	keep := maximum - len(appended)
	if len(tail) > keep {
		copy(tail, tail[len(tail)-keep:])
		tail = tail[:keep]
	}
	return append(tail, appended...)
}

func (m *uiModel) validateInitialPromptHistoryCount(count int) {
	if count <= serverapi.SessionPromptHistoryMaxEntries {
		return
	}
	m.handleOngoingDeveloperError(ongoing.NewDeveloperError(
		"load_prompt_history",
		"session-opening prompt history exceeds contract maximum",
		map[string]any{
			"actual_count":  count,
			"maximum_count": serverapi.SessionPromptHistoryMaxEntries,
		},
	))
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

func ringBellCmd(output *uiTerminalOutput) tea.Cmd {
	return func() tea.Msg {
		if err := writeTerminalSequence(output, terminalBell); err != nil {
			return terminalSequenceWriteErrMsg{err: err}
		}
		return nil
	}
}
