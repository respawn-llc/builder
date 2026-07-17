package app

import (
	"strings"

	"core/shared/serverapi"
)

func (m *uiModel) sessionDraftRecoveryBuffers() []serverapi.SessionDraftRecoveryBuffer {
	if m == nil {
		return nil
	}
	buffers := make([]serverapi.SessionDraftRecoveryBuffer, 0, len(m.pendingInjected)+len(m.queued))
	for _, pending := range m.pendingInjected {
		text := strings.TrimSpace(pending.Text)
		if text == "" {
			continue
		}
		buffers = append(buffers, serverapi.SessionDraftRecoveryBuffer{
			Kind: serverapi.SessionDraftRecoveryBufferPendingInjectedInput,
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
		if buffer.Kind == serverapi.SessionDraftRecoveryBufferActiveSubmit {
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
