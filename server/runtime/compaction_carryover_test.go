package runtime

import (
	"testing"

	"core/prompts"
	"core/server/llm"
)

func TestHandoffFutureAgentMessageProducesTypedDeveloperCarryover(t *testing.T) {
	message, ok := handoffFutureAgentMessage(" resume with tests ")
	if !ok {
		t.Fatal("expected non-empty handoff future message")
	}
	if got, want := message.Role, llm.RoleDeveloper; got != want {
		t.Fatalf("role = %q, want %q", got, want)
	}
	if message.MessageType == nil {
		t.Fatal("message type is absent")
	}
	if got, want := *message.MessageType, llm.MessageTypeHandoffFutureMessage; got != want {
		t.Fatalf("message type = %q, want %q", got, want)
	}
	if message.Content == nil {
		t.Fatal("content is absent")
	}
	if got, want := *message.Content, prompts.FormatHandoffFutureAgentMessage("resume with tests"); got != want {
		t.Fatalf("content = %q, want formatted future-agent message", got)
	}
}

func TestHandoffFutureAgentMessageOmitsBlankCarryover(t *testing.T) {
	if _, ok := handoffFutureAgentMessage(" \n\t "); ok {
		t.Fatal("blank future-agent carryover must not produce a message")
	}
}
