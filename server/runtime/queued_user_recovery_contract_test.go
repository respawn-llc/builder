package runtime

import (
	"testing"

	"core/server/tools"
)

func TestDiscardQueuedUserMessagePreservesOtherQueuedIdentities(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
	})
	first := mustQueueUserMessage(t, engine, "same")
	target := mustQueueUserMessage(t, engine, "same")
	last := mustQueueUserMessage(t, engine, "same")
	if pending := engine.boundaryAgenda.pendingHuman(); len(pending) != 3 {
		t.Fatalf("canonical human agenda entries = %+v, want three", pending)
	}

	if !engine.DiscardQueuedUserMessage(target.ID) {
		t.Fatal("target queued identity was not discarded")
	}
	queue := engine.transcriptHydrationSegmentLocked().QueuedMessages
	if len(queue) != 2 || queue[0].ID != first.ID || queue[1].ID != last.ID {
		t.Fatalf("remaining queued identities = %+v", queue)
	}
}
