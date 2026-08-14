package metadata

import (
	"reflect"
	"strings"
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
		HeadlessActive:                  true,
		CompactionSoonReminderIssued:    true,
		GeneratedRecoveredWarningIssued: true,
		PendingModelRecovery: &session.PendingModelRecovery{
			RecoveryID: "recovery",
			Reason:     "provider",
			CreatedAt:  createdAt,
		},
		WorktreeReminder: &session.WorktreeReminderState{Mode: session.WorktreeReminderModeEnter},
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
	if strings.Contains(string(encoded), "prompt_cache_lineage_generation") {
		t.Fatalf("encoded metadata retained obsolete prompt cache lineage generation: %s", encoded)
	}
	var legacyDecoded sessionMetadataDocument
	if err := unmarshalStoredJSON(`{"workspace_root":"/workspace","prompt_cache_lineage_generation":7}`, &legacyDecoded); err != nil {
		t.Fatalf("unmarshalStoredJSON legacy metadata: %v", err)
	}
	legacyEncoded, err := marshalJSON(legacyDecoded)
	if err != nil {
		t.Fatalf("marshalJSON legacy metadata: %v", err)
	}
	if strings.Contains(string(legacyEncoded), "prompt_cache_lineage_generation") {
		t.Fatalf("rewritten metadata retained obsolete prompt cache lineage generation: %s", legacyEncoded)
	}
	var decoded sessionMetadataDocument
	if err := unmarshalStoredJSON(encoded, &decoded); err != nil {
		t.Fatalf("unmarshalStoredJSON: %v", err)
	}
	if !reflect.DeepEqual(decoded, document) {
		t.Fatalf("decoded metadata = %#v, want %#v", decoded, document)
	}
}
