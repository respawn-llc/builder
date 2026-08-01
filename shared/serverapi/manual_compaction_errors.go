package serverapi

import (
	"encoding/json"

	"core/shared/protocol"
)

const ErrCodeManualCompactionTooSoon = protocol.ErrCodeManualCompactionTooSoon
const ErrCodeManualCompactionDisabled = protocol.ErrCodeManualCompactionDisabled

var ErrManualCompactionTooSoon = &ManualCompactionError{}
var ErrManualCompactionDisabled = &ManualCompactionDisabledError{}

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
