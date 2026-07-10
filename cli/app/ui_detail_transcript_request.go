package app

import (
	"context"
	"errors"
	"fmt"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/valuecopy"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
)

type uiPendingDetailTranscriptRequest struct {
	id        uuid.UUID
	sessionID runtimeids.SessionID
	request   clientui.TranscriptPageRequest
	cancel    context.CancelFunc
}

func (m *uiModel) loadDetailTranscriptPageCmd(req clientui.TranscriptPageRequest) tea.Cmd {
	if m == nil || m.pendingDetailTranscript != nil {
		return nil
	}
	sessionID, err := runtimeids.ParseSessionID(m.currentRuntimeSessionID())
	if err != nil {
		return m.sendTransientStatusWithNoticeID(
			err.Error(),
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	}
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
			SessionID:   sessionID.String(),
			Cursor:      request.Cursor,
			NewerCursor: request.NewerCursor,
		})
		if err == nil {
			err = validateDetailTranscriptPageResponse(sessionID, resp.Transcript)
		}
		return detailTranscriptLoadMsg{requestID: requestID, page: resp.Transcript, err: err}
	}
}

func validateDetailTranscriptPageResponse(
	requestSessionID runtimeids.SessionID,
	page clientui.TranscriptPage,
) error {
	if err := clientui.ValidateTranscriptPage(page); err != nil {
		return err
	}
	responseSessionID, err := runtimeids.ParseSessionID(page.SessionID)
	if err != nil {
		return err
	}
	if responseSessionID != requestSessionID {
		return fmt.Errorf(
			"transcript page session %s does not match requested session %s",
			responseSessionID.String(),
			requestSessionID.String(),
		)
	}
	return nil
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
