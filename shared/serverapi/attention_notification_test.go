package serverapi

import (
	"encoding/json"
	"testing"
	"time"

	"core/shared/clientui"
)

func TestValidateAttentionNotificationEventAcceptsTaskDetailQuestionFocus(t *testing.T) {
	event := validAttentionNotificationEvent()

	if err := ValidateAttentionNotificationEvent(event); err != nil {
		t.Fatalf("ValidateAttentionNotificationEvent: %v", err)
	}
}

func TestValidateAttentionNotificationEventRejectsSnapshotCompletePayload(t *testing.T) {
	event := clientui.AttentionNotificationEvent{
		Sequence:  1,
		Source:    clientui.AttentionNotificationSourceSnapshot,
		Type:      clientui.AttentionNotificationEventSnapshotComplete,
		SessionID: "session-1",
		ID:        attentionNotificationIDPtr(clientui.AttentionNotificationKindQuestion, "prompt-1"),
	}

	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted snapshot_complete with id payload")
	}
}

func TestValidateAttentionNotificationEventRejectsPendingEnvelopePayload(t *testing.T) {
	event := validAttentionNotificationEvent()
	event.ID = attentionNotificationIDPtr(clientui.AttentionNotificationKindQuestion, "batch-1")
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted pending event with top-level id")
	}

	event = validAttentionNotificationEvent()
	event.OccurredAt = timePtr(time.Unix(1, 0).UTC())
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted pending event with top-level occurred_at")
	}
}

func TestValidateAttentionNotificationEventRejectsTaskDetailTargetWithoutTypedFocus(t *testing.T) {
	event := validAttentionNotificationEvent()
	event.Pending.Target.Focus = nil

	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted task-detail target without focus")
	}
	event = validAttentionNotificationEvent()
	event.Pending.Target.Focus.AskIDs = nil
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted question focus without immutable ask ids")
	}
}

func TestValidateAttentionNotificationEventRejectsUnsupportedEnums(t *testing.T) {
	event := validAttentionNotificationEvent()
	event.Source = clientui.AttentionNotificationSource("replay")
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted unsupported source")
	}

	event = validAttentionNotificationEvent()
	event.Pending.Kind = clientui.AttentionNotificationKind("other")
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted unsupported pending kind")
	}

	event = clientui.AttentionNotificationEvent{
		Sequence:   1,
		Source:     clientui.AttentionNotificationSourceLive,
		Type:       clientui.AttentionNotificationEventResolved,
		ID:         attentionNotificationIDPtr(clientui.AttentionNotificationKind("other"), "transition-1"),
		Kind:       clientui.AttentionNotificationKind("other"),
		OccurredAt: timePtr(time.Unix(1, 0).UTC()),
	}
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted unsupported resolved kind")
	}
}

func TestValidateAttentionNotificationEventRejectsKindTargetFocusMismatch(t *testing.T) {
	event := validAttentionNotificationEvent()
	event.Pending.ID = attentionNotificationID(clientui.AttentionNotificationKindApproval, "transition-1")
	event.Pending.Kind = clientui.AttentionNotificationKindApproval
	event.Pending.Question = nil
	event.Pending.Approval = &clientui.AttentionNotificationApprovalState{TaskTransitionID: "transition-1"}
	event.Pending.Target.Focus.Kind = clientui.AttentionNotificationFocusQuestion
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted task-detail approval with question focus")
	}

	event = validAttentionNotificationEvent()
	event.Pending.ID = attentionNotificationID(clientui.AttentionNotificationKindApproval, "transition-1")
	event.Pending.Kind = clientui.AttentionNotificationKindApproval
	event.Pending.Approval = &clientui.AttentionNotificationApprovalState{TaskTransitionID: "transition-1"}
	event.Pending.Question = &clientui.AttentionNotificationQuestionState{PreparedAskIDs: []string{"ask-1"}}
	event.Pending.Target.Focus = &clientui.AttentionNotificationTaskDetailFocus{
		Kind:             clientui.AttentionNotificationFocusApproval,
		TaskTransitionID: "transition-1",
	}
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted approval with question state")
	}

	event = validAttentionNotificationEvent()
	event.Pending.ID = attentionNotificationID(clientui.AttentionNotificationKindApproval, "approval-1")
	event.Pending.Kind = clientui.AttentionNotificationKindApproval
	event.Pending.Question = nil
	event.Pending.Approval = &clientui.AttentionNotificationApprovalState{Message: "approve"}
	event.Pending.Target = clientui.AttentionNotificationTarget{
		Kind:      clientui.AttentionNotificationTargetSessionPrompt,
		SessionID: "session-1",
	}
	if err := ValidateAttentionNotificationEvent(event); err != nil {
		t.Fatalf("ValidateAttentionNotificationEvent rejected session-prompt approval: %v", err)
	}
}

