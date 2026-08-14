package session

import (
	"errors"
	"testing"
)

func TestActiveWorkflowAssignmentProjectionDistinguishesKnownAbsenceFromMissingState(t *testing.T) {
	store := newSessionTestLazyStore(t)
	assignment, err := store.ActiveWorkflowAssignmentProjection()
	if err != nil || assignment != nil {
		t.Fatalf("fresh Session projection = %+v, %v; want known absence", assignment, err)
	}

	store.meta.ActiveWorkflowAssignmentState = nil
	if _, err := store.ActiveWorkflowAssignmentProjection(); !errors.Is(err, ErrActiveWorkflowAssignmentProjectionUnavailable) {
		t.Fatalf("missing projection marker error = %v", err)
	}
}

func TestActiveWorkflowAssignmentProjectionTrustsPersistedParentAssignment(t *testing.T) {
	store := newSessionTestLazyStore(t)
	messageType := MessageTypeWorkflowMode
	content := "persisted assignment"
	store.meta.ActiveWorkflowAssignmentState = nil
	store.meta.ActiveWorkflowAssignment = &MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: &messageType,
		Content:     &content,
	}

	assignment, err := store.ActiveWorkflowAssignmentProjection()
	if err != nil || assignment == nil || assignment.Content == nil || *assignment.Content != content {
		t.Fatalf("persisted parent projection = %+v, %v", assignment, err)
	}
}
