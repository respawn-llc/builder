package serverapi

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSessionPersistInputDraftMarshalsInertRecoveryBuffers(t *testing.T) {
	req := SessionPersistInputDraftRequest{
		SessionID: "session-1",
		Input:     "visible draft",
		RecoveryBuffers: []SessionDraftRecoveryBuffer{{
			Kind: SessionDraftRecoveryBufferActiveSubmit,
			Text: "submitted before forced exit",
		}},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("Unmarshal payload: %v", err)
	}
	recoveryBuffers, ok := payload["recovery_buffers"].([]any)
	if !ok || len(recoveryBuffers) != 1 {
		t.Fatalf("recovery_buffers = %#v, want one entry", payload["recovery_buffers"])
	}
	got, ok := recoveryBuffers[0].(map[string]any)
	if !ok {
		t.Fatalf("recovery buffer = %#v, want JSON object", recoveryBuffers[0])
	}
	want := map[string]any{
		"kind": string(SessionDraftRecoveryBufferActiveSubmit),
		"text": "submitted before forced exit",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovery buffer = %#v, want category/text only %#v", got, want)
	}
}

func TestSessionPersistInputDraftRejectsInvalidRecoveryBuffer(t *testing.T) {
	for _, req := range []SessionPersistInputDraftRequest{
		{SessionID: "session-1", RecoveryBuffers: []SessionDraftRecoveryBuffer{{Text: "missing kind"}}},
		{SessionID: "session-1", RecoveryBuffers: []SessionDraftRecoveryBuffer{{Kind: SessionDraftRecoveryBufferQueuedInput}}},
	} {
		if err := req.Validate(); err == nil {
			t.Fatalf("Validate(%+v) = nil, want error", req)
		}
	}
}
