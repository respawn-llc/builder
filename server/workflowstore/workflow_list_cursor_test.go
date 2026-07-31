package workflowstore

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestWorkflowListCursorUsesOptionalWorkflowIDAndRejectsItsAbsence(t *testing.T) {
	activityAtUnixMs := int64(1)
	workflowID := runtimeids.NewWorkflowID()
	token, err := workflowListPageToken(
		workflowRecordRow{ID: workflowID, ActivityAtUnixMs: &activityAtUnixMs},
		nil,
		nil,
		"",
	)
	if err != nil {
		t.Fatalf("workflowListPageToken: %v", err)
	}

	cursor, err := parseWorkflowListPageToken(token)
	if err != nil {
		t.Fatalf("parseWorkflowListPageToken: %v", err)
	}
	if cursor.workflowID == nil || *cursor.workflowID != workflowID {
		t.Fatalf("cursor workflow id = %v, want %q", cursor.workflowID, workflowID)
	}

	payload, err := json.Marshal(workflowListPageTokenPayload{
		Version:           workflowListPageTokenVersion,
		ActivityAtUnixMs:  &activityAtUnixMs,
		SearchQuery:       "",
		FilterFingerprint: "fingerprint",
	})
	if err != nil {
		t.Fatalf("marshal token payload: %v", err)
	}
	if _, err := parseWorkflowListPageToken(base64.RawURLEncoding.EncodeToString(payload)); err == nil {
		t.Fatal("parseWorkflowListPageToken accepted a token without the required cursor workflow id")
	}
}
