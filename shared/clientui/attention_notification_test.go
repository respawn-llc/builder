package clientui

import (
	"encoding/json"
	"testing"
)

func TestInterruptedCurrentNodeFocusEmitsExplicitNullSetupOperationID(t *testing.T) {
	var setupOperationID *string
	raw, err := json.Marshal(AttentionNotificationTaskDetailFocus{
		Kind:             AttentionNotificationFocusInterruptedCurrentNode,
		SetupOperationID: &setupOperationID,
	})
	if err != nil {
		t.Fatalf("marshal interrupted Current Node focus: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode interrupted Current Node focus: %v", err)
	}
	if got := string(fields["setup_operation_id"]); got != "null" {
		t.Fatalf("setup_operation_id = %s, want explicit null: %s", got, raw)
	}
}

func TestQuestionFocusOmitsInapplicableSetupOperationID(t *testing.T) {
	raw, err := json.Marshal(AttentionNotificationTaskDetailFocus{
		Kind:   AttentionNotificationFocusQuestion,
		AskIDs: []string{"ask-1"},
	})
	if err != nil {
		t.Fatalf("marshal question focus: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode question focus: %v", err)
	}
	if _, exists := fields["setup_operation_id"]; exists {
		t.Fatalf("question focus emitted inapplicable setup_operation_id: %s", raw)
	}
}
