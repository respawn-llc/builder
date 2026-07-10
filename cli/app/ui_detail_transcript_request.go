package app

import (
	"context"
	"errors"
	"strings"

	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/valuecopy"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type uiPendingDetailTranscriptRequest struct {
	id        uuid.UUID
	sessionID string
	request   clientui.TranscriptPageRequest
	cancel    context.CancelFunc
}

func (m *uiModel) loadDetailTranscriptPageCmd(req clientui.TranscriptPageRequest) tea.Cmd {
	if m == nil || m.pendingDetailTranscript != nil {
		return nil
	}
	sessionID := strings.TrimSpace(m.currentRuntimeSessionID())
	request := clientui.TranscriptPageRequest{
		Cursor:      valuecopy.Pointer(req.Cursor),
		NewerCursor: valuecopy.Pointer(req.NewerCursor),
	}
	requestID := uuid.New()
	parentCtx, cancel := context.WithCancel(context.Background())
	m.pendingDetailTranscript = &uiPendingDetailTranscriptRequest{
		id:        requestID,
		sessionID: sessionID,
		request:   request,
		cancel:    cancel,
	}
	if request.Cursor != nil || request.NewerCursor != nil {
		m.showDetailTranscriptLoadingNotice(requestID)
	}
	client := m.statusConfig.SessionViews
	return func() tea.Msg {
		if client == nil {
			return detailTranscriptLoadMsg{
				requestID: requestID,
				err:       errors.New("session view client is required"),
			}
		}
		ctx, timeoutCancel := context.WithTimeout(parentCtx, uiRuntimeHydrationReadTimeout)
		defer timeoutCancel()
		resp, err := client.GetSessionTranscriptPage(ctx, serverapi.SessionTranscriptPageRequest{
			SessionID:   sessionID,
			Cursor:      request.Cursor,
			NewerCursor: request.NewerCursor,
		})
		return detailTranscriptLoadMsg{requestID: requestID, page: resp.Transcript, err: err}
	}
}

func (m *uiModel) takePendingDetailTranscriptRequest(requestID uuid.UUID) (uiPendingDetailTranscriptRequest, bool) {
	if m == nil || m.pendingDetailTranscript == nil || m.pendingDetailTranscript.id != requestID {
		return uiPendingDetailTranscriptRequest{}, false
	}
	pending := *m.pendingDetailTranscript
	m.pendingDetailTranscript = nil
	pending.cancel()
	return pending, true
}

func (m *uiModel) cancelPendingDetailTranscriptRequest() tea.Cmd {
	if m == nil || m.pendingDetailTranscript == nil {
		return nil
	}
	pending := *m.pendingDetailTranscript
	m.pendingDetailTranscript = nil
	pending.cancel()
	return m.clearDetailTranscriptLoadingNotice(pending.id)
}

func (m *uiModel) showDetailTranscriptLoadingNotice(requestID uuid.UUID) {
	requestIDCopy := requestID
	_ = m.showTransientStatusNotice(uiStatusNotice{
		Text:      "Loading transcript history",
		Kind:      uiStatusNoticeInfo,
		RequestID: &requestIDCopy,
	})
}

func (m *uiModel) clearDetailTranscriptLoadingNotice(requestID uuid.UUID) tea.Cmd {
	if m == nil || m.transientStatusRequestID == nil || *m.transientStatusRequestID != requestID {
		return nil
	}
	return m.advanceTransientStatusQueue()
}

func cloneUUID(value *uuid.UUID) *uuid.UUID {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
