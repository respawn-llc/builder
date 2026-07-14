package sessionview

import (
	"context"
	"fmt"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/shared/serverapi"
)

func TestDormantTranscriptCacheReusesEntryForUnchangedRevision(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	buildCalls := 0
	cache := newDormantTranscriptCacheWithLimit(dormantTranscriptCacheMaxEntries, func(_ context.Context, store *session.Store) (dormantTranscriptCacheEntry, error) {
		buildCalls++
		meta := store.Meta()
		return dormantTranscriptCacheEntry{
			sessionDir:                   store.Dir(),
			sessionID:                    meta.SessionID,
			revision:                     meta.LastSequence,
			lastCommittedAssistantAnswer: "done",
			newestSegment:                dormantSegmentFixture([]string{"tail-1", "tail-2"}),
		}, nil
	})

	entry, err := cache.get(context.Background(), store)
	if err != nil {
		t.Fatalf("cache get: %v", err)
	}
	if len(entry.newestSegment.Snapshot.Entries) != 2 || entry.lastCommittedAssistantAnswer != "done" {
		t.Fatalf("unexpected cache entry: %+v", entry)
	}
	entry, err = cache.get(context.Background(), store)
	if err != nil {
		t.Fatalf("cache get second time: %v", err)
	}
	if len(entry.newestSegment.Snapshot.Entries) != 2 {
		t.Fatalf("unexpected second cache entry: %+v", entry)
	}
	if buildCalls != 1 {
		t.Fatalf("build calls = %d, want 1", buildCalls)
	}
}

func TestDormantTranscriptCacheInvalidatesOnRevisionAdvance(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "hello"}); err != nil {
		t.Fatalf("append first message: %v", err)
	}

	buildCalls := 0
	cache := newDormantTranscriptCacheWithLimit(dormantTranscriptCacheMaxEntries, func(_ context.Context, store *session.Store) (dormantTranscriptCacheEntry, error) {
		buildCalls++
		meta := store.Meta()
		return dormantTranscriptCacheEntry{
			sessionDir: store.Dir(),
			sessionID:  meta.SessionID,
			revision:   meta.LastSequence,
		}, nil
	})

	if _, err := cache.get(context.Background(), store); err != nil {
		t.Fatalf("cache get: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal}); err != nil {
		t.Fatalf("append second message: %v", err)
	}
	if _, err := cache.get(context.Background(), store); err != nil {
		t.Fatalf("cache get after revision advance: %v", err)
	}
	if buildCalls != 2 {
		t.Fatalf("build calls = %d, want 2", buildCalls)
	}
}

func TestDormantTranscriptCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	root := t.TempDir()
	storeA := newSessionViewStore(t, root, "ws", root)
	storeB := newSessionViewStore(t, root, "ws", root)
	storeC := newSessionViewStore(t, root, "ws", root)
	buildCalls := 0
	cache := newDormantTranscriptCacheWithLimit(2, func(_ context.Context, store *session.Store) (dormantTranscriptCacheEntry, error) {
		buildCalls++
		meta := store.Meta()
		return dormantTranscriptCacheEntry{sessionDir: store.Dir(), sessionID: meta.SessionID, revision: meta.LastSequence}, nil
	})

	if _, err := cache.get(context.Background(), storeA); err != nil {
		t.Fatalf("cache get A: %v", err)
	}
	if _, err := cache.get(context.Background(), storeB); err != nil {
		t.Fatalf("cache get B: %v", err)
	}
	if _, err := cache.get(context.Background(), storeA); err != nil {
		t.Fatalf("cache re-get A: %v", err)
	}
	if _, err := cache.get(context.Background(), storeC); err != nil {
		t.Fatalf("cache get C: %v", err)
	}
	if _, err := cache.get(context.Background(), storeB); err != nil {
		t.Fatalf("cache re-get B after eviction: %v", err)
	}
	if buildCalls != 4 {
		t.Fatalf("build calls = %d, want 4", buildCalls)
	}
}

func TestServiceUsesDormantCacheForMainViewAndTailEntries(t *testing.T) {
	dir := t.TempDir()
	store := newSessionViewStore(t, dir, "ws", dir)
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("set name: %v", err)
	}
	if _, _, err := store.AppendEvent("step-1", "message", llm.Message{Role: llm.RoleUser, Content: "seed"}); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	buildCalls := 0
	cache := newDormantTranscriptCacheWithLimit(dormantTranscriptCacheMaxEntries, func(_ context.Context, store *session.Store) (dormantTranscriptCacheEntry, error) {
		buildCalls++
		meta := store.Meta()
		return dormantTranscriptCacheEntry{
			sessionDir:                   store.Dir(),
			sessionID:                    meta.SessionID,
			revision:                     meta.LastSequence,
			lastCommittedAssistantAnswer: "done",
			newestSegment:                dormantSegmentFixture(buildTailTexts(0, 500)),
		}, nil
	})

	dormantSource := newDormantSessionSnapshotSource(nil)
	dormantSource.dormant = cache
	svc := &Service{snapshots: &resolvedSessionSnapshotSource{sessions: NewStaticSessionResolver(store), dormant: dormantSource}}
	mainViewResp, err := svc.GetSessionMainView(context.Background(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("get session main view: %v", err)
	}
	if got := mainViewResp.MainView.Status.LastCommittedAssistantFinalAnswer; got != "done" {
		t.Fatalf("last committed assistant final answer = %q, want done", got)
	}

	entries, err := svc.SessionTranscriptTailEntries(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("get transcript tail entries: %v", err)
	}
	if got := len(entries); got != 500 {
		t.Fatalf("default newest-segment entry count = %d, want 500", got)
	}
	if entries[0].Text != "line 0" {
		t.Fatalf("unexpected first entry: %q", entries[0].Text)
	}
	if buildCalls != 1 {
		t.Fatalf("build calls = %d, want 1", buildCalls)
	}
}

func dormantSegmentFixture(texts []string) runtime.TranscriptSegmentPage {
	entries := make([]runtime.ChatEntry, 0, len(texts))
	for _, text := range texts {
		entries = append(entries, runtime.ChatEntry{Role: "assistant", Text: text})
	}
	return runtime.TranscriptSegmentPage{Snapshot: runtime.ChatSnapshot{Entries: entries}}
}

func buildTailTexts(offset, count int) []string {
	texts := make([]string, 0, count)
	for i := 0; i < count; i++ {
		texts = append(texts, fmt.Sprintf("line %d", offset+i))
	}
	return texts
}
