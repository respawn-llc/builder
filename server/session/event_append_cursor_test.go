package session

import (
	"context"
	"errors"
	"testing"
)

func TestAppendEventWithEndByteCursorReturnsReadableCommittedPosition(t *testing.T) {
	store := newSessionTestStore(t)
	eventLog := mustMaterializeSessionTestEventLog(t, store)

	appended, err := eventLog.AppendRecordWithEndByteCursor(
		stringPointer("step-1"),
		sessionTestMessage(MessageRoleUser, "located"),
	)
	if err != nil {
		t.Fatalf("append positioned event: %v", err)
	}
	if !appended.Committed {
		t.Fatal("positioned event was not committed")
	}
	if appended.Record.Seq() <= 0 || appended.EndByteCursor == nil || *appended.EndByteCursor <= 0 {
		t.Fatalf("positioned append = %#v, want positive sequence and end-byte cursor", appended)
	}

	window, err := eventLog.ReadSegmentBackward(*appended.EndByteCursor, nil)
	if err != nil {
		t.Fatalf("read through positioned cursor: %v", err)
	}
	if len(window.Records) != 1 || window.Records[0].Seq() != appended.Record.Seq() {
		t.Fatalf("records at positioned cursor = %#v, want appended event sequence %d", window.Records, appended.Record.Seq())
	}
	if window.EndOffset != *appended.EndByteCursor {
		t.Fatalf("read end offset = %d, want append cursor %d", window.EndOffset, *appended.EndByteCursor)
	}
}

func TestAppendEventWithEndByteCursorRetainsPositionWhenProjectionFailsAfterCommit(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		testSessionCategory,
		WithPersistenceObserver(observer),
		WithAppendProjector(func(context.Context, AppendProjection) error {
			return errors.New("projection failed")
		}),
	)
	if err != nil {
		t.Fatalf("create observed store: %v", err)
	}
	eventLog := mustMaterializeSessionTestEventLog(t, store)
	appended, err := eventLog.AppendRecordWithEndByteCursor(
		stringPointer("step-1"),
		sessionTestMessage(MessageRoleUser, "committed despite projection failure"),
	)
	if err != nil {
		t.Fatalf("positioned append returned projection failure: %v", err)
	}
	if !appended.Committed || appended.EndByteCursor == nil || *appended.EndByteCursor <= 0 {
		t.Fatalf("positioned append = %#v, want committed event with retained cursor", appended)
	}
	window, readErr := eventLog.ReadSegmentBackward(*appended.EndByteCursor, nil)
	if readErr != nil {
		t.Fatalf("read committed event after observer failure: %v", readErr)
	}
	if len(window.Records) != 1 || window.Records[0].Seq() != appended.Record.Seq() {
		t.Fatalf("records after observer failure = %#v, want committed event %d", window.Records, appended.Record.Seq())
	}
	if _, _, err := eventLog.AppendRecord(
		stringPointer("step-2"),
		sessionTestMessage(MessageRoleUser, "later mutation"),
	); err != nil {
		t.Fatalf("append after committed observer failure: %v", err)
	}
}
