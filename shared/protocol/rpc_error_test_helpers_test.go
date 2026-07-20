package protocol

import (
	"encoding/json"
	"testing"
)

func mustRPCErrorData(t *testing.T, source StructuredRPCError) json.RawMessage {
	t.Helper()
	data, err := source.RPCErrorData()
	if err != nil {
		t.Fatalf("encode RPC error data: %v", err)
	}
	return data
}
