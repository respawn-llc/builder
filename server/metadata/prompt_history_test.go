package metadata

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"core/shared/serverapi"
)

func TestPromptHistoryRecordsAndListsByInsertionSequence(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := createMetadataTestSession(t, store, cfg, binding).Metadata().SessionID
	now := time.UnixMilli(123).UTC()

	first, inserted, err := store.RecordPromptHistoryEntry(ctx, PromptHistoryEntry{
		SessionID: sessionID,
		SourceID:  "req-1",
		Text:      "first",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("record first: %v", err)
	}
	if !inserted {
		t.Fatal("expected first insert")
	}
	second, inserted, err := store.RecordPromptHistoryEntry(ctx, PromptHistoryEntry{
		SessionID: sessionID,
		SourceID:  "req-2",
		Text:      "second",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("record second: %v", err)
	}
	if !inserted {
		t.Fatal("expected second insert")
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
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := createMetadataTestSession(t, store, cfg, binding).Metadata().SessionID
	entryCount := serverapi.SessionPromptHistoryMaxEntries + 2
	entries := make([]PromptHistoryEntry, 0, entryCount)

	for index := range entryCount {
		entry := PromptHistoryEntry{
			SessionID: sessionID,
			SourceID:  fmt.Sprintf("req-%03d", index),
			Text:      fmt.Sprintf("prompt-%03d", index),
			CreatedAt: time.UnixMilli(int64(entryCount - index)).UTC(),
		}
		entries = append(entries, entry)
		if _, inserted, err := store.RecordPromptHistoryEntry(ctx, entry); err != nil {
			t.Fatalf("record prompt %d: %v", index, err)
		} else if !inserted {
			t.Fatalf("record prompt %d returned existing row", index)
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

	existing, inserted, err := store.RecordPromptHistoryEntry(ctx, entries[0])
	if err != nil {
		t.Fatalf("replay omitted oldest prompt: %v", err)
	}
	if inserted {
		t.Fatal("replay omitted oldest prompt inserted a new row")
	}
	if existing.SourceID != entries[0].SourceID || existing.Text != entries[0].Text {
		t.Fatalf("replayed omitted prompt = %+v, want source_id=%q text=%q", existing, entries[0].SourceID, entries[0].Text)
	}
}

func TestPromptHistoryConflictRequiresEquivalentPayload(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := createMetadataTestSession(t, store, cfg, binding).Metadata().SessionID
	entry := PromptHistoryEntry{
		SessionID: sessionID,
		SourceID:  "req-1",
		Text:      "/status",
	}

	if _, _, err := store.RecordPromptHistoryEntry(ctx, entry); err != nil {
		t.Fatalf("record initial: %v", err)
	}
	_, inserted, err := store.RecordPromptHistoryEntry(ctx, entry)
	if err != nil {
		t.Fatalf("record equivalent replay: %v", err)
	}
	if inserted {
		t.Fatal("expected equivalent replay to return existing row")
	}

	entry.Text = "/resume"
	_, _, err = store.RecordPromptHistoryEntry(ctx, entry)
	if !errors.Is(err, ErrPromptHistoryConflict) {
		t.Fatalf("mismatched replay error = %v, want ErrPromptHistoryConflict", err)
	}
}

func TestQueuedPromptHistoryRecordsPlainPromptRow(t *testing.T) {
	ctx := context.Background()
	store, cfg, binding := newMetadataTestStore(t)
	sessionID := createMetadataTestSession(t, store, cfg, binding).Metadata().SessionID

	record, inserted, err := store.RecordPromptHistoryEntry(ctx, PromptHistoryEntry{
		SessionID: sessionID,
		SourceID:  "req-queue-1",
		Text:      "queued text",
	})
	if err != nil {
		t.Fatalf("record queued: %v", err)
	}
	if !inserted {
		t.Fatal("expected queued prompt insert")
	}
	if record.SessionID != sessionID || record.SourceID != "req-queue-1" || record.Text != "queued text" {
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
