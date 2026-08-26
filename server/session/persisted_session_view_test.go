package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistedSessionViewCapturesMetadataAndEventLogIndependently(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	eventsPath := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(eventsPath)
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "persisted"),
	}); err != nil {
		t.Fatalf("append current event record: %v", err)
	}
	fp, err := os.OpenFile(eventsPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open current event log for partial append: %v", err)
	}
	if _, err := fp.WriteString(`{"seq":2`); err != nil {
		_ = fp.Close()
		t.Fatalf("write partial current event record: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close partially appended current event log: %v", err)
	}

	meta := Meta{SessionID: "session-1"}
	view, err := ResolvePersistedSessionView(
		context.Background(),
		stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta:       &meta,
		}},
		meta.SessionID,
	)
	if err != nil {
		t.Fatalf("resolve independently captured persisted Session view: %v", err)
	}
	if view.Meta().LastSequence != 0 {
		t.Fatalf("persisted metadata sequence = %d, want independently captured 0", view.Meta().LastSequence)
	}
	window, err := view.ReadNewestSegmentBackward(nil)
	if err != nil {
		t.Fatalf("read bounded event-log projection: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1)
}
