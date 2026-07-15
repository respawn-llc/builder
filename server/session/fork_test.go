package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/shared/sessioncontract"
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

func TestCloneSessionStreamsLargeHistoryAcrossChunks(t *testing.T) {
	withTinyForkChunks(t)
	parent := newSessionTestStore(t)
	appendForkTestEvents(t, parent, 6, 4)
	if _, _, err := parent.AppendEvent("step", "message", map[string]any{"role": "developer", "message_type": "headless_mode", "content": "x"}); err != nil {
		t.Fatalf("append headless marker: %v", err)
	}

	parentEvents, err := collectEvents(parent)
	if err != nil {
		t.Fatalf("collect parent events: %v", err)
	}
	if len(parentEvents) <= forkReplayFlushEventCount {
		t.Fatalf("test requires more events (%d) than one chunk (%d)", len(parentEvents), forkReplayFlushEventCount)
	}

	child, err := CloneSession(parent, "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("clone session: %v", err)
	}
	childEvents, err := collectEvents(child)
	if err != nil {
		t.Fatalf("collect child events: %v", err)
	}
	if !replayShapesEqual(parentEvents, childEvents) {
		t.Fatalf("cloned child must replay the full parent history: parent=%d child=%d", len(parentEvents), len(childEvents))
	}
	if child.Meta().ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("child parent id = %q, want %q", child.Meta().ParentSessionID, parent.Meta().SessionID)
	}
	if !child.Meta().HeadlessActive {
		t.Fatal("expected cloned child to inherit headless-active state derived from replay")
	}
}

func TestStreamedForkAndCloneRequireAndPersistCategory(t *testing.T) {
	parent, err := Create(t.TempDir(), "workspace", "/tmp/work", sessioncontract.SessionCategoryMain, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	target, _, err := parent.AppendEvent("step", "message", userMessagePayload(t, "fork target"))
	if err != nil {
		t.Fatalf("append fork target: %v", err)
	}

	forked, _, err := ForkAtUserMessage(
		parent,
		target.Seq,
		"interactive fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("fork session: %v", err)
	}
	if got := forked.Meta().Category; got == nil || *got != sessioncontract.SessionCategoryMain {
		t.Fatalf("fork category = %v, want main", got)
	}

	cloned, err := CloneSession(parent, "workflow clone", sessioncontract.SessionCategorySubagent)
	if err != nil {
		t.Fatalf("clone session: %v", err)
	}
	if got := cloned.Meta().Category; got == nil || *got != sessioncontract.SessionCategorySubagent {
		t.Fatalf("clone category = %v, want subagent", got)
	}
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

func TestForkAtUserMessageOutOfRangeCleansUpChild(t *testing.T) {
	withTinyForkChunks(t)
	root := t.TempDir()
	parent := newSessionTestStoreAt(t, root)
	appendForkTestEvents(t, parent, 2, 4)

	if _, _, err := ForkAtUserMessage(parent, 999999, "fork", testSessionCategory); err == nil {
		t.Fatal("expected out-of-range fork to fail")
	}
	assertOnlyForkParentSessionDir(t, root, parent)
}

func TestForkAtUserMessageRejectsMalformedHistoryReplacement(t *testing.T) {
	root := t.TempDir()
	parent := newSessionTestStoreAt(t, root)
	if _, _, err := parent.AppendEvent("step", "message", userMessagePayload(t, "before malformed history")); err != nil {
		t.Fatalf("append first user message: %v", err)
	}
	if _, _, err := parent.AppendEvent("step", "history_replaced", "malformed history replacement"); err != nil {
		t.Fatalf("append malformed history replacement: %v", err)
	}
	target, _, err := parent.AppendEvent("step", "message", userMessagePayload(t, "fork target"))
	if err != nil {
		t.Fatalf("append fork target: %v", err)
	}

	if _, _, err := ForkAtUserMessage(parent, target.Seq, "fork", testSessionCategory); !errors.Is(err, errDecodeForkHistoryReplacement) {
		t.Fatalf("fork error = %v, want %v", err, errDecodeForkHistoryReplacement)
	}
	assertOnlyForkParentSessionDir(t, root, parent)
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

func TestCloneSessionWithoutEventsPersistsEmptyChild(t *testing.T) {
	parent := newSessionTestStore(t)
	child, err := CloneSession(parent, "clone", testSessionCategory)
	if err != nil {
		t.Fatalf("clone empty session: %v", err)
	}
	events, err := collectEvents(child)
	if err != nil {
		t.Fatalf("collect child events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected empty cloned child, got %d events", len(events))
	}
	if _, err := os.Stat(filepath.Join(child.Dir(), eventsFile)); err != nil {
		t.Fatalf("empty cloned child must be durable: %v", err)
	}
	if child.Meta().ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("child parent id = %q, want %q", child.Meta().ParentSessionID, parent.Meta().SessionID)
	}
}
