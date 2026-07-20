package protocol

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSessionEventLogMaterializationErrorUnsupportedVersionWireContract(t *testing.T) {
	found := 2
	supported := 1
	source := &SessionEventLogMaterializationError{
		Reason:           SessionEventLogMaterializationUnsupportedVersion,
		Stage:            SessionEventLogMaterializationPreparation,
		Committed:        false,
		PendingRepair:    false,
		FoundVersion:     &found,
		SupportedVersion: &supported,
	}
	if err := source.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if source.RPCErrorCode() != ErrCodeSessionEventLogMaterialization {
		t.Fatalf(
			"RPCErrorCode = %d, want %d",
			source.RPCErrorCode(),
			ErrCodeSessionEventLogMaterialization,
		)
	}
	decodedErr := DecodeSessionEventLogMaterializationError(
		mustRPCErrorData(t, source),
		"fallback materialization failure",
	)
	var decoded *SessionEventLogMaterializationError
	if !errors.As(decodedErr, &decoded) {
		t.Fatalf(
			"decoded error = %T %v, want SessionEventLogMaterializationError",
			decodedErr,
			decodedErr,
		)
	}
	if decoded.Reason != source.Reason ||
		decoded.Committed != source.Committed ||
		decoded.PendingRepair != source.PendingRepair ||
		decoded.FoundVersion == nil ||
		*decoded.FoundVersion != found ||
		decoded.SupportedVersion == nil ||
		*decoded.SupportedVersion != supported {
		t.Fatalf("decoded = %+v, want %+v", decoded, source)
	}

	var data map[string]any
	if err := json.Unmarshal(mustRPCErrorData(t, source), &data); err != nil {
		t.Fatalf("decode RPC data: %v", err)
	}
	if data["type"] != "session_event_log_materialization_error" {
		t.Fatalf("wire type = %v", data["type"])
	}
}

func TestSessionEventLogMaterializationErrorRejectsMalformedWireData(t *testing.T) {
	tests := []json.RawMessage{
		[]byte(`{"type":"session_event_log_materialization_error","reason":"unknown","stage":"preparation","committed":false,"pending_repair":false}`),
		[]byte(`{"type":"session_event_log_materialization_error","reason":"materialization_failure","stage":"unknown","committed":false,"pending_repair":false}`),
		[]byte(`{"type":"session_event_log_materialization_error","reason":"reconciliation_pending","stage":"reconciliation","committed":false,"pending_repair":true}`),
		[]byte(`{"type":"session_event_log_materialization_error","reason":"unsupported_version","stage":"preparation","committed":false,"pending_repair":false}`),
		[]byte(`{"type":"session_event_log_materialization_error","reason":"materialization_failure","stage":"preparation","committed":false,"pending_repair":false,"found_version":2,"supported_version":1}`),
		[]byte(`{"type":"session_event_log_materialization_error","reason":"materialization_failure","stage":"preparation","committed":false,"pending_repair":false,"unknown":true}`),
		[]byte(`{"type":"session_event_log_materialization_error","reason":"materialization_failure","stage":"preparation","committed":false,"pending_repair":false} {}`),
	}
	for _, data := range tests {
		decoded := DecodeSessionEventLogMaterializationError(data, "fallback")
		var typed *SessionEventLogMaterializationError
		if errors.As(decoded, &typed) {
			t.Fatalf("malformed data decoded as typed error: %s -> %+v", data, typed)
		}
		if decoded.Error() != "fallback" {
			t.Fatalf("malformed fallback = %q, want fallback", decoded)
		}
	}
}

func TestInvalidSessionEventLogMaterializationErrorUsesInvariantPolicy(t *testing.T) {
	t.Run("release diagnostic", func(t *testing.T) {
		t.Setenv("KENT_INVARIANT_MODE", "diagnostic")
		invalid := &SessionEventLogMaterializationError{}
		if data, err := invalid.RPCErrorData(); err == nil || data != nil {
			t.Fatalf("invalid RPC error data = %s error=%v, want typed error", data, err)
		}
	})

	t.Run("debug panic", func(t *testing.T) {
		t.Setenv("KENT_INVARIANT_MODE", "panic")
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("debug protocol invariant did not panic")
			}
		}()
		_, _ = (&SessionEventLogMaterializationError{}).RPCErrorData()
	})
}
