package client

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestProtocolErrorDecodesManualCompactionAdmissionError(t *testing.T) {
	for _, reason := range []serverapi.ManualCompactionAdmissionReason{
		serverapi.ManualCompactionAdmissionActive,
		serverapi.ManualCompactionAdmissionDisabled,
		serverapi.ManualCompactionAdmissionTooSoon,
	} {
		source := &serverapi.ManualCompactionAdmissionError{Reason: reason}
		err := protocolError(&protocol.ResponseError{
			Code:    protocol.ErrCodeManualCompactionAdmission,
			Message: "server display text that must not be parsed",
			Data:    source.RPCErrorData(),
		})
		var decoded *serverapi.ManualCompactionAdmissionError
		if !errors.As(err, &decoded) || decoded.Reason != reason {
			t.Fatalf("decoded %q = %T %+v", reason, err, err)
		}
	}
}

func TestProtocolErrorUsesGenericMessageForMalformedManualCompactionAdmissionData(t *testing.T) {
	err := protocolError(&protocol.ResponseError{
		Code:    protocol.ErrCodeManualCompactionAdmission,
		Message: "server display text",
		Data:    json.RawMessage(`{"reason":"too_soon"}`),
	})
	var decoded *serverapi.ManualCompactionAdmissionError
	if errors.As(err, &decoded) {
		t.Fatalf("decoded malformed error as typed %+v", decoded)
	}
	if err == nil || err.Error() != "server display text" {
		t.Fatalf("decoded malformed error = %v, want generic message", err)
	}
}
