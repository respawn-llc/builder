package appfixture

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"core/shared/lifecyclecontract"
)

type LifecycleHookRecord struct {
	ParentPID int             `json:"parent_pid"`
	Payload   json.RawMessage `json:"payload"`
}

type LifecycleHookEvent struct {
	Category   lifecyclecontract.Category `json:"category"`
	OccurredAt time.Time                  `json:"occurred_at"`
	Focused    bool                       `json:"focused"`
	Context    lifecyclecontract.Context  `json:"context"`
	Details    json.RawMessage            `json:"details"`
}

func WaitForLifecycleHookRecords(t testing.TB, path string, count int) []LifecycleHookRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatalf("open lifecycle hook records: %v", err)
		}
		var records []LifecycleHookRecord
		decoder := json.NewDecoder(file)
		for {
			var record LifecycleHookRecord
			if err := decoder.Decode(&record); err != nil {
				if !errors.Is(err, io.EOF) {
					_ = file.Close()
					t.Fatalf("decode lifecycle hook records: %v", err)
				}
				break
			}
			records = append(records, record)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close lifecycle hook records: %v", err)
		}
		if len(records) >= count {
			return records
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lifecycle hook records", count)
	return nil
}

func DecodeLifecycleHookEvents(t testing.TB, records []LifecycleHookRecord) []LifecycleHookEvent {
	t.Helper()
	events := make([]LifecycleHookEvent, 0, len(records))
	for index, record := range records {
		var event LifecycleHookEvent
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			t.Fatalf("decode lifecycle hook event %d: %v", index, err)
		}
		events = append(events, event)
	}
	return events
}
