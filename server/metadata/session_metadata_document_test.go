package metadata

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"core/server/session"
	"core/shared/serverapi"
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
		RebindReminder: &session.SessionRebindReminder{
			SourceProject: serverapi.ProjectReference{ID: "source-project", Name: "Source"},
			TargetProject: serverapi.ProjectReference{ID: "target-project", Name: "Target"},
		},
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
	var encodedFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(encoded), &encodedFields); err != nil {
		t.Fatalf("decode encoded metadata fields: %v", err)
	}
	if _, ok := encodedFields["prompt_cache_lineage_generation"]; ok {
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
	var legacyEncodedFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(legacyEncoded), &legacyEncodedFields); err != nil {
		t.Fatalf("decode rewritten legacy metadata fields: %v", err)
	}
	if _, ok := legacyEncodedFields["prompt_cache_lineage_generation"]; ok {
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
