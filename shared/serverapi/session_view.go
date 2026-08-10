package serverapi

import (
	"errors"

	"core/shared/clientui"
	"core/shared/invariant"
)

// ErrLimitNegative is returned when a request supplies a negative limit.
var ErrLimitNegative = errors.New("limit must be >= 0")
var ErrTranscriptCursorDirectionAmbiguous = errors.New("transcript page request must not set both cursor directions")
var ErrTranscriptCursorInvalid = errors.New("transcript cursor must be > 0")

type SessionMainViewRequest struct {
	SessionID string
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

func (r SessionTranscriptPageResponse) Validate() error {
	return invariant.ValidateTranscriptPage(r.Transcript)
}

type SessionLatestCommittedAssistantFinalAnswerRequest struct {
	SessionID string `json:"session_id"`
}

type SessionLatestCommittedAssistantFinalAnswerResponse struct {
	Answer *string `json:"answer"`
}

func (r SessionMainViewRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
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

func (r SessionLatestCommittedAssistantFinalAnswerRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
