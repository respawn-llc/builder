package session

import (
	"errors"
	"os"
	"testing"

	"core/internal/testharness/filemode"
)

func TestAgentStepBoundaryRecordRoundTripsAsNonTranscriptEvent(t *testing.T) {
	stepID := "step-1"
	source, err := NewEventRecord(1, &stepID, AgentStepBoundaryRecord{
		SessionID: "session-1",
	})
	if err != nil {
		t.Fatalf("NewEventRecord: %v", err)
	}
	encoded, err := encodeEventRecordV1(source)
	if err != nil {
		t.Fatalf("encode boundary record: %v", err)
	}
	decoded, err := decodeEventRecordV1(encoded)
	if err != nil {
		t.Fatalf("decode boundary record: %v", err)
	}
	if got, err := decoded.Kind(); err != nil || got != EventKindAgentStepBoundary {
		t.Fatalf("decoded kind = %q, %v; want %q", got, err, EventKindAgentStepBoundary)
	}
	payload, err := decoded.Payload()
	if err != nil {
		t.Fatalf("decoded payload: %v", err)
	}
	boundary, ok := payload.(AgentStepBoundaryRecord)
	if !ok || boundary.SessionID != "session-1" || decoded.StepID() == nil || *decoded.StepID() != stepID {
		t.Fatalf("decoded boundary = %#v, step_id=%v", payload, decoded.StepID())
	}
}

func TestAppendAgentStepFinalizationAppendsBoundaryAndClearsMatchingRecovery(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if err := store.SetPendingModelRecovery(PendingModelRecovery{
		RecoveryID: "recovery-1",
		StepID:     "step-1",
		Reason:     "provider_failure",
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	records, receipt, err := log.AppendAgentStepFinalization("step-1", []EventRecordPayload{
		sessionTestMessage(MessageRoleAssistant, "final response"),
	})
	if err != nil {
		t.Fatalf("append finalization: %v", err)
	}
	if !receipt.Committed || len(records) != 2 {
		t.Fatalf("finalization records=%d receipt=%+v, want two committed records", len(records), receipt)
	}
	if _, ok := mustEventRecordPayload(records[1]).(AgentStepBoundaryRecord); !ok {
		t.Fatalf("finalization tail payload = %#v, want boundary", mustEventRecordPayload(records[1]))
	}
	if got := store.Meta().PendingModelRecovery; got != nil {
		t.Fatalf("matching recovery after finalization = %+v, want nil", got)
	}
}

func TestAppendAgentStepFinalizationSupportsBoundaryOnlyAndPreservesNonmatchingRecovery(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if err := store.SetPendingModelRecovery(PendingModelRecovery{
		RecoveryID: "recovery-1",
		StepID:     "successor-step",
		Reason:     "provider_failure",
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	records, receipt, err := log.AppendAgentStepFinalization("step-1", nil)
	if err != nil {
		t.Fatalf("append boundary-only finalization: %v", err)
	}
	if !receipt.Committed || len(records) != 1 {
		t.Fatalf("boundary-only records=%d receipt=%+v, want one committed record", len(records), receipt)
	}
	if _, ok := mustEventRecordPayload(records[0]).(AgentStepBoundaryRecord); !ok {
		t.Fatalf("boundary-only payload = %#v, want boundary", mustEventRecordPayload(records[0]))
	}
	if got := store.Meta().PendingModelRecovery; got == nil || got.StepID != "successor-step" {
		t.Fatalf("nonmatching recovery after finalization = %+v, want preserved", got)
	}
}

func TestAppendAgentStepFinalizationLeavesEverythingUnchangedWhenEventLogUncommitted(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if err := store.SetPendingModelRecovery(PendingModelRecovery{
		RecoveryID: "recovery-1",
		StepID:     "step-1",
		Reason:     "provider_failure",
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}
	before := store.Meta()
	filemode.MustBlockEventLogAppends(t, store.eventsFP)

	records, receipt, err := log.AppendAgentStepFinalization("step-1", []EventRecordPayload{
		sessionTestMessage(MessageRoleAssistant, "must not persist"),
	})
	if err == nil {
		t.Fatal("append finalization did not surface event-log failure")
	}
	if receipt.Committed || len(records) != 2 {
		t.Fatalf("uncommitted records=%d receipt=%+v, want attempted payload plus boundary", len(records), receipt)
	}
	after := store.Meta()
	if after.LastSequence != before.LastSequence || after.PendingModelRecovery == nil {
		t.Fatalf("metadata after uncommitted finalization = %+v, want unchanged from %+v", after, before)
	}
	if got := mustMaterializedRevision(log); got != before.LastSequence {
		t.Fatalf("event revision after uncommitted finalization = %d, want %d", got, before.LastSequence)
	}
}

func TestAppendAgentStepFinalizationKeepsFactsWhenObserverFailsAfterCommit(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(t.TempDir(), "workspace", t.TempDir(), testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("create observed store: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist observed store: %v", err)
	}
	if err := store.SetPendingModelRecovery(PendingModelRecovery{
		RecoveryID: "recovery-1",
		StepID:     "step-1",
		Reason:     "provider_failure",
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)
	observer.err = os.ErrPermission

	records, receipt, err := log.AppendAgentStepFinalization("step-1", []EventRecordPayload{
		sessionTestMessage(MessageRoleAssistant, "committed final response"),
	})
	if err == nil {
		t.Fatal("append finalization did not surface observer failure")
	}
	if !receipt.Committed || len(records) != 2 {
		t.Fatalf("committed records=%d receipt=%+v, want two committed records", len(records), receipt)
	}
	if got := store.Meta().PendingModelRecovery; got != nil {
		t.Fatalf("matching recovery after committed observer failure = %+v, want nil", got)
	}
	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("collect committed events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("committed event count = %d, want 2", len(events))
	}
}

func TestAgentStepBoundaryRecordRejectsMissingSessionID(t *testing.T) {
	_, err := NewEventRecord(1, nil, AgentStepBoundaryRecord{})
	if err == nil {
		t.Fatal("missing boundary session id accepted")
	}
	if errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("boundary validation returned unrelated error: %v", err)
	}
}
