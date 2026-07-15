package runtime

import (
	"testing"

	"core/prompts"
)

func TestHandoffFutureAgentMessageWrapsContentForModelAndTranscript(t *testing.T) {
	msg, ok := handoffFutureAgentMessage(" resume with tests ")
	if !ok {
		t.Fatal("expected non-empty handoff future message")
	}

	if got, want := msg.Content, prompts.FormatHandoffFutureAgentMessage("resume with tests"); got != want {
		t.Fatalf("future-agent message content = %q, want %q", got, want)
	}
}

func TestHandoffFutureAgentMessageRejectsEmptyContent(t *testing.T) {
	if _, ok := handoffFutureAgentMessage(" \n\t "); ok {
		t.Fatal("did not expect empty handoff future message")
	}
}
