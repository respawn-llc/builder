package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDiagnosticSessionCopyLeavesSourceUntouched(t *testing.T) {
	persisted := newSessionTestStore(t)
	persistedLog, err := persisted.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize persisted event log: %v", err)
	}
	seedContent := "persisted"
	if _, receipt, err := persistedLog.AppendRecord(nil, MessageRecord{
		Role:    MessageRoleDeveloper,
		Content: &seedContent,
	}); err != nil || !receipt.Committed {
		t.Fatalf("seed persisted event = %+v, %v; want committed", receipt, err)
	}
	eventsPath := filepath.Join(persisted.Dir(), eventsFile)
	beforeEvents := eventLogFingerprint(t, eventsPath)
	beforeRecord, err := sessionTestPersistence.ResolvePersistedSession(t.Context(), persisted.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolve persisted Session before inspection: %v", err)
	}

	inspection, err := OpenDiagnosticSessionCopy(
		t.Context(),
		sessionTestPersistence,
		persisted.Meta().SessionID,
	)
	if err != nil {
		t.Fatalf("open diagnostic Session copy: %v", err)
	}
	copyStore := inspection.Store()
	copyDir := copyStore.Dir()
	inspectionLog, err := copyStore.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize diagnostic event log: %v", err)
	}
	active, err := inspectionLog.ReadNewestSegmentBackward(nil)
	if err != nil {
		t.Fatalf("read diagnostic active segment: %v", err)
	}
	if len(active.Records) != 1 {
		t.Fatalf("diagnostic active records = %d, want original event", len(active.Records))
	}
	original, err := active.Records[0].Payload()
	if err != nil {
		t.Fatalf("read diagnostic original event: %v", err)
	}
	message, ok := original.(MessageRecord)
	if !ok || message.Content == nil || *message.Content != seedContent {
		t.Fatalf("diagnostic original event = %#v, want persisted message", original)
	}
	inspectionContent := "inspection only"
	if _, receipt, err := inspectionLog.AppendRecord(nil, MessageRecord{
		Role:    MessageRoleDeveloper,
		Content: &inspectionContent,
	}); err != nil || !receipt.Committed {
		t.Fatalf("append ephemeral event = %+v, %v; want committed", receipt, err)
	}
	if err := copyStore.SetInputDraft("inspection draft"); err != nil {
		t.Fatalf("mutate diagnostic metadata: %v", err)
	}
	if got := copyStore.Meta(); got.InputDraft != "inspection draft" {
		t.Fatalf("diagnostic Session copy meta = %+v, want isolated event and metadata mutations", got)
	}

	afterEvents := eventLogFingerprint(t, eventsPath)
	if !beforeEvents.equal(afterEvents) {
		t.Fatalf("diagnostic copy changed source event log: before=%+v after=%+v", beforeEvents, afterEvents)
	}
	afterRecord, err := sessionTestPersistence.ResolvePersistedSession(t.Context(), persisted.Meta().SessionID)
	if err != nil {
		t.Fatalf("resolve persisted Session after inspection: %v", err)
	}
	if afterRecord.Meta.InputDraft != beforeRecord.Meta.InputDraft {
		t.Fatalf("diagnostic copy changed source metadata: before=%+v after=%+v", beforeRecord.Meta, afterRecord.Meta)
	}
	if err := inspection.Close(); err != nil {
		t.Fatalf("close diagnostic Session copy: %v", err)
	}
	if _, err := os.Stat(copyDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("diagnostic Session copy still exists after close: %v", err)
	}
}

func TestDiagnosticSessionCopyFailedCloseRetainsStore(t *testing.T) {
	store := &Store{}
	inspection := &DiagnosticSessionCopy{
		root:  string([]byte{0}),
		store: store,
		state: diagnosticSessionCopyOpen,
	}

	if err := inspection.Close(); err == nil {
		t.Fatal("close diagnostic Session copy with invalid root unexpectedly succeeded")
	}
	if got := inspection.Store(); got != store {
		t.Fatalf("store after failed close = %p, want retained store %p", got, store)
	}
}

