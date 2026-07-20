package client

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
	return source.RPCErrorData()
}
