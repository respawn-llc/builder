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
	first := engine.QueueUserMessage("same")
	target := engine.QueueUserMessage("same")
	last := engine.QueueUserMessage("same")

	if !engine.DiscardQueuedUserMessage(target.ID) {
		t.Fatal("target queued identity was not discarded")
	}
	queue := engine.messageFlow.(*defaultMessageLifecycle).queue.Snapshot()
	if len(queue) != 2 || queue[0].ID != first.ID || queue[1].ID != last.ID {
		t.Fatalf("remaining queued identities = %+v", queue)
	}
}
