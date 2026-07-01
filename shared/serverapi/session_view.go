package serverapi

import (
	"errors"

	"core/shared/clientui"
)

// ErrLimitNegative is returned when a request supplies a negative limit.
var ErrLimitNegative = errors.New("limit must be >= 0")

type SessionMainViewRequest struct {
	SessionID string
}

type SessionMainViewResponse struct {
	MainView clientui.RuntimeMainView
}

type SessionTranscriptPageRequest struct {
	SessionID   string `json:"session_id"`
	Cursor      int64  `json:"cursor,omitempty"`
	NewerCursor int64  `json:"newer_cursor,omitempty"`
}

type SessionTranscriptPageResponse struct {
	Transcript clientui.TranscriptPage `json:"transcript"`
}

type SessionCommittedTranscriptSuffixRequest struct {
	SessionID string `json:"session_id"`
}

type SessionCommittedTranscriptSuffixResponse struct {
	Suffix clientui.CommittedTranscriptSuffix `json:"suffix"`
}

func (r SessionMainViewRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}

func (r SessionTranscriptPageRequest) Validate() error {
	if err := validateRequiredSessionID(r.SessionID); err != nil {
		return err
	}
	if r.Cursor < 0 || r.NewerCursor < 0 {
		return errors.New("cursor values must be >= 0")
	}
	if r.Cursor > 0 && r.NewerCursor > 0 {
		return errors.New("cursor and newer_cursor are mutually exclusive")
	}
	return nil
}

func (r SessionCommittedTranscriptSuffixRequest) Validate() error {
	return validateRequiredSessionID(r.SessionID)
}
