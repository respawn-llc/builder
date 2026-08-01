package serverapi

import (
	"encoding/json"

	"core/shared/protocol"
)

const ErrCodeManualCompactionTooSoon = protocol.ErrCodeManualCompactionTooSoon

var ErrManualCompactionTooSoon = &ManualCompactionError{}

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
