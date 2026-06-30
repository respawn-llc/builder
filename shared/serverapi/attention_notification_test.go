package serverapi

import (
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
		ID:        "prompt:session-1:prompt-1",
	}

	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted snapshot_complete with id payload")
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
		ID:         "approval:transition-1",
		Kind:       clientui.AttentionNotificationKind("other"),
		OccurredAt: time.Unix(1, 0).UTC(),
	}
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted unsupported resolved kind")
	}
}

func TestValidateAttentionNotificationEventRejectsKindTargetFocusMismatch(t *testing.T) {
	event := validAttentionNotificationEvent()
	event.Pending.Kind = clientui.AttentionNotificationKindApproval
	event.Pending.Question = nil
	event.Pending.Target.Focus.Kind = clientui.AttentionNotificationFocusQuestion
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted task-detail approval with question focus")
	}

	event = validAttentionNotificationEvent()
	event.Pending.Kind = clientui.AttentionNotificationKindApproval
	event.Pending.Question = &clientui.AttentionNotificationQuestionState{PreparedAskIDs: []string{"ask-1"}}
	event.Pending.Target.Focus = &clientui.AttentionNotificationTaskDetailFocus{
		Kind:             clientui.AttentionNotificationFocusApproval,
		TaskTransitionID: "transition-1",
	}
	if err := ValidateAttentionNotificationEvent(event); err == nil {
		t.Fatal("ValidateAttentionNotificationEvent accepted approval with question state")
	}

	event = validAttentionNotificationEvent()
	event.Pending.Kind = clientui.AttentionNotificationKindApproval
	event.Pending.Question = nil
	event.Pending.Target = clientui.AttentionNotificationTarget{
		Kind:      clientui.AttentionNotificationTargetSessionPrompt,
		SessionID: "session-1",
	}
	if err := ValidateAttentionNotificationEvent(event); err != nil {
		t.Fatalf("ValidateAttentionNotificationEvent rejected session-prompt approval: %v", err)
	}
}

func validAttentionNotificationEvent() clientui.AttentionNotificationEvent {
	return clientui.AttentionNotificationEvent{
		Sequence: 1,
		Source:   clientui.AttentionNotificationSourceLive,
		Type:     clientui.AttentionNotificationEventPending,
		Pending: &clientui.AttentionNotification{
			ID:         "question_batch:run-1:batch-1",
			Kind:       clientui.AttentionNotificationKindQuestion,
			OccurredAt: time.Unix(1, 0).UTC(),
			Revision:   1,
			Question: &clientui.AttentionNotificationQuestionState{
				PreparedAskIDs:          []string{"ask-1", "ask-2"},
				MaterializedAskIDs:      []string{"ask-1"},
				CurrentUnresolvedAskIDs: []string{"ask-1"},
				DisplayCount:            2,
				MaterializedCount:       1,
			},
			Target: clientui.AttentionNotificationTarget{
				Kind:   clientui.AttentionNotificationTargetTaskDetail,
				TaskID: "task-1",
				Focus: &clientui.AttentionNotificationTaskDetailFocus{
					Kind:   clientui.AttentionNotificationFocusQuestion,
					AskIDs: []string{"ask-1", "ask-2"},
				},
			},
			Presentation: clientui.AttentionNotificationPresentation{
				Title: "KT-1: 2 questions",
				Body:  "question from agent",
				Count: 2,
			},
		},
	}
}
