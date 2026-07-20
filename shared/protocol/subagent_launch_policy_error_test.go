package protocol

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/runtimeids"
)

func TestSubagentLaunchPolicyErrorRoundTripsMaxDepthIncludingZero(t *testing.T) {
	source := NewMaxDepthExceededSubagentLaunchPolicyError(1, 0)
	if source.RPCErrorCode() != ErrCodeSubagentLaunchPolicy {
		t.Fatalf("RPCErrorCode = %d, want %d", source.RPCErrorCode(), ErrCodeSubagentLaunchPolicy)
	}
	var fields map[string]any
	if err := json.Unmarshal(mustRPCErrorData(t, source), &fields); err != nil {
		t.Fatalf("Unmarshal RPC data: %v", err)
	}
	if fields["kind"] != "max_depth_exceeded" || fields["attempted_depth"] != float64(1) || fields["max_depth"] != float64(0) {
		t.Fatalf("RPC data = %+v", fields)
	}
	decodedErr := DecodeSubagentLaunchPolicyError(mustRPCErrorData(t, source), "fallback")
	var decoded *SubagentLaunchPolicyError
	if !errors.As(decodedErr, &decoded) {
		t.Fatalf("decoded error = %T %v, want SubagentLaunchPolicyError", decodedErr, decodedErr)
	}
	if decoded.Kind != SubagentLaunchPolicyMaxDepthExceeded ||
		decoded.AttemptedDepth == nil || *decoded.AttemptedDepth != 1 ||
		decoded.MaxDepth == nil || *decoded.MaxDepth != 0 {
		t.Fatalf("decoded error = %+v", decoded)
	}
}

func TestSubagentLaunchPolicyErrorRoundTripsLineageCorruption(t *testing.T) {
	repeated := mustPolicySessionID(t, "repeated-session")
	visitedA := mustPolicySessionID(t, "visited-a")
	visitedB := mustPolicySessionID(t, "visited-b")
	source := NewLineageCorruptSubagentLaunchPolicyError(repeated, []runtimeids.SessionID{visitedA, visitedB})
	decodedErr := DecodeSubagentLaunchPolicyError(mustRPCErrorData(t, source), "fallback")
	var decoded *SubagentLaunchPolicyError
	if !errors.As(decodedErr, &decoded) {
		t.Fatalf("decoded error = %T %v, want SubagentLaunchPolicyError", decodedErr, decodedErr)
	}
	if decoded.Kind != SubagentLaunchPolicyLineageCorrupt ||
		decoded.RepeatedSessionID == nil || *decoded.RepeatedSessionID != repeated ||
		len(decoded.VisitedSessionIDs) != 2 ||
		decoded.VisitedSessionIDs[0] != visitedA ||
		decoded.VisitedSessionIDs[1] != visitedB {
		t.Fatalf("decoded error = %+v", decoded)
	}
}

func TestDecodeSubagentLaunchPolicyErrorRejectsInvalidPayloadsWithGenericFallback(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"kind":"unknown"}`,
		`{"kind":"max_depth_exceeded"}`,
		`{"kind":"max_depth_exceeded","attempted_depth":0,"max_depth":0}`,
		`{"kind":"max_depth_exceeded","attempted_depth":3,"max_depth":2,"repeated_session_id":"mixed"}`,
		`{"kind":"lineage_corrupt"}`,
		`{"kind":"lineage_corrupt","repeated_session_id":"repeated","visited_session_ids":[]}`,
		`{"kind":"lineage_corrupt","repeated_session_id":"repeated","visited_session_ids":["repeated"],"max_depth":2}`,
		`{"kind":"lineage_corrupt","repeated_session_id":"../escape","visited_session_ids":["visited"]}`,
		`{"kind":"max_depth_exceeded","attempted_depth":3,"max_depth":2,"unknown":true}`,
	} {
		err := DecodeSubagentLaunchPolicyError(json.RawMessage(raw), "launch rejected")
		var typed *SubagentLaunchPolicyError
		if errors.As(err, &typed) {
			t.Fatalf("Decode(%s) = typed %+v, want generic fallback", raw, typed)
		}
		if err == nil || err.Error() != "launch rejected" {
			t.Fatalf("Decode(%s) = %v, want exact generic fallback", raw, err)
		}
	}
}

func TestDecodeSubagentLaunchPolicyErrorRejectsMalformedTrailingBytes(t *testing.T) {
	source := NewMaxDepthExceededSubagentLaunchPolicyError(1, 0)
	for _, suffix := range []string{" garbage", " {", "\x00"} {
		err := DecodeSubagentLaunchPolicyError(
			append(mustRPCErrorData(t, source), []byte(suffix)...),
			"generic fallback",
		)
		var typed *SubagentLaunchPolicyError
		if errors.As(err, &typed) {
			t.Fatalf("Decode suffix %q = typed %+v, want generic fallback", suffix, typed)
		}
		if err == nil || err.Error() != "generic fallback" {
			t.Fatalf("Decode suffix %q = %v, want generic fallback", suffix, err)
		}
	}
}

func mustPolicySessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
