package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestChatContextRequestAcceptsExactlyOneTarget(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	tests := []struct {
		name string
		body string
	}{
		{name: "workspace Chat", body: `{"target":{"workspace_chat":{}}}`},
		{name: "Session", body: `{"target":{"session":{"session_id":"` + sessionID.String() + `"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request ChatContextRequest
			if err := json.Unmarshal([]byte(test.body), &request); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			if err := request.Validate(); err != nil {
				t.Fatalf("validate request: %v", err)
			}
		})
	}
}

func TestChatContextRequestRejectsMalformedTargets(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	tests := []struct {
		name string
		body string
	}{
		{name: "missing target", body: `{}`},
		{name: "null target", body: `{"target":null}`},
		{name: "missing arm", body: `{"target":{}}`},
		{name: "both arms", body: `{"target":{"workspace_chat":{},"session":{"session_id":"` + sessionID.String() + `"}}}`},
		{name: "noncanonical Session id", body: `{"target":{"session":{"session_id":"session-1"}}}`},
		{name: "unknown request field", body: `{"target":{"workspace_chat":{}},"unknown":true}`},
		{name: "unknown target field", body: `{"target":{"workspace_chat":{},"unknown":true}}`},
		{name: "workspace payload field", body: `{"target":{"workspace_chat":{"session_id":"` + sessionID.String() + `"}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var request ChatContextRequest
			if err := json.Unmarshal([]byte(test.body), &request); err == nil {
				t.Fatal("unmarshal request succeeded")
			}
		})
	}
}

func TestChatContextRequestTargetAccessorsAreExclusive(t *testing.T) {
	workspaceRequest := NewWorkspaceChatContextRequest()
	if !workspaceRequest.Target.IsWorkspaceChat() {
		t.Fatal("workspace target is not workspace Chat")
	}
	if _, ok := workspaceRequest.Target.SessionID(); ok {
		t.Fatal("workspace target exposed a Session id")
	}

	sessionID := runtimeids.NewSessionID()
	sessionRequest := NewSessionChatContextRequest(sessionID)
	if sessionRequest.Target.IsWorkspaceChat() {
		t.Fatal("Session target is workspace Chat")
	}
	if got, ok := sessionRequest.Target.SessionID(); !ok || got != sessionID {
		t.Fatalf("Session id = (%v, %v), want (%v, true)", got, ok, sessionID)
	}
}

func TestChatContextResponseValidatesAllCompactionModes(t *testing.T) {
	for _, mode := range []ChatContextCompactionMode{
		ChatContextCompactionModeDisabled,
		ChatContextCompactionModeLocal,
		ChatContextCompactionModeProviderNative,
	} {
		t.Run(string(mode), func(t *testing.T) {
			response := validChatContextResponse()
			response.Context.CompactionMode = mode
			if err := response.Validate(); err != nil {
				t.Fatalf("validate response: %v", err)
			}
		})
	}
}

func TestChatContextResponseRejectsInvalidFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ChatContext)
	}{
		{name: "non-positive window", mutate: func(context *ChatContext) { context.ContextWindowTokens = 0 }},
		{name: "negative used", mutate: func(context *ChatContext) { context.UsedTokens = -1 }},
		{name: "negative threshold", mutate: func(context *ChatContext) { context.AutomaticThresholdTokens = -1 }},
		{name: "threshold above window", mutate: func(context *ChatContext) { context.AutomaticThresholdTokens = 101 }},
		{name: "negative completed count", mutate: func(context *ChatContext) { context.CompletedCompactionCount = -1 }},
		{name: "unknown mode", mutate: func(context *ChatContext) { context.CompactionMode = "future" }},
		{name: "invalid remaining relation", mutate: func(context *ChatContext) { context.RemainingTokens++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := validChatContextResponse()
			test.mutate(&response.Context)
			if err := response.Validate(); err == nil {
				t.Fatal("validate response succeeded")
			}
		})
	}
}

func TestChatContextResponseAllowsOverWindowUsage(t *testing.T) {
	response := validChatContextResponse()
	response.Context.UsedTokens = 125
	response.Context.RemainingTokens = -25
	if err := response.Validate(); err != nil {
		t.Fatalf("validate response: %v", err)
	}
}

func validChatContextResponse() ChatContextResponse {
	return ChatContextResponse{Context: ChatContext{
		ContextWindowTokens:      100,
		UsedTokens:               40,
		RemainingTokens:          60,
		AutomaticThresholdTokens: 80,
		AutoCompactionEnabled:    true,
		CompactionMode:           ChatContextCompactionModeLocal,
		CompletedCompactionCount: 2,
		CompactionRunning:        false,
		ManualCompactAvailable:   true,
	}}
}
