package metadata

import (
	"reflect"
	"testing"
	"time"

	"core/server/session"
)

func TestSessionMetadataDocumentRoundTripsWorkflowNeutralFields(t *testing.T) {
	t.Parallel()
	createdAt := time.Unix(123, 456).UTC()
	document := sessionMetadataDocument{
		WorkspaceRoot:                   "/workspace",
		WorkspaceContainer:              "workspace",
		ConversationEstablished:         true,
		PromptCacheLineageGeneration:    7,
		HeadlessActive:                  true,
		CompactionSoonReminderIssued:    true,
		GeneratedRecoveredWarningIssued: true,
		WorktreeReminder:                &session.WorktreeReminderState{Mode: session.WorktreeReminderModeEnter},
		Goal: &session.GoalState{
			ID:        "goal",
			Objective: "finish",
			Status:    session.GoalStatusActive,
			CreatedAt: createdAt,
			UpdatedAt: createdAt,
		},
		ActiveWorkflowAssignment: &session.MessageRecord{
			Role: session.MessageRoleDeveloper,
		},
		ActiveWorkflowAssignmentState: &session.ActiveWorkflowAssignmentState{},
	}

	encoded, err := marshalJSON(document)
	if err != nil {
		t.Fatalf("marshalJSON: %v", err)
	}
	var decoded sessionMetadataDocument
	if err := unmarshalStoredJSON(encoded, &decoded); err != nil {
		t.Fatalf("unmarshalStoredJSON: %v", err)
	}
	if !reflect.DeepEqual(decoded, document) {
		t.Fatalf("decoded metadata = %#v, want %#v", decoded, document)
	}
}
