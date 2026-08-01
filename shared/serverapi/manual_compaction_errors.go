package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
)

var ErrManualCompactionAdmission = errors.New("manual compaction admission rejected")

type ManualCompactionAdmissionReason string

const (
	ManualCompactionAdmissionActive   ManualCompactionAdmissionReason = "active"
	ManualCompactionAdmissionDisabled ManualCompactionAdmissionReason = "disabled"
	ManualCompactionAdmissionTooSoon  ManualCompactionAdmissionReason = "too_soon"
)

type ManualCompactionAdmissionError struct {
	Reason ManualCompactionAdmissionReason `json:"reason"`
}

func (e *ManualCompactionAdmissionError) RetainRuntimeOperation() {}

func (e *ManualCompactionAdmissionError) Error() string {
	if e == nil {
		return ErrManualCompactionAdmission.Error()
	}
	return fmt.Sprintf("%s: %s", ErrManualCompactionAdmission, e.Reason)
}

func (e *ManualCompactionAdmissionError) Is(target error) bool {
	return target == ErrManualCompactionAdmission
}

func (e *ManualCompactionAdmissionError) Validate() error {
	if e == nil {
		return errors.New("manual compaction admission error is required")
	}
	switch e.Reason {
	case ManualCompactionAdmissionActive,
		ManualCompactionAdmissionDisabled,
		ManualCompactionAdmissionTooSoon:
		return nil
	default:
		return fmt.Errorf("manual compaction admission reason is invalid: %q", e.Reason)
	}
}

func (e *ManualCompactionAdmissionError) RPCErrorCode() int {
	return protocol.ErrCodeManualCompactionAdmission
}

func (e *ManualCompactionAdmissionError) RPCErrorData() json.RawMessage {
	if err := e.Validate(); err != nil {
		panic("marshal manual compaction admission error: " + err.Error())
	}
	data, err := json.Marshal(struct {
		Type   string                          `json:"type"`
		Reason ManualCompactionAdmissionReason `json:"reason"`
	}{
		Type:   "manual_compaction_admission_error",
		Reason: e.Reason,
	})
	if err != nil {
		panic("marshal manual compaction admission error: " + err.Error())
	}
	return data
}

func DecodeManualCompactionAdmissionError(data json.RawMessage, fallback string) error {
	var envelope struct {
		Type   string                          `json:"type"`
		Reason ManualCompactionAdmissionReason `json:"reason"`
	}
	if err := protocol.DecodeStrictJSON(data, &envelope); err != nil ||
		envelope.Type != "manual_compaction_admission_error" ||
		(&ManualCompactionAdmissionError{Reason: envelope.Reason}).Validate() != nil {
		if message := strings.TrimSpace(fallback); message != "" {
			return errors.New(message)
		}
		return ErrManualCompactionAdmission
	}
	return &ManualCompactionAdmissionError{Reason: envelope.Reason}
}
