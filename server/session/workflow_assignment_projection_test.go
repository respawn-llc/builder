package session

import (
	"fmt"
	"testing"
)

func TestActiveWorkflowAssignmentProjectionMigratesMissingStateToKnownAbsence(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable Session: %v", err)
	}
	if err := store.mutateAndPersist(func() error {
		store.meta.ActiveWorkflowAssignmentState = nil
		return nil
	}); err != nil {
		t.Fatalf("simulate Session created before assignment projection: %v", err)
	}
	assignment, err := store.ActiveWorkflowAssignmentProjection()
	if err != nil || assignment != nil {
		t.Fatalf("migrated Session projection = %+v, %v; want known absence", assignment, err)
	}
	if store.Meta().ActiveWorkflowAssignmentState == nil {
		t.Fatal("missing assignment projection state was not persisted")
	}
}

func TestActiveWorkflowAssignmentProjectionMigratesPersistedAssignment(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable Session: %v", err)
	}
	messageType := MessageTypeWorkflowMode
	content := "persisted assignment"
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	if _, receipt, err := eventLog.AppendRecord(nil, MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: &messageType,
		Content:     &content,
	}); err != nil || !receipt.Committed {
		t.Fatalf("append legacy workflow assignment = %+v, %v", receipt, err)
	}
	if err := store.mutateAndPersist(func() error {
		store.meta.ActiveWorkflowAssignmentState = nil
		store.meta.ActiveWorkflowAssignment = nil
		return nil
	}); err != nil {
		t.Fatalf("simulate Session created before assignment projection: %v", err)
	}

	assignment, err := store.ActiveWorkflowAssignmentProjection()
	if err != nil || assignment == nil || assignment.Content == nil || *assignment.Content != content {
		t.Fatalf("persisted parent projection = %+v, %v", assignment, err)
	}
	if store.Meta().ActiveWorkflowAssignmentState == nil {
		t.Fatal("persisted assignment projection state was not migrated")
	}
}

func TestActiveWorkflowAssignmentProjectionMigrationDoesNotScanPastRecentWindow(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable Session: %v", err)
	}
	messageType := MessageTypeWorkflowMode
	assignment := "assignment outside migration window"
	payloads := make([]EventRecordPayload, 0, activeWorkflowAssignmentMigrationMaxRecords+1)
	payloads = append(payloads, MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: &messageType,
		Content:     &assignment,
	})
	for index := 0; index < activeWorkflowAssignmentMigrationMaxRecords; index++ {
		content := fmt.Sprintf("ordinary message %d", index)
		payloads = append(payloads, MessageRecord{
			Role:    MessageRoleUser,
			Content: &content,
		})
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	if _, receipt, err := eventLog.AppendRecordsAtomic(nil, payloads); err != nil || !receipt.Committed {
		t.Fatalf("append migration window fixture = %+v, %v", receipt, err)
	}
	if err := store.mutateAndPersist(func() error {
		store.meta.ActiveWorkflowAssignment = nil
		store.meta.ActiveWorkflowAssignmentState = nil
		return nil
	}); err != nil {
		t.Fatalf("simulate Session created before assignment projection: %v", err)
	}

	if _, err := store.ActiveWorkflowAssignmentProjection(); err == nil {
		t.Fatal("migration unexpectedly traversed past the bounded recent window")
	}
	meta := store.Meta()
	if meta.ActiveWorkflowAssignment != nil || meta.ActiveWorkflowAssignmentState != nil {
		t.Fatalf("unresolved migration persisted projection %+v", meta.ActiveWorkflowAssignment)
	}
}
