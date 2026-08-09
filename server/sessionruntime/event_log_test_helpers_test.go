package sessionruntime

import (
	"testing"

	"core/server/session"
)

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
		if ok && entry.Role == "warning" &&
			entry.Text != nil && *entry.Text == warning {
			count++
		}
	}
	return count
}
