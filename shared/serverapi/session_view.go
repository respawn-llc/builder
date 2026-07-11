package serverapi

import (
	"errors"

	"core/shared/clientui"
)

// ErrLimitNegative is returned when a request supplies a negative limit.
var ErrLimitNegative = errors.New("limit must be >= 0")
var ErrTranscriptCursorDirectionAmbiguous = errors.New("transcript page request must not set both cursor directions")
var ErrTranscriptCursorInvalid = errors.New("transcript cursor must be > 0")

type SessionMainViewRequest struct {
	SessionID            string
	PendingOperationRefs []clientui.RuntimeOperationRef
}

type SessionMainViewResponse struct {
	MainView clientui.RuntimeMainView
}

type SessionTranscriptPageRequest struct {
	SessionID   string `json:"session_id"`
	Cursor      *int64 `json:"cursor,omitempty"`
	NewerCursor *int64 `json:"newer_cursor,omitempty"`
}

type SessionTranscriptPageResponse struct {
	Transcript clientui.TranscriptPage `json:"transcript"`
}

func (r SessionMainViewRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	for _, ref := range r.PendingOperationRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (r SessionTranscriptPageRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if r.Cursor != nil && r.NewerCursor != nil {
		return ErrTranscriptCursorDirectionAmbiguous
	}
	if r.Cursor != nil && *r.Cursor <= 0 {
		return ErrTranscriptCursorInvalid
	}
	if r.NewerCursor != nil && *r.NewerCursor <= 0 {
		return ErrTranscriptCursorInvalid
	}
	return nil
}
