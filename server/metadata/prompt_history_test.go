package metadata

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"core/shared/serverapi"
)

func TestPromptHistoryRecordsAndListsByInsertionSequence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := createMetadataTestSession(t, store, cfg, binding).Meta().SessionID
	now := time.UnixMilli(123).UTC()

	first, err := store.RecordPromptHistoryEntry(ctx, PromptHistoryEntry{
		SessionID: sessionID,
		Text:      "first",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("record first: %v", err)
	}
	second, err := store.RecordPromptHistoryEntry(ctx, PromptHistoryEntry{
		SessionID: sessionID,
		Text:      "second",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("record second: %v", err)
	}
	if first.Sequence >= second.Sequence {
		t.Fatalf("sequences not increasing: first=%d second=%d", first.Sequence, second.Sequence)
	}

	history, err := store.ReadPromptHistory(ctx, sessionID)
	if err != nil {
		t.Fatalf("read prompt history: %v", err)
	}
	if !reflect.DeepEqual(history, []string{"first", "second"}) {
		t.Fatalf("history = %+v", history)
	}
}

func TestPromptHistoryReadsNewestRecordedTailWithoutPruningPersistence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := createMetadataTestSession(t, store, cfg, binding).Meta().SessionID
	entryCount := serverapi.SessionPromptHistoryMaxEntries + 2
	entries := make([]PromptHistoryEntry, 0, entryCount)

	for index := range entryCount {
		entry := PromptHistoryEntry{
			SessionID: sessionID,
			Text:      fmt.Sprintf("prompt-%03d", index),
			CreatedAt: time.UnixMilli(int64(entryCount - index)).UTC(),
		}
		entries = append(entries, entry)
		if _, err := store.RecordPromptHistoryEntry(ctx, entry); err != nil {
			t.Fatalf("record prompt %d: %v", index, err)
		}
	}

	history, err := store.ReadPromptHistory(ctx, sessionID)
	if err != nil {
		t.Fatalf("read prompt history: %v", err)
	}
	if got, want := len(history), serverapi.SessionPromptHistoryMaxEntries; got != want {
		t.Fatalf("history length = %d, want %d", got, want)
	}
	for retainedIndex, text := range history {
		recordingIndex := retainedIndex + entryCount - serverapi.SessionPromptHistoryMaxEntries
		if want := entries[recordingIndex].Text; text != want {
			t.Fatalf("history[%d] = %q, want %q", retainedIndex, text, want)
		}
	}

	repeated, err := store.RecordPromptHistoryEntry(ctx, entries[0])
	if err != nil {
		t.Fatalf("repeat omitted oldest prompt: %v", err)
	}
	if repeated.Sequence <= int64(entryCount) || repeated.Text != entries[0].Text {
		t.Fatalf("repeated omitted prompt = %+v, want a new appended row for %q", repeated, entries[0].Text)
	}
}

func TestPromptHistoryRepeatedExplicitInvocationAppendsDistinctRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := createMetadataTestSession(t, store, cfg, binding).Meta().SessionID
	entry := PromptHistoryEntry{
		SessionID: sessionID,
		Text:      "/status",
	}

	first, err := store.RecordPromptHistoryEntry(ctx, entry)
	if err != nil {
		t.Fatalf("record initial: %v", err)
	}
	second, err := store.RecordPromptHistoryEntry(ctx, entry)
	if err != nil {
		t.Fatalf("record repeated invocation: %v", err)
	}
	if second.Sequence <= first.Sequence {
		t.Fatalf("repeated invocation did not append: first=%+v second=%+v", first, second)
	}
}

func TestQueuedPromptHistoryRecordsPlainPromptRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := createMetadataTestSession(t, store, cfg, binding).Meta().SessionID

	record, err := store.RecordPromptHistoryEntry(ctx, PromptHistoryEntry{
		SessionID: sessionID,
		Text:      "queued text",
	})
	if err != nil {
		t.Fatalf("record queued: %v", err)
	}
	if record.SessionID != sessionID || record.Text != "queued text" {
		t.Fatalf("queued record = %+v", record)
	}

	history, err := store.ReadPromptHistory(ctx, sessionID)
	if err != nil {
		t.Fatalf("read prompt history: %v", err)
	}
	if !reflect.DeepEqual(history, []string{"queued text"}) {
		t.Fatalf("history = %+v", history)
	}
}
