package runtime

import (
	"strings"
	"testing"

	"core/server/llm"
	"core/server/tools"
)

func steerUserMessageForCacheTest(t *testing.T, eng *Engine, content string) {
	t.Helper()
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: content}})); err != nil {
		t.Fatalf("steer %q: %v", content, err)
	}
}

func TestRecentTailTranscriptWindowCacheInvalidatesOnCommittedEvent(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	steerUserMessageForCacheTest(t, eng, "one")
	first := eng.RecentTailTranscriptWindow(50)
	cached := eng.RecentTailTranscriptWindow(50)
	if len(cached.Snapshot.Entries) != len(first.Snapshot.Entries) || cached.TotalEntries != first.TotalEntries {
		t.Fatalf("cache hit changed result: first=%d/%d cached=%d/%d", len(first.Snapshot.Entries), first.TotalEntries, len(cached.Snapshot.Entries), cached.TotalEntries)
	}

	steerUserMessageForCacheTest(t, eng, "two")
	updated := eng.RecentTailTranscriptWindow(50)
	if updated.TotalEntries <= first.TotalEntries {
		t.Fatalf("a newly committed event must invalidate the cache: before=%d after=%d", first.TotalEntries, updated.TotalEntries)
	}
}

func TestRecentTailTranscriptWindowCacheReflectsLiveStreamingOnHit(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})

	steerUserMessageForCacheTest(t, eng, "one")
	if got := strings.TrimSpace(eng.RecentTailTranscriptWindow(50).Snapshot.Streaming); got != "" {
		t.Fatalf("unexpected streaming before delta: %q", got)
	}

	newTranscriptPersistenceCoordinator(eng.transcriptRuntimeState()).AppendStreamingDelta("step-1", eng.TranscriptRevision(), eng.CommittedTranscriptEntryCount(), "partial answer", llm.MessagePhaseFinal)
	got := eng.RecentTailTranscriptWindow(50).Snapshot.Streaming
	if strings.TrimSpace(got) != "partial answer" {
		t.Fatalf("cache hit must overlay live streaming, got %q", got)
	}
}
