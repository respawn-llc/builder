package rollbacktarget

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidRollbackTargetID is returned when a rollback target id cannot be
// decoded into a valid user-message sequence.
var ErrInvalidRollbackTargetID = errors.New("invalid rollback target id")

const tokenVersion = 2

type CandidateLocator struct {
	UserMessageSeq       int64 `json:"user_message_seq"`
	CandidatePageEndByte int64 `json:"candidate_page_end_byte"`
}

func (l CandidateLocator) Validate() error {
	if l.UserMessageSeq <= 0 {
		return fmt.Errorf("rollback candidate user message sequence must be positive, got %d", l.UserMessageSeq)
	}
	if l.CandidatePageEndByte <= 0 {
		return fmt.Errorf("rollback candidate page end byte must be positive, got %d", l.CandidatePageEndByte)
	}
	return nil
}

type tokenPayload struct {
	Version        int   `json:"v"`
	UserMessageSeq int64 `json:"s"`
}

func EncodeUserMessageSeq(seq int64) string {
	if seq <= 0 {
		return ""
	}
	payload, err := json.Marshal(tokenPayload{Version: tokenVersion, UserMessageSeq: seq})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func DecodeUserMessageSeq(raw string) (int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, fmt.Errorf("rollback target id is required")
	}
	payload, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		return 0, ErrInvalidRollbackTargetID
	}
	var decoded tokenPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return 0, ErrInvalidRollbackTargetID
	}
	if decoded.Version != tokenVersion {
		return 0, fmt.Errorf("unsupported rollback target id version")
	}
	if decoded.UserMessageSeq <= 0 {
		return 0, ErrInvalidRollbackTargetID
	}
	return decoded.UserMessageSeq, nil
}
