package session

import (
	"errors"
	"fmt"
	"testing"
)

func TestEnsureActiveWorkflowAssignmentProjectionMigratesRecentAssignment(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable Session: %v", err)
	}
	messageType := MessageTypeWorkflowMode
	sourcePath := "workflow/task/node"
	content := "assignment"
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	if _, receipt, err := eventLog.AppendRecord(nil, MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: &messageType,
		SourcePath:  &sourcePath,
		Content:     &content,
	}); err != nil || !receipt.Committed {
		t.Fatalf("append legacy workflow assignment = %+v, %v", receipt, err)
	}
	if err := store.mutateAndPersist(func() error {
		store.meta.ActiveWorkflowAssignment = nil
		store.meta.ActiveWorkflowAssignmentKnown = false
		return nil
	}); err != nil {
		t.Fatalf("simulate pre-projection Session: %v", err)
	}

	if err := store.EnsureActiveWorkflowAssignmentProjection(); err != nil {
		t.Fatalf("migrate active workflow assignment projection: %v", err)
	}
	meta := store.Meta()
	if !meta.ActiveWorkflowAssignmentKnown ||
		meta.ActiveWorkflowAssignment == nil ||
		meta.ActiveWorkflowAssignment.SourcePath == nil ||
		*meta.ActiveWorkflowAssignment.SourcePath != sourcePath {
		t.Fatalf("migrated active workflow assignment = %+v", meta.ActiveWorkflowAssignment)
	}
}

func TestEnsureActiveWorkflowAssignmentProjectionMigratesAbsentAssignment(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable Session: %v", err)
	}
	if err := store.mutateAndPersist(func() error {
		store.meta.ActiveWorkflowAssignmentKnown = false
		return nil
	}); err != nil {
		t.Fatalf("simulate pre-projection empty Session: %v", err)
	}

	if err := store.EnsureActiveWorkflowAssignmentProjection(); err != nil {
		t.Fatalf("migrate absent workflow assignment projection: %v", err)
	}
	meta := store.Meta()
	if !meta.ActiveWorkflowAssignmentKnown || meta.ActiveWorkflowAssignment != nil {
		t.Fatalf("migrated absent workflow assignment state = known %v assignment %+v", meta.ActiveWorkflowAssignmentKnown, meta.ActiveWorkflowAssignment)
	}
}

func TestEnsureActiveWorkflowAssignmentProjectionDoesNotScanPastBoundedWindow(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("ensure durable Session: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	payloads := make([]EventRecordPayload, activeWorkflowAssignmentMigrationWindow+1)
	for index := range payloads {
		content := fmt.Sprintf("ordinary message %d", index)
		payloads[index] = MessageRecord{Role: MessageRoleUser, Content: &content}
	}
	if _, receipt, err := eventLog.AppendRecordsAtomic(nil, payloads); err != nil || !receipt.Committed {
		t.Fatalf("append migration-window fixture = %+v, %v", receipt, err)
	}
	if err := store.mutateAndPersist(func() error {
		store.meta.ActiveWorkflowAssignmentKnown = false
		return nil
	}); err != nil {
		t.Fatalf("simulate unresolved pre-projection Session: %v", err)
	}

	err = store.EnsureActiveWorkflowAssignmentProjection()
	if !errors.Is(err, ErrActiveWorkflowAssignmentProjectionUnavailable) {
		t.Fatalf("projection migration error = %v, want bounded-window unavailable", err)
	}
}
