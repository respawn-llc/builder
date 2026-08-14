package serverapi

import (
	"encoding/json"
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/runtimeids"
)

func TestChatSettingsReadContract(t *testing.T) {
	sessionID := mustChatSettingsSessionID(t, "session-1")
	for _, request := range []ChatSettingsReadRequest{
		{Target: LazyChatSettingsTarget("project-1", "workspace-1")},
		{Target: SessionChatSettingsTarget(sessionID)},
	} {
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded ChatSettingsReadRequest
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if decoded.Target.TargetKind != request.Target.TargetKind {
			t.Fatalf("target kind = %q, want %q", decoded.Target.TargetKind, request.Target.TargetKind)
		}
	}
	for _, raw := range []string{
		`{}`,
		`{"target":{"kind":"lazy","project_id":"project-1"}}`,
		`{"target":{"kind":"session","session_id":"session-1","project_id":"project-1"}}`,
		`{"target":{"kind":"session","session_id":"../escape"}}`,
	} {
		var request ChatSettingsReadRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Fatalf("Unmarshal(%s) unexpectedly succeeded", raw)
		}
	}

	lazy := LazyChatSettingsTarget("project-1", "workspace-1")
	if err := (ChatSettingsReadResponse{}).ValidateForTarget(lazy); err != nil {
		t.Fatalf("lazy response: %v", err)
	}
	if err := (ChatSettingsReadResponse{
		Session: &ChatSettingsSessionFacts{SessionID: sessionID},
	}).ValidateForTarget(lazy); err == nil {
		t.Fatal("lazy response accepted Session facts")
	}
	session := SessionChatSettingsTarget(sessionID)
	if err := (ChatSettingsReadResponse{}).ValidateForTarget(session); err == nil {
		t.Fatal("materialized response accepted absent Session facts")
	}
	if err := (ChatSettingsReadResponse{
		Session: &ChatSettingsSessionFacts{SessionID: sessionID},
	}).ValidateForTarget(session); err != nil {
		t.Fatalf("materialized response: %v", err)
	}
}

func TestChatSettingsAgentPreparationErrorContract(t *testing.T) {
	for _, category := range []ChatSettingsAgentPreparationCategory{
		ChatSettingsAgentInvalidConfiguration,
		ChatSettingsAgentProviderUnavailable,
		ChatSettingsAgentInternalPreparation,
	} {
		source := &ChatSettingsAgentPreparationError{Agent: "reviewer", Category: category}
		if source.RPCErrorCode() != protocol.ErrCodeChatSettingsAgentPreparation {
			t.Fatalf("RPCErrorCode = %d", source.RPCErrorCode())
		}
		var decoded *ChatSettingsAgentPreparationError
		if !errors.As(
			DecodeChatSettingsAgentPreparationError(source.RPCErrorData(), ""),
			&decoded,
		) || decoded.Agent != source.Agent || decoded.Category != category {
			t.Fatalf("decoded error = %+v, want %+v", decoded, source)
		}
	}
}

func mustChatSettingsSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
