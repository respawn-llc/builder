package appfixture

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		records, err := readLifecycleHookRecords(path)
		if lifecycleHookRecordsPending(err) {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatalf("read lifecycle hook records: %v", err)
		}
		if len(records) >= count {
			return records
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d lifecycle hook records", count)
	return nil
}

func WaitForLifecycleHookCategories(
	ctx context.Context,
	path string,
	required []lifecyclecontract.Category,
) error {
	if len(required) == 0 {
		return errors.New("wait for lifecycle hook categories requires categories")
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		records, err := readLifecycleHookRecords(path)
		if err == nil {
			events, decodeErr := decodeLifecycleHookEvents(records)
			if decodeErr != nil {
				return decodeErr
			}
			seen := make(map[lifecyclecontract.Category]struct{}, len(events))
			for _, event := range events {
				seen[event.Category] = struct{}{}
			}
			complete := true
			for _, category := range required {
				if _, ok := seen[category]; !ok {
					complete = false
					break
				}
			}
			if complete {
				return nil
			}
		} else if !lifecycleHookRecordsPending(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for lifecycle hook categories: %w", context.Cause(ctx))
		case <-ticker.C:
		}
	}
}

func lifecycleHookRecordsPending(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, io.ErrUnexpectedEOF)
}

func DecodeLifecycleHookEvents(t testing.TB, records []LifecycleHookRecord) []LifecycleHookEvent {
	t.Helper()
	events, err := decodeLifecycleHookEvents(records)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func readLifecycleHookRecords(path string) ([]LifecycleHookRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var records []LifecycleHookRecord
	decoder := json.NewDecoder(file)
	for {
		var record LifecycleHookRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				return records, nil
			}
			return nil, fmt.Errorf("decode lifecycle hook records: %w", err)
		}
		records = append(records, record)
	}
}

func decodeLifecycleHookEvents(records []LifecycleHookRecord) ([]LifecycleHookEvent, error) {
	events := make([]LifecycleHookEvent, 0, len(records))
	for index, record := range records {
		var event LifecycleHookEvent
		if err := json.Unmarshal(record.Payload, &event); err != nil {
			return nil, fmt.Errorf("decode lifecycle hook event %d: %w", index, err)
		}
		events = append(events, event)
	}
	return events, nil
}
