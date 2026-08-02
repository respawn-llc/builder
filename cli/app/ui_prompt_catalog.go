package app

import (
	"context"
	"errors"

	"core/cli/app/commands"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func promptCatalogSnapshot(response serverapi.PromptCommandCatalogResponse) ([]commands.PromptCommandCatalogEntry, error) {
	if err := response.Validate(); err != nil {
		return nil, err
	}
	return append([]commands.PromptCommandCatalogEntry(nil), response.Commands...), nil
}

func (m *uiModel) removePromptCatalogEntry(name string) {
	if m == nil {
		return
	}
	filtered := make([]commands.PromptCommandCatalogEntry, 0, len(m.promptCatalogEntries))
	for _, entry := range m.promptCatalogEntries {
		if entry.Name == name {
			continue
		}
		filtered = append(filtered, entry)
	}
	m.promptCatalogEntries = filtered
	m.commandRegistry = commands.NewDefaultRegistryWithPromptCatalog(filtered)
}

func (m *uiModel) startPromptCatalogRefresh(name string) tea.Cmd {
	if m == nil || m.promptCatalog == nil {
		return nil
	}
	m.removePromptCatalogEntry(name)
	m.promptCatalogRefreshToken++
	if m.promptCatalogRefreshToken == 0 {
		m.promptCatalogRefreshToken++
	}
	token := m.promptCatalogRefreshToken
	catalog := m.promptCatalog
	return func() tea.Msg {
		response, err := catalog.GetPromptCommandCatalog(context.Background(), serverapi.PromptCommandCatalogRequest{})
		if err != nil {
			return promptCatalogRefreshDoneMsg{token: token, err: err}
		}
		entries, err := promptCatalogSnapshot(response)
		if err != nil {
			return promptCatalogRefreshDoneMsg{token: token, err: err}
		}
		return promptCatalogRefreshDoneMsg{token: token, entries: entries}
	}
}

func (m *uiModel) handlePromptCatalogRefreshDone(msg promptCatalogRefreshDoneMsg) tea.Cmd {
	if m == nil || msg.token != m.promptCatalogRefreshToken {
		return nil
	}
	if msg.err != nil {
		return m.sendTransientStatusWithNoticeID("Custom prompt commands are unavailable for this session.", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	m.promptCatalogEntries = append([]commands.PromptCommandCatalogEntry(nil), msg.entries...)
	m.commandRegistry = commands.NewDefaultRegistryWithPromptCatalog(m.promptCatalogEntries)
	return nil
}

func promptCommandNotFound(err error) (*serverapi.PromptCommandError, bool) {
	var typed *serverapi.PromptCommandError
	if !errors.As(err, &typed) || typed.Kind != serverapi.PromptCommandErrorKindCommandNotFound {
		return nil, false
	}
	return typed, true
}
