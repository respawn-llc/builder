package sessionruntime

import (
	"path/filepath"
	"testing"

	"core/internal/testharness/filemode"
	"core/server/session"
)

func mustBlockSessionRuntimeEventLogAppends(t *testing.T, store *session.Store) *filemode.EventLogAppendBlocker {
	t.Helper()
	if store == nil {
		t.Fatal("event-log append blocker requires a session store")
	}
	return filemode.MustBlockEventLogAppends(t, filepath.Join(store.Dir(), "events.jsonl"))
}

func recoveredWarningEntryCount(t *testing.T, store *session.Store, warning string) int {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	window, err := eventLog.ReadRecentRecords(128)
	if err != nil {
		t.Fatalf("read recent event records: %v", err)
	}
	count := 0
	for _, record := range window.Records {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			t.Fatalf("read event payload: %v", payloadErr)
		}
		entry, ok := payload.(session.LocalEntryRecord)
		if ok && entry.Role == "warning" && entry.Text == warning {
			count++
		}
	}
	return count
}
