package session

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func userMessagePayload(t *testing.T, content string) map[string]any {
	t.Helper()
	return map[string]any{"role": "user", "content": content}
}

func appendForkTestEvents(t *testing.T, store *Store, userMessages int, perMessageFiller int) {
	t.Helper()
	for i := 0; i < userMessages; i++ {
		if _, _, err := store.AppendEvent("step", "message", userMessagePayload(t, "prompt")); err != nil {
			t.Fatalf("append user message %d: %v", i, err)
		}
		for f := 0; f < perMessageFiller; f++ {
			if _, _, err := store.AppendEvent("step", "tool_completed", map[string]any{"n": f}); err != nil {
				t.Fatalf("append filler %d/%d: %v", i, f, err)
			}
		}
	}
}

func replayShapesEqual(left, right []Event) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Kind != right[i].Kind || left[i].StepID != right[i].StepID {
			return false
		}
		if !bytes.Equal([]byte(left[i].Payload), []byte(right[i].Payload)) {
			return false
		}
	}
	return true
}

func withTinyForkChunks(t *testing.T) {
	t.Helper()
	prevCount := forkReplayFlushEventCount
	prevBytes := forkReplayFlushByteBudget
	forkReplayFlushEventCount = 3
	forkReplayFlushByteBudget = 1 << 30
	t.Cleanup(func() {
		forkReplayFlushEventCount = prevCount
		forkReplayFlushByteBudget = prevBytes
	})
}

func TestForkAtUserMessageStreamsPrefixAcrossChunks(t *testing.T) {
	withTinyForkChunks(t)
	parent := newSessionTestStore(t)
	appendForkTestEvents(t, parent, 4, 3)

	parentEvents, err := collectEvents(parent)
	if err != nil {
		t.Fatalf("collect parent events: %v", err)
	}
	const forkIndex = 3
	expected := make([]Event, 0)
	visible := 0
	var forkSeq int64
	for _, evt := range parentEvents {
		if hasVisibleUserMessageEvent(evt.Kind, evt.Payload) {
			visible++
			if visible == forkIndex {
				forkSeq = evt.Seq
				break
			}
		}
		expected = append(expected, evt)
	}
	if len(expected) <= forkReplayFlushEventCount {
		t.Fatalf("test requires fork prefix (%d) to span multiple chunks (%d)", len(expected), forkReplayFlushEventCount)
	}

	child, ordinal, err := ForkAtUserMessage(parent, forkSeq, "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("fork at user message: %v", err)
	}
	if ordinal != forkIndex {
		t.Fatalf("fork ordinal = %d, want %d", ordinal, forkIndex)
	}
	childEvents, err := collectEvents(child)
	if err != nil {
		t.Fatalf("collect child events: %v", err)
	}
	if !replayShapesEqual(expected, childEvents) {
		t.Fatalf("fork child must replay the prefix before user message %d: want %d events, got %d", forkIndex, len(expected), len(childEvents))
	}
}

func assertOnlyForkParentSessionDir(t *testing.T, root string, parent *Store) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read container dir: %v", err)
	}
	sessionDirs := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			sessionDirs = append(sessionDirs, entry.Name())
		}
	}
	if len(sessionDirs) != 1 || sessionDirs[0] != parent.Meta().SessionID {
		t.Fatalf("expected only the parent session dir to remain, got %v", sessionDirs)
	}
	if _, err := os.Stat(filepath.Join(root, parent.Meta().SessionID)); err != nil {
		t.Fatalf("parent session dir must survive a failed fork: %v", err)
	}
}
