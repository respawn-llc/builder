package serverapi

import (
	"encoding/json"

	"core/shared/protocol"
)

const ErrCodeManualCompactionTooSoon = protocol.ErrCodeManualCompactionTooSoon
const ErrCodeManualCompactionDisabled = protocol.ErrCodeManualCompactionDisabled
const ErrCodeManualCompactionActive = protocol.ErrCodeManualCompactionActive

var ErrManualCompactionTooSoon = &ManualCompactionError{}
var ErrManualCompactionDisabled = &ManualCompactionDisabledError{}
var ErrManualCompactionActive = &ManualCompactionActiveError{}

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
