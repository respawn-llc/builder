//go:build windows

package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestWindowsDurableEventLogMaterialization(t *testing.T) {
	t.Run("missing source", func(t *testing.T) {
		sessionDir := newEventLogPreparationSessionDir(t)
		store := newEventLogReconciliationStore(
			t,
			sessionDir,
			reconciliationTestMeta(),
			&eventLogReconciliationTestObserver{},
			nil,
		)
		capability, err := store.MaterializeEventLog()
		if err != nil {
			t.Fatalf("materialize missing source: %v", err)
		}
		if mustMaterializedRevision(capability) != 0 {
			t.Fatalf("missing-source revision = %d, want 0", mustMaterializedRevision(capability))
		}
	})

	t.Run("empty source", func(t *testing.T) {
		sessionDir := newEventLogPreparationSessionDir(t)
		writeEventLogPreparationSource(
			t,
			filepath.Join(sessionDir, eventsFile),
			nil,
		)
		store := newEventLogReconciliationStore(
			t,
			sessionDir,
			reconciliationTestMeta(),
			&eventLogReconciliationTestObserver{},
			nil,
		)
		capability, err := store.MaterializeEventLog()
		if err != nil {
			t.Fatalf("materialize empty source: %v", err)
		}
		if mustMaterializedRevision(capability) != 0 {
			t.Fatalf("empty-source revision = %d, want 0", mustMaterializedRevision(capability))
		}
	})

	t.Run("legacy committed retry", func(t *testing.T) {
		sessionDir := newEventLogPreparationSessionDir(t)
		eventsPath := filepath.Join(sessionDir, eventsFile)
		writeEventLogPreparationSource(t, eventsPath, []byte(
			`{"seq":1,"timestamp":"2026-07-19T10:00:00Z","kind":"message",`+
				`"payload":{"role":"user","content":"windows retry"}}`+"\n",
		))
		meta := reconciliationTestMeta()
		meta.LastSequence = 1
		observer := &eventLogReconciliationTestObserver{
			err: context.DeadlineExceeded,
		}
		store := newEventLogReconciliationStore(
			t,
			sessionDir,
			meta,
			observer,
			nil,
		)
		_, err := store.MaterializeEventLog()
		var materializationErr *EventLogMaterializationError
		if !errors.As(err, &materializationErr) ||
			!materializationErr.Committed ||
			!materializationErr.PendingRepair {
			t.Fatalf("committed failure facts = %+v / %v", materializationErr, err)
		}
		observer.err = nil
		capability, err := store.MaterializeEventLog()
		if err != nil {
			t.Fatalf("retry committed materialization: %v", err)
		}
		if mustMaterializedRevision(capability) != 1 {
			t.Fatalf("legacy retry revision = %d, want 1", mustMaterializedRevision(capability))
		}
	})
}
