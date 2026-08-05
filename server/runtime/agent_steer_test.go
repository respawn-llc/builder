package runtime

import (
	"testing"

	"core/server/llm"
	"core/shared/runtimeids"
)

func TestNewAgentSteerBuildsTypedDeveloperMessage(t *testing.T) {
	sourceID := runtimeids.NewSessionID()
	steer, err := NewAgentSteer(sourceID, "inspect the migration\nthen report back")
	if err != nil {
		t.Fatalf("NewAgentSteer: %v", err)
	}
	message := steer.Message()
	if message.Role != llm.RoleDeveloper {
		t.Fatalf("message role = %q, want developer", message.Role)
	}
	if message.MessageType == nil || *message.MessageType != llm.MessageTypeAgentSteer {
		t.Fatalf("message type = %v, want agent_steer", message.MessageType)
	}
	if steer.SourceSessionID() != sourceID || message.Content == nil || *message.Content == "" {
		t.Fatalf("message provenance/content = %q/%v", steer.SourceSessionID(), message.Content)
	}
}

func TestNewAgentSteerRejectsMissingSourceOrText(t *testing.T) {
	sourceID := runtimeids.NewSessionID()
	for _, tc := range []struct {
		name   string
		source runtimeids.SessionID
		text   string
	}{
		{name: "missing source", text: "message"},
		{name: "missing text", source: sourceID, text: " \t "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewAgentSteer(tc.source, tc.text); err == nil {
				t.Fatal("NewAgentSteer unexpectedly succeeded")
			}
		})
	}
}
