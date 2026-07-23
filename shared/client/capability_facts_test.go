package client

import (
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestProtocolErrorReconstructsUnsupportedProvider(t *testing.T) {
	err := protocolError(&protocol.ResponseError{Code: protocol.ErrCodeUnsupportedProvider, Message: "unsupported llm provider: nope"})

	if !errors.Is(err, serverapi.ErrUnsupportedProvider) {
		t.Fatalf("reconstructed error = %v, want ErrUnsupportedProvider", err)
	}
}
