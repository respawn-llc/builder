package serverapi

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
)

func TestManualCompactionAdmissionErrorValidatesAndRoundTripsEachReason(t *testing.T) {
	for _, reason := range []ManualCompactionAdmissionReason{
		ManualCompactionAdmissionActive,
		ManualCompactionAdmissionDisabled,
		ManualCompactionAdmissionTooSoon,
	} {
		source := &ManualCompactionAdmissionError{Reason: reason}
		if err := source.Validate(); err != nil {
			t.Fatalf("Validate(%q): %v", reason, err)
		}
		if !errors.Is(source, ErrManualCompactionAdmission) {
			t.Fatalf("errors.Is(%q) = false", reason)
		}
		if source.RPCErrorCode() != protocol.ErrCodeManualCompactionAdmission {
			t.Fatalf("RPCErrorCode(%q) = %d, want %d", reason, source.RPCErrorCode(), protocol.ErrCodeManualCompactionAdmission)
		}

		var payload map[string]any
		if err := json.Unmarshal(source.RPCErrorData(), &payload); err != nil {
			t.Fatalf("Unmarshal(%q): %v", reason, err)
		}
		if payload["type"] != "manual_compaction_admission_error" || payload["reason"] != string(reason) {
			t.Fatalf("RPC data(%q) = %+v", reason, payload)
		}
		if _, ok := payload["retry_hint"]; ok {
			t.Fatalf("RPC data(%q) contains a retry hint: %+v", reason, payload)
		}
		if _, ok := payload["message"]; ok {
			t.Fatalf("RPC data(%q) contains display copy: %+v", reason, payload)
		}

		decoded := DecodeManualCompactionAdmissionError(source.RPCErrorData(), "fallback display text")
		var typed *ManualCompactionAdmissionError
		if !errors.As(decoded, &typed) || typed.Reason != reason {
			t.Fatalf("Decode(%q) = %T %+v", reason, decoded, decoded)
		}
	}
}

func TestManualCompactionAdmissionErrorRejectsInvalidReasons(t *testing.T) {
	for _, reason := range []ManualCompactionAdmissionReason{"", "retry_later"} {
		if err := (&ManualCompactionAdmissionError{Reason: reason}).Validate(); err == nil {
			t.Fatalf("Validate(%q) = nil, want error", reason)
		}
	}
}

func TestDecodeManualCompactionAdmissionErrorUsesGenericFallbackForMalformedData(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"type":"manual_compaction_admission_error"}`,
		`{"type":"other","reason":"too_soon"}`,
		`{"type":"manual_compaction_admission_error","reason":"retry_later"}`,
		`{"type":"manual_compaction_admission_error","reason":"too_soon","message":"retry now"}`,
		`{"type":"manual_compaction_admission_error","reason":"too_soon"} trailing`,
	} {
		err := DecodeManualCompactionAdmissionError(json.RawMessage(raw), "fallback display text")
		var typed *ManualCompactionAdmissionError
		if errors.As(err, &typed) {
			t.Fatalf("Decode(%s) = typed %+v, want fallback", raw, typed)
		}
		if err == nil || err.Error() != "fallback display text" {
			t.Fatalf("Decode(%s) = %v, want fallback display text", raw, err)
		}
	}
}
