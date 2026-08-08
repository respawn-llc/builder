package attentionnotify

import (
	"testing"

	"core/shared/clientui"
)

func TestQuestionBatchTrackerKeysAndPublishesByStepIdentity(t *testing.T) {
	fixture := newBrokerFixture(t)
	tracker := NewQuestionBatchTracker(fixture.Broker)
	sub := fixture.subscribeDesktop()
	stepID := "22222222-2222-4222-8222-222222222222"
	batch := QuestionBatch{
		StepID:         stepID,
		Route:          RoutingScope{Kind: RoutingWorkflowTask, TaskID: "task-1"},
		Target:         testQuestionTarget(),
		Preview:        "question from agent",
		PreparedAskIDs: []string{"ask-1", "ask-2"},
		OccurredAt:     testTime(),
	}
	if err := tracker.Prepare(batch); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := tracker.MarkMaterialized(stepID, "ask-1"); err != nil {
		t.Fatalf("MarkMaterialized: %v", err)
	}
	event := fixture.next(sub)
	want := clientui.AttentionNotificationID{
		Kind: clientui.AttentionNotificationKindQuestion,
		UUID: stepID,
	}
	if event.Pending == nil || event.Pending.ID != want {
		t.Fatalf("pending notification ID = %+v, want %+v", event.Pending, want)
	}
}
