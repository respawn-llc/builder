package protocol

import (
	"encoding/json"
	"testing"
)

func mustRPCErrorData(t *testing.T, source StructuredRPCError) json.RawMessage {
	t.Helper()
	return source.RPCErrorData()
}
