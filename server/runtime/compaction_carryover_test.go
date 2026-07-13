package runtime

import (
	"testing"

	"core/prompts"
	"core/server/llm"
)

func TestHandoffFutureAgentMessageWrapsContentForModelAndTranscript(t *testing.T) {
	msg := handoffFutureAgentMessage(" resume with tests ")

	if got, want := msg.Content, prompts.FormatHandoffFutureAgentMessage("resume with tests"); got != want {
		t.Fatalf("future-agent message content = %q, want %q", got, want)
	}
}

func TestPostCompactionMessagesSkipsEmptyHandoffFutureMessage(t *testing.T) {
	state := newHandoffRuntimeState()
	state.QueueRequest("keep API details", " \n\t ")
	eng := &Engine{handoffState: state}

	messages := newCompactionCarryoverCoordinator(eng).postCompactionMessages(compactionModeHandoff, "")
	for _, message := range messages {
		if message.message.MessageType == llm.MessageTypeHandoffFutureMessage {
			t.Fatalf("did not expect empty handoff future message carryover: %+v", message)
		}
	}
}
