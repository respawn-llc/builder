package session

import (
	"os"
	"testing"
)

func TestAppendEventWithEndByteCursorReturnsReadableCommittedPosition(t *testing.T) {
	store := newSessionTestStore(t)

	appended, err := store.AppendEventWithEndByteCursor(
		"step-1",
		"message",
		map[string]any{"role": "user", "content": "located"},
	)
	if err != nil {
		t.Fatalf("append positioned event: %v", err)
	}
	if !appended.Committed {
		t.Fatal("positioned event was not committed")
	}
	if appended.Event.Seq <= 0 || appended.EndByteCursor == nil || *appended.EndByteCursor <= 0 {
		t.Fatalf("positioned append = %#v, want positive sequence and end-byte cursor", appended)
	}

	window, err := store.ReadSegmentBackward(*appended.EndByteCursor, nil)
	if err != nil {
		t.Fatalf("read through positioned cursor: %v", err)
	}
	if len(window.Events) != 1 || window.Events[0].Seq != appended.Event.Seq {
		t.Fatalf("events at positioned cursor = %#v, want appended event sequence %d", window.Events, appended.Event.Seq)
	}
	if window.EndOffset != *appended.EndByteCursor {
		t.Fatalf("read end offset = %d, want append cursor %d", window.EndOffset, *appended.EndByteCursor)
	}
}

func TestAppendEventWithEndByteCursorRetainsPositionWhenObserverFailsAfterCommit(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		testSessionCategory,
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("create observed store: %v", err)
	}
	observer.err = os.ErrPermission

	appended, err := store.AppendEventWithEndByteCursor(
		"step-1",
		"message",
		map[string]any{"role": "user", "content": "committed despite observer failure"},
	)
	if err == nil {
		t.Fatal("positioned append did not surface observer failure")
	}
	if !appended.Committed || appended.EndByteCursor == nil || *appended.EndByteCursor <= 0 {
		t.Fatalf("positioned append = %#v, want committed event with retained cursor", appended)
	}
	window, readErr := store.ReadSegmentBackward(*appended.EndByteCursor, nil)
	if readErr != nil {
		t.Fatalf("read committed event after observer failure: %v", readErr)
	}
	if len(window.Events) != 1 || window.Events[0].Seq != appended.Event.Seq {
		t.Fatalf("events after observer failure = %#v, want committed event %d", window.Events, appended.Event.Seq)
	}
}
