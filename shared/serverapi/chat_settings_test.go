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
		{Target: NewChatSettingsTarget("project-1", "workspace-1")},
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
		`{"target":{"kind":"new_chat","project_id":"project-1"}}`,
		`{"target":{"kind":"session","session_id":"session-1","project_id":"project-1"}}`,
		`{"target":{"kind":"session","session_id":"../escape"}}`,
	} {
		var request ChatSettingsReadRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Fatalf("Unmarshal(%s) unexpectedly succeeded", raw)
		}
	}

	newChat := NewChatSettingsTarget("project-1", "workspace-1")
	if err := (ChatSettingsReadResponse{}).ValidateForTarget(newChat); err != nil {
		t.Fatalf("New Chat response: %v", err)
	}
	if err := (ChatSettingsReadResponse{
		Session: &ChatSettingsSessionFacts{SessionID: sessionID},
	}).ValidateForTarget(newChat); err == nil {
		t.Fatal("New Chat response accepted Session facts")
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
	taskID := mustChatSettingsTaskID(t, "task-1")
	taskShortID := "KENT-416"
	if err := (ChatSettingsReadResponse{
		Session: &ChatSettingsSessionFacts{
			SessionID:   sessionID,
			TaskID:      &taskID,
			TaskShortID: &taskShortID,
		},
	}).ValidateForTarget(session); err != nil {
		t.Fatalf("materialized response with Task identity: %v", err)
	}
	for name, facts := range map[string]ChatSettingsSessionFacts{
		"Task ID only":       {SessionID: sessionID, TaskID: &taskID},
		"Task Short ID only": {SessionID: sessionID, TaskShortID: &taskShortID},
	} {
		if err := (ChatSettingsReadResponse{Session: &facts}).ValidateForTarget(session); err == nil {
			t.Fatalf("%s response accepted an incomplete Task identity", name)
		}
	}
}

func TestChatSettingsMutationRequiresSession(t *testing.T) {
	sessionID := mustChatSettingsSessionID(t, "session-1")
	enabled := true
	request := ChatSettingsMutationRequest{
		SessionID: sessionID,
		Operation: ChatSettingsMutationOperation{
			Kind:    ChatSettingsMutationQuestions,
			Enabled: &enabled,
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded ChatSettingsMutationRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.SessionID != sessionID {
		t.Fatalf("Session ID = %q, want %q", decoded.SessionID, sessionID)
	}

	for _, raw := range []string{
		`{"operation":{"kind":"questions","enabled":true}}`,
		`{"session_id":"../escape","operation":{"kind":"questions","enabled":true}}`,
		`{"session_id":"session-1","operation":{"kind":"questions","enabled":true},"target":{"kind":"new_chat","project_id":"project-1","workspace_id":"workspace-1"}}`,
	} {
		var request ChatSettingsMutationRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Fatalf("Unmarshal(%s) unexpectedly succeeded", raw)
		}
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

func mustChatSettingsTaskID(t *testing.T, raw string) string {
	t.Helper()
	id, err := runtimeids.ParseTaskID(raw)
	if err != nil {
		t.Fatalf("ParseTaskID(%q): %v", raw, err)
	}
	return id
}
