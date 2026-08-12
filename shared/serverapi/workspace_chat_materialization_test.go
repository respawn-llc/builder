package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestWorkspaceChatMaterializationContractIsTriggerAgnostic(t *testing.T) {
	var request WorkspaceChatMaterializeRequest
	if err := json.Unmarshal([]byte(`{}`), &request); err != nil {
		t.Fatalf("decode empty materialization request: %v", err)
	}

	for name, payload := range map[string]string{
		"unknown":          `{"unknown":true}`,
		"text":             `{"text":"hello"}`,
		"prompt command":   `{"prompt_command":{"name":"review"}}`,
		"goal":             `{"goal":"ship it"}`,
		"trigger":          `{"trigger":"send"}`,
		"request identity": `{"client_request_id":"request-1"}`,
		"session identity": `{"session_id":"7fd3bc93-f11c-4814-87d0-b60f10e6dd5c"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := json.Unmarshal([]byte(payload), &request); err == nil {
				t.Fatalf("materialization request accepted %s field", name)
			}
		})
	}
}

func TestWorkspaceChatMaterializationResponseRequiresCanonicalUUIDv4(t *testing.T) {
	canonical := runtimeids.NewSessionID()
	response := WorkspaceChatMaterializeResponse{SessionID: canonical}
	if err := response.Validate(); err != nil {
		t.Fatalf("validate canonical response: %v", err)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var decoded WorkspaceChatMaterializeResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("round-trip response: %v", err)
	}
	if decoded.SessionID != canonical {
		t.Fatalf("round-trip Session identity = %q, want %q", decoded.SessionID.String(), canonical.String())
	}

	legacy, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("parse legacy Session identity: %v", err)
	}
	if err := (WorkspaceChatMaterializeResponse{SessionID: legacy}).Validate(); err == nil {
		t.Fatal("materialization response accepted a non-UUIDv4 Session identity")
	}
}

func TestWorkspaceChatDraftConsumeOperationIsRejected(t *testing.T) {
	var request WorkspaceChatDraftRequest
	if err := json.Unmarshal([]byte(`{"operation":{"kind":"consume"}}`), &request); err == nil {
		t.Fatal("workspace Chat draft accepted the removed consume operation")
	}
}
