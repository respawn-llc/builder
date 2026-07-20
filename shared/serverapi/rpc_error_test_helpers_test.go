package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/protocol"
)

func mustRPCErrorData(
	t *testing.T,
	source protocol.StructuredRPCError,
) json.RawMessage {
	t.Helper()
	data, err := source.RPCErrorData()
	if err != nil {
		t.Fatalf("encode RPC error data: %v", err)
	}
	return data
}