func TestValidateAttentionNotificationEventAcceptsInterruptedRunFocus(t *testing.T) {
	event := validAttentionNotificationEvent()
	event.Pending.ID = attentionNotificationID(clientui.AttentionNotificationKindInterruptedRun, "run-1")
	event.Pending.Kind = clientui.AttentionNotificationKindInterruptedRun
	event.Pending.Question = nil
	event.Pending.InterruptedRun = &clientui.AttentionNotificationInterruptedRunState{RunID: "run-1"}
	event.Pending.Target.RunID = "run-1"
	event.Pending.Target.Focus = &clientui.AttentionNotificationTaskDetailFocus{
		Kind:  clientui.AttentionNotificationFocusInterruptedRun,
		RunID: "run-1",
	}

	if err := ValidateAttentionNotificationEvent(event); err != nil {
		t.Fatalf("ValidateAttentionNotificationEvent rejected interrupted-run focus: %v", err)
	}
}

func TestValidateAttentionNotificationEventRejectsInterruptedRunFocusMismatch(t *testing.T) {
	event := validAttentionNotificationEvent()
	event.Pending.ID = attentionNotificationID(clientui.AttentionNotificationKindInterruptedRun, "run-1")
	event.Pending.Kind = clientui.AttentionNotificationKindInterruptedRun
	event.Pending.Question = nil
	event.Pending.InterruptedRun = &clientui.AttentionNotificationInterruptedRunState{RunID: "run-1"}
	event.Pending.Target.RunID = "run-1"
	event.Pending.Target.Focus = &clientui.AttentionNotificationTaskDetailFocus{
		Kind:  clientui.AttentionNotificationFocusInterruptedRun,
		RunID: "other-run",
	}

	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted interrupted-run focus with mismatched run_id")
	}
}

func TestAttentionNotificationEventJSONOmitsVariantAbsentFields(t *testing.T) {
	pendingFields := jsonObjectFields(t, validAttentionNotificationEvent())
	if _, ok := pendingFields["id"]; ok {
		t.Fatalf("pending event JSON carried top-level id: %s", pendingFields["id"])
	}
	if _, ok := pendingFields["occurred_at"]; ok {
		t.Fatalf("pending event JSON carried top-level occurred_at: %s", pendingFields["occurred_at"])
	}

	snapshotFields := jsonObjectFields(t, clientui.AttentionNotificationEvent{
		Sequence:  2,
		Source:    clientui.AttentionNotificationSourceSnapshot,
		Type:      clientui.AttentionNotificationEventSnapshotComplete,
		SessionID: "session-1",
	})
	if _, ok := snapshotFields["id"]; ok {
		t.Fatalf("snapshot_complete event JSON carried id: %s", snapshotFields["id"])
	}
	if _, ok := snapshotFields["occurred_at"]; ok {
		t.Fatalf("snapshot_complete event JSON carried occurred_at: %s", snapshotFields["occurred_at"])
	}

	resolvedFields := jsonObjectFields(t, clientui.AttentionNotificationEvent{
		Sequence:   3,
		Source:     clientui.AttentionNotificationSourceLive,
		Type:       clientui.AttentionNotificationEventResolved,
		ID:         attentionNotificationIDPtr(clientui.AttentionNotificationKindQuestion, "batch-1"),
		Kind:       clientui.AttentionNotificationKindQuestion,
		OccurredAt: timePtr(time.Unix(1, 0).UTC()),
	})
	if _, ok := resolvedFields["id"]; !ok {
		t.Fatal("resolved event JSON omitted id")
	}
	if _, ok := resolvedFields["occurred_at"]; !ok {
		t.Fatal("resolved event JSON omitted occurred_at")
	}
}

func validAttentionNotificationEvent() clientui.AttentionNotificationEvent {
	return clientui.AttentionNotificationEvent{
		Sequence: 1,
		Source:   clientui.AttentionNotificationSourceLive,
		Type:     clientui.AttentionNotificationEventPending,
		Pending: &clientui.AttentionNotification{
			ID:         attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1"),
			Kind:       clientui.AttentionNotificationKindQuestion,
			OccurredAt: time.Unix(1, 0).UTC(),
			Revision:   1,
			Question: &clientui.AttentionNotificationQuestionState{
				PreparedAskIDs:          []string{"ask-1", "ask-2"},
				MaterializedAskIDs:      []string{"ask-1"},
				CurrentUnresolvedAskIDs: []string{"ask-1"},
				Preview:                 "question from agent",
				DisplayCount:            2,
				MaterializedCount:       1,
			},
			Target: clientui.AttentionNotificationTarget{
				Kind:   clientui.AttentionNotificationTargetWorkflowTask,
				TaskID: "task-1",
				Focus: &clientui.AttentionNotificationTaskDetailFocus{
					Kind:   clientui.AttentionNotificationFocusQuestion,
					AskIDs: []string{"ask-1", "ask-2"},
				},
			},
		},
	}
}

func attentionNotificationID(kind clientui.AttentionNotificationKind, uuid string) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{Kind: kind, UUID: uuid}
}

func attentionNotificationIDPtr(kind clientui.AttentionNotificationKind, uuid string) *clientui.AttentionNotificationID {
	id := attentionNotificationID(kind, uuid)
	return &id
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func jsonObjectFields(t *testing.T, value clientui.AttentionNotificationEvent) map[string]json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("Unmarshal object: %v", err)
	}
	return fields
}
