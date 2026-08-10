package app

import (
	"context"
	"errors"
	"strings"

	"core/shared/apicontract"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) sessionDraftRecoveryBuffers() []serverapi.SessionDraftRecoveryBuffer {
	if m == nil {
		return nil
	}
	buffers := make([]serverapi.SessionDraftRecoveryBuffer, 0, 1+len(m.injectedQueue)+len(m.queued))
	if text := strings.TrimSpace(m.activeSubmit.text); m.activeSubmit.token != 0 && text != "" {
		buffers = append(buffers, serverapi.SessionDraftRecoveryBuffer{
			Kind: serverapi.SessionDraftRecoveryBufferActiveSubmit,
			Text: m.activeSubmit.text,
		})
	}
	for _, pending := range m.injectedQueue {
		if !pending.RecoveryOwned {
			continue
		}
		text := strings.TrimSpace(pending.Text)
		if text == "" {
			continue
		}
		kind := serverapi.SessionDraftRecoveryBufferPendingInjectedInput
		switch pending.State {
		case injectedRuntimeQueuePendingCreate,
			injectedRuntimeQueueCanceledBeforeCreate,
			injectedRuntimeQueueCreateFailed:
			kind = serverapi.SessionDraftRecoveryBufferActiveSubmit
		}
		buffers = append(buffers, serverapi.SessionDraftRecoveryBuffer{
			Kind: kind,
			Text: pending.Text,
		})
	}
	for _, queued := range m.queued {
		text := strings.TrimSpace(queued.Text)
		if text == "" {
			continue
		}
		buffers = append(buffers, serverapi.SessionDraftRecoveryBuffer{
			Kind: serverapi.SessionDraftRecoveryBufferQueuedInput,
			Text: queued.Text,
		})
	}
	return buffers
}

func (m *uiModel) restoreSessionDraftRecoveryBuffers(buffers []serverapi.SessionDraftRecoveryBuffer) {
	if m == nil || len(buffers) == 0 {
		return
	}
	recovered := make([]serverapi.SessionDraftRecoveryBuffer, 0, len(buffers))
	for _, buffer := range buffers {
		if strings.TrimSpace(buffer.Text) == "" {
			continue
		}
		recovered = append(recovered, buffer)
	}
	if len(recovered) == 0 {
		return
	}
	m.recoveredDraftBuffers = append([]serverapi.SessionDraftRecoveryBuffer(nil), recovered...)
	m.restoreRecoveredDraftBuffersToVisibleInput(recovered)
}

func (m *uiModel) restoreRecoveredDraftBuffersToVisibleInput(buffers []serverapi.SessionDraftRecoveryBuffer) {
	parts := make([]string, 0, 1+len(buffers))
	if m.mainEditor.Text() != "" {
		parts = append(parts, m.mainEditor.Text())
	}
	for _, buffer := range buffers {
		if strings.TrimSpace(buffer.Text) == "" {
			continue
		}
		parts = append(parts, buffer.Text)
	}
	if len(parts) == 0 {
		return
	}
	m.replaceMainInputAtEnd(strings.Join(parts, "\n\n"))
}

func (m *uiModel) persistSessionDraftRecoveryCmd() tea.Cmd {
	if m == nil {
		return nil
	}
	persistence, request, err := m.sessionDraftRecoveryPersistence()
	return func() tea.Msg {
		if err == nil {
			_, err = persistence.PersistInputDraft(context.Background(), request)
		}
		return draftRecoveryPersistedMsg{err: err}
	}
}

func (m *uiModel) persistActiveSubmitCmd(token uint64) tea.Cmd {
	if m == nil {
		return nil
	}
	persistence, request, err := m.sessionDraftRecoveryPersistence()
	return func() tea.Msg {
		if err == nil {
			_, err = persistence.PersistInputDraft(context.Background(), request)
		}
		return submitDraftPersistedMsg{token: token, err: err}
	}
}

func (m *uiModel) sessionDraftRecoveryPersistence() (
	apicontract.SessionLifecycleService,
	serverapi.SessionPersistInputDraftRequest,
	error,
) {
	if m.sessionDraftPersistence == nil {
		return nil, serverapi.SessionPersistInputDraftRequest{}, errors.New("session Draft Recovery persistence is not configured")
	}
	sessionID := strings.TrimSpace(m.sessionID)
	if sessionID == "" {
		return nil, serverapi.SessionPersistInputDraftRequest{}, errors.New("session ID is required for Draft Recovery persistence")
	}
	return m.sessionDraftPersistence, serverapi.SessionPersistInputDraftRequest{
		SessionID:       sessionID,
		Input:           m.mainEditor.Text(),
		RecoveryBuffers: m.sessionDraftRecoveryBuffers(),
	}, nil
}
