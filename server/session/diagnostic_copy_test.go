package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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
	if got := copyStore.Meta(); got.LastSequence != beforeRecord.Meta.LastSequence+1 || got.InputDraft != "inspection draft" {
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
	if afterRecord.Meta.LastSequence != beforeRecord.Meta.LastSequence || afterRecord.Meta.InputDraft != beforeRecord.Meta.InputDraft {
		t.Fatalf("diagnostic copy changed source metadata: before=%+v after=%+v", beforeRecord.Meta, afterRecord.Meta)
	}
	if err := inspection.Close(); err != nil {
		t.Fatalf("close diagnostic Session copy: %v", err)
	}
	if _, err := os.Stat(copyDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("diagnostic Session copy still exists after close: %v", err)
	}
}
