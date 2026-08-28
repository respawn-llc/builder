package runtime

import (
	"testing"
)

func TestQueuedUserMessageOwnerDiscardPreservesOtherQueuedIdentities(t *testing.T) {
	t.Parallel()
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{
		Model: "gpt-5",
	})
	first := mustQueueUserMessage(t, engine, "same")
	target := mustQueueUserMessage(t, engine, "same")
	last := mustQueueUserMessage(t, engine, "same")

	if _, removed := engine.messageFlow.DiscardQueuedUserMessage(target.ID); !removed {
		t.Fatal("target queued identity was not discarded")
	}
	queue := engine.messageFlow.(*defaultMessageLifecycle).queue.Snapshot()
	if len(queue) != 2 || queue[0].ID != first.ID || queue[1].ID != last.ID {
		t.Fatalf("remaining queued identities = %+v", queue)
	}
}