func TestDiagnosticSessionCopyContainsOnlyTheActiveEventSegment(t *testing.T) {
	persisted := newSessionTestStore(t)
	persistedLog, err := persisted.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize persisted event log: %v", err)
	}
	beforeCompaction := "before compaction"
	if _, receipt, err := persistedLog.AppendRecord(nil, MessageRecord{
		Role:    MessageRoleDeveloper,
		Content: &beforeCompaction,
	}); err != nil || !receipt.Committed {
		t.Fatalf("append pre-compaction event = %+v, %v; want committed", receipt, err)
	}
	if _, receipt, err := persistedLog.AppendCompactionHistoryReplacement(
		nil,
		HistoryReplacementRecord{Engine: "local", Mode: CompactionModeAuto},
	); err != nil || !receipt.Committed {
		t.Fatalf("append history replacement = %+v, %v; want committed", receipt, err)
	}
	afterCompaction := "after compaction"
	if _, receipt, err := persistedLog.AppendRecord(nil, MessageRecord{
		Role:    MessageRoleDeveloper,
		Content: &afterCompaction,
	}); err != nil || !receipt.Committed {
		t.Fatalf("append post-compaction event = %+v, %v; want committed", receipt, err)
	}

	inspection, err := OpenDiagnosticSessionCopy(
		t.Context(),
		sessionTestPersistence,
		persisted.Meta().SessionID,
	)
	if err != nil {
		t.Fatalf("open diagnostic Session copy: %v", err)
	}
	t.Cleanup(func() {
		if err := inspection.Close(); err != nil {
			t.Fatalf("close diagnostic Session copy: %v", err)
		}
	})
	inspectionLog, err := inspection.Store().MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize diagnostic event log: %v", err)
	}
	var payloads []EventRecordPayload
	if err := inspectionLog.WalkRecords(func(record EventRecord) error {
		payload, err := record.Payload()
		if err != nil {
			return err
		}
		payloads = append(payloads, payload)
		return nil
	}); err != nil {
		t.Fatalf("walk diagnostic event log: %v", err)
	}
	if len(payloads) != 2 {
		t.Fatalf("diagnostic active event count = %d, want replacement and active event", len(payloads))
	}
	if _, ok := payloads[0].(HistoryReplacementRecord); !ok {
		t.Fatalf("diagnostic first active payload = %T, want HistoryReplacementRecord", payloads[0])
	}
	message, ok := payloads[1].(MessageRecord)
	if !ok || message.Content == nil || *message.Content != afterCompaction {
		t.Fatalf("diagnostic active message = %#v, want post-compaction message", payloads[1])
	}
}

func TestDiagnosticSessionCopyRejectsLegacyEventLogsWithoutChangingTheEventLog(t *testing.T) {
	persisted := newSessionTestStore(t)
	writeSessionFixtureEvents(t, persisted.Dir(), []legacyTestEvent{{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      string(EventKindMessage),
		StepID:    "legacy-step",
		Payload: mustFixtureJSON(t, map[string]any{
			"role":    string(MessageRoleUser),
			"content": "legacy request history",
		}),
	}})
	eventsPath := filepath.Join(persisted.Dir(), eventsFile)
	before := eventLogFingerprint(t, eventsPath)

	inspection, err := OpenDiagnosticSessionCopy(
		t.Context(),
		sessionTestPersistence,
		persisted.Meta().SessionID,
	)
	if inspection != nil {
		t.Fatal("legacy diagnostic Session copy unexpectedly opened")
	}
	if !errors.Is(err, ErrDiagnosticLegacyEventLogUnsupported) {
		t.Fatalf("legacy diagnostic error = %v, want unsupported error", err)
	}
	after := eventLogFingerprint(t, eventsPath)
	if !before.equal(after) {
		t.Fatalf("legacy diagnostic attempt changed source event log: before=%+v after=%+v", before, after)
	}
}
