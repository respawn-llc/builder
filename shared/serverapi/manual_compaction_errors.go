package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"

	"core/shared/protocol"
)

const ErrCodeManualCompactionTooSoon = protocol.ErrCodeManualCompactionTooSoon
const ErrCodeManualCompactionDisabled = protocol.ErrCodeManualCompactionDisabled
const ErrCodeManualCompactionActive = protocol.ErrCodeManualCompactionActive

var ErrManualCompactionTooSoon = &ManualCompactionError{}
var ErrManualCompactionDisabled = &ManualCompactionDisabledError{}
var ErrManualCompactionActive = &ManualCompactionActiveError{}

type ManualCompactionReason string

const (
	ManualCompactionReasonTooSoon  ManualCompactionReason = "too_soon"
	ManualCompactionReasonDisabled ManualCompactionReason = "disabled"
	ManualCompactionReasonActive   ManualCompactionReason = "active"
)

func DecodeManualCompactionError(code int, data json.RawMessage) error {
	var payload struct {
		Reason ManualCompactionReason `json:"reason"`
	}
	if len(data) == 0 {
		return errors.New("manual compaction error is missing reason data")
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode manual compaction error: %w", err)
	}
	var expected ManualCompactionReason
	var decoded error
	switch code {
	case ErrCodeManualCompactionTooSoon:
		expected, decoded = ManualCompactionReasonTooSoon, ErrManualCompactionTooSoon
	case ErrCodeManualCompactionDisabled:
		expected, decoded = ManualCompactionReasonDisabled, ErrManualCompactionDisabled
	case ErrCodeManualCompactionActive:
		expected, decoded = ManualCompactionReasonActive, ErrManualCompactionActive
	default:
		return fmt.Errorf("manual compaction error code %d is invalid", code)
	}
	if payload.Reason != expected {
		return fmt.Errorf("manual compaction error reason %q does not match code %d", payload.Reason, code)
	}
	return decoded
}

type ManualCompactionError struct{}

func (*ManualCompactionError) Error() string {
	return "manual compaction is too soon"
}

func (*ManualCompactionError) RPCErrorCode() int {
	return ErrCodeManualCompactionTooSoon
}

func (*ManualCompactionError) RPCErrorData() json.RawMessage {
	return json.RawMessage(`{"reason":"too_soon"}`)
}

var _ protocol.StructuredRPCError = (*ManualCompactionError)(nil)

type ManualCompactionDisabledError struct{}

func (*ManualCompactionDisabledError) Error() string     { return "manual compaction is disabled" }
func (*ManualCompactionDisabledError) RPCErrorCode() int { return ErrCodeManualCompactionDisabled }
func (*ManualCompactionDisabledError) RPCErrorData() json.RawMessage {
	return json.RawMessage(`{"reason":"disabled"}`)
}

var _ protocol.StructuredRPCError = (*ManualCompactionDisabledError)(nil)

type ManualCompactionActiveError struct{}

func (*ManualCompactionActiveError) Error() string     { return "manual compaction is already active" }
func (*ManualCompactionActiveError) RPCErrorCode() int { return ErrCodeManualCompactionActive }
func (*ManualCompactionActiveError) RPCErrorData() json.RawMessage {
	return json.RawMessage(`{"reason":"active"}`)
}

var _ protocol.StructuredRPCError = (*ManualCompactionActiveError)(nil)
