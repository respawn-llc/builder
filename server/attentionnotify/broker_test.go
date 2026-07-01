package attentionnotify

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestBrokerDeliversTaskDetailPendingAndTargetlessResolvedToDesktop(t *testing.T) {
	broker := NewBroker()
	sub, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	scope := RoutingScope{Kind: RoutingWorkflowTask, TaskID: "task-1"}
	notification := testQuestionNotification("batch-1", 1)
	if err := broker.PublishPending(scope, notification); err != nil {
		t.Fatalf("PublishPending: %v", err)
	}
	pending := nextAttentionEvent(t, sub)
	if pending.Sequence != 1 || pending.Type != clientui.AttentionNotificationEventPending || pending.Pending == nil {
		t.Fatalf("pending event = %+v", pending)
	}
	if pending.Pending.Target.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		t.Fatalf("pending target = %+v", pending.Pending.Target)
	}
	if err := broker.PublishResolved(scope, notification.ID, notification.Kind, testTime().Add(time.Second)); err != nil {
		t.Fatalf("PublishResolved: %v", err)
	}
	resolved := nextAttentionEvent(t, sub)
	if resolved.Sequence != 2 || resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, notification.ID) || resolved.Pending != nil {
		t.Fatalf("resolved event = %+v", resolved)
	}
}

func TestBrokerDeliversSameIDHigherRevisionPendingUpdates(t *testing.T) {
	broker := NewBroker()
	sub, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	scope := RoutingScope{Kind: RoutingWorkflowTask, TaskID: "task-1"}
	if err := broker.PublishPending(scope, testQuestionNotification("batch-1", 1)); err != nil {
		t.Fatalf("PublishPending rev1: %v", err)
	}
	if err := broker.PublishPending(scope, testQuestionNotification("batch-1", 2)); err != nil {
		t.Fatalf("PublishPending rev2: %v", err)
	}

	first := nextAttentionEvent(t, sub)
	second := nextAttentionEvent(t, sub)
	if first.Pending.Revision != 1 || second.Pending.Revision != 2 {
		t.Fatalf("revisions = %d, %d; want 1, 2", first.Pending.Revision, second.Pending.Revision)
	}
}

func TestBrokerKeepsSessionPromptOffDesktopRoot(t *testing.T) {
	broker := NewBroker()
	desktop, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	session, err := broker.SubscribeSession("session-1")
	if err != nil {
		t.Fatalf("SubscribeSession: %v", err)
	}
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	if err := broker.PublishPending(scope, testSessionPromptNotification("prompt-1")); err != nil {
		t.Fatalf("PublishPending: %v", err)
	}
	if event, err := desktop.Next(shortContext(t)); err == nil {
		t.Fatalf("desktop received session prompt event: %+v", event)
	}
	if event := nextAttentionEvent(t, session); event.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt {
		t.Fatalf("session target = %+v", event.Pending.Target)
	}
}

func TestBrokerSessionRouteReceivesMatchingTaskDetailEvents(t *testing.T) {
	broker := NewBroker()
	matching, err := broker.SubscribeSession("session-1")
	if err != nil {
		t.Fatalf("SubscribeSession matching: %v", err)
	}
	other, err := broker.SubscribeSession("session-2")
	if err != nil {
		t.Fatalf("SubscribeSession other: %v", err)
	}
	scope := RoutingScope{Kind: RoutingWorkflowTask, SessionID: "session-1", TaskID: "task-1"}
	if err := broker.PublishPending(scope, testQuestionNotification("batch-1", 1)); err != nil {
		t.Fatalf("PublishPending: %v", err)
	}
	if event := nextAttentionEvent(t, matching); attentionNotificationIDEmpty(event.Pending.ID) {
		t.Fatalf("matching event = %+v", event)
	}
	if event, err := other.Next(shortContext(t)); err == nil {
		t.Fatalf("wrong session received event: %+v", event)
	}
}

func TestBrokerDeliversScopedResolvedWithoutActiveID(t *testing.T) {
	broker := NewBroker()
	sub, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	approvalID := attentionNotificationID(clientui.AttentionNotificationKindApproval, "transition-1")
	if err := broker.PublishResolved(RoutingScope{Kind: RoutingWorkflowTask}, approvalID, clientui.AttentionNotificationKindApproval, testTime()); err != nil {
		t.Fatalf("PublishResolved: %v", err)
	}
	event := nextAttentionEvent(t, sub)
	if event.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(event, approvalID) {
		t.Fatalf("resolved event = %+v", event)
	}
}

func TestBrokerEnqueuesInitialEventsOnlyForRegisteredSubscriber(t *testing.T) {
	broker := NewBroker()
	if err := broker.PublishPending(RoutingScope{Kind: RoutingWorkflowTask}, testQuestionNotification("batch-1", 1)); err != nil {
		t.Fatalf("PublishPending before subscribe: %v", err)
	}
	sub, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	if event, err := sub.Next(shortContext(t)); err == nil {
		t.Fatalf("default subscription replayed old event: %+v", event)
	}
	initial := clientui.AttentionNotificationEvent{
		Source:    clientui.AttentionNotificationSourceSnapshot,
		Type:      clientui.AttentionNotificationEventSnapshotComplete,
		SessionID: "session-1",
	}
	if err := broker.EnqueueInitial(sub, RoutingScope{Kind: RoutingWorkflowTask}, initial); err != nil {
		t.Fatalf("EnqueueInitial: %v", err)
	}
	if event := nextAttentionEvent(t, sub); event.Type != clientui.AttentionNotificationEventSnapshotComplete {
		t.Fatalf("initial event = %+v", event)
	}
}

func TestBrokerSnapshotPendingActivatesLaterResolvedEvent(t *testing.T) {
	broker := NewBroker()
	sub, err := broker.SubscribeSession("session-1")
	if err != nil {
		t.Fatalf("SubscribeSession: %v", err)
	}
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	notification := testSessionPromptNotification("ask-1")
	initial := clientui.AttentionNotificationEvent{
		Source:  clientui.AttentionNotificationSourceSnapshot,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &notification,
	}
	if err := broker.EnqueueInitial(sub, scope, initial); err != nil {
		t.Fatalf("EnqueueInitial: %v", err)
	}
	if event := nextAttentionEvent(t, sub); event.Type != clientui.AttentionNotificationEventPending || event.Pending.ID != notification.ID {
		t.Fatalf("initial pending event = %+v", event)
	}
	if err := broker.PublishResolved(scope, notification.ID, notification.Kind, testTime().Add(time.Second)); err != nil {
		t.Fatalf("PublishResolved: %v", err)
	}
	resolved := nextAttentionEvent(t, sub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, notification.ID) {
		t.Fatalf("resolved event = %+v", resolved)
	}
}

func TestBrokerClosesLaggingSubscriberWithStreamGap(t *testing.T) {
	broker := NewBroker(WithBufferSize(1))
	sub, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	scope := RoutingScope{Kind: RoutingWorkflowTask}
	if err := broker.PublishPending(scope, testQuestionNotification("batch-1", 1)); err != nil {
		t.Fatalf("PublishPending first: %v", err)
	}
	if err := broker.PublishPending(scope, testQuestionNotification("batch-2", 1)); err != nil {
		t.Fatalf("PublishPending second: %v", err)
	}
	_ = nextAttentionEvent(t, sub)
	_, err = sub.Next(context.Background())
	if !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("Next error = %v, want ErrStreamGap", err)
	}
}

func TestBrokerInitialEnqueueOverflowReturnsStreamGap(t *testing.T) {
	broker := NewBroker(WithBufferSize(1))
	sub, err := broker.SubscribeSession("session-1")
	if err != nil {
		t.Fatalf("SubscribeSession: %v", err)
	}
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	firstNotification := testSessionPromptNotification("ask-1")
	secondNotification := testSessionPromptNotification("ask-2")
	first := clientui.AttentionNotificationEvent{
		Source:  clientui.AttentionNotificationSourceSnapshot,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &firstNotification,
	}
	second := clientui.AttentionNotificationEvent{
		Source:  clientui.AttentionNotificationSourceSnapshot,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &secondNotification,
	}
	if err := broker.EnqueueInitial(sub, scope, first); err != nil {
		t.Fatalf("EnqueueInitial first: %v", err)
	}
	if err := broker.EnqueueInitial(sub, scope, second); !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("EnqueueInitial overflow error = %v, want ErrStreamGap", err)
	}
	_ = nextAttentionEvent(t, sub)
	if _, err := sub.Next(context.Background()); !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("Next error = %v, want ErrStreamGap", err)
	}
}

func TestQuestionBatchTrackerPublishesAggregateDisplayCountAndResolvesAfterClears(t *testing.T) {
	broker := NewBroker()
	sub, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	tracker := NewQuestionBatchTracker(broker)
	batch := QuestionBatch{
		ID:             "batch-1",
		Route:          RoutingScope{Kind: RoutingWorkflowTask, TaskID: "task-1"},
		Target:         testQuestionTarget(),
		Preview:        "question from agent",
		PreparedAskIDs: []string{"ask-1", "ask-2"},
		OccurredAt:     testTime(),
	}
	if err := tracker.Prepare(batch); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := tracker.MarkMaterialized(batch.ID, "ask-1"); err != nil {
		t.Fatalf("MarkMaterialized ask-1: %v", err)
	}
	first := nextAttentionEvent(t, sub)
	if first.Pending.Question.DisplayCount != 2 || len(first.Pending.Question.CurrentUnresolvedAskIDs) != 1 {
		t.Fatalf("first question state = %+v", first.Pending.Question)
	}
	if first.Pending.Question.Preview != "question from agent" {
		t.Fatalf("first preview = %q", first.Pending.Question.Preview)
	}
	batch.Preview = "later question from agent"
	if err := tracker.Prepare(batch); err != nil {
		t.Fatalf("Prepare emitted batch update: %v", err)
	}
	if err := tracker.MarkMaterialized(batch.ID, "ask-2"); err != nil {
		t.Fatalf("MarkMaterialized ask-2: %v", err)
	}
	update := nextAttentionEvent(t, sub)
	if update.Pending.Question.Preview != "question from agent" {
		t.Fatalf("updated preview = %q", update.Pending.Question.Preview)
	}
	if err := tracker.MarkDurablyCleared(batch.ID, "ask-1"); err != nil {
		t.Fatalf("MarkDurablyCleared ask-1: %v", err)
	}
	if err := tracker.MarkDurablyCleared(batch.ID, "ask-2"); err != nil {
		t.Fatalf("MarkDurablyCleared ask-2: %v", err)
	}
	resolved := nextAttentionEvent(t, sub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, attentionNotificationID(clientui.AttentionNotificationKindQuestion, batch.ID)) {
		t.Fatalf("resolved event = %+v", resolved)
	}
	if err := tracker.MarkMaterialized(batch.ID, "ask-1"); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("MarkMaterialized after resolved = %v, want ErrBatchNotFound", err)
	}
}

func TestQuestionBatchTrackerUpdatesDisplayCountWhenAskSkipped(t *testing.T) {
	broker := NewBroker()
	sub, err := broker.SubscribeDesktop()
	if err != nil {
		t.Fatalf("SubscribeDesktop: %v", err)
	}
	tracker := NewQuestionBatchTracker(broker)
	batch := QuestionBatch{
		ID:             "batch-1",
		Route:          RoutingScope{Kind: RoutingWorkflowTask, TaskID: "task-1"},
		Target:         testQuestionTarget(),
		Preview:        "question from agent",
		PreparedAskIDs: []string{"ask-1", "ask-2"},
		OccurredAt:     testTime(),
	}
	if err := tracker.Prepare(batch); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := tracker.MarkMaterialized(batch.ID, "ask-1"); err != nil {
		t.Fatalf("MarkMaterialized ask-1: %v", err)
	}
	_ = nextAttentionEvent(t, sub)
	if err := tracker.MarkSkipped(batch.ID, "ask-2"); err != nil {
		t.Fatalf("MarkSkipped ask-2: %v", err)
	}
	update := nextAttentionEvent(t, sub)
	if update.Pending.Question.DisplayCount != 1 {
		t.Fatalf("skip update question count = %+v", update.Pending.Question)
	}
}

func nextAttentionEvent(t *testing.T, sub *Subscription) clientui.AttentionNotificationEvent {
	t.Helper()
	event, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

func shortContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func attentionNotificationID(kind clientui.AttentionNotificationKind, uuid string) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{Kind: kind, UUID: uuid}
}

func attentionNotificationIDEmpty(id clientui.AttentionNotificationID) bool {
	return id.Kind == "" && id.UUID == ""
}

func attentionNotificationEventIDMatches(event clientui.AttentionNotificationEvent, id clientui.AttentionNotificationID) bool {
	return event.ID != nil && *event.ID == id
}

func testQuestionNotification(id string, revision uint64) clientui.AttentionNotification {
	return clientui.AttentionNotification{
		ID:         attentionNotificationID(clientui.AttentionNotificationKindQuestion, id),
		Kind:       clientui.AttentionNotificationKindQuestion,
		OccurredAt: testTime(),
		Revision:   revision,
		Question: &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{"ask-1", "ask-2"},
			MaterializedAskIDs:      []string{"ask-1"},
			CurrentUnresolvedAskIDs: []string{"ask-1"},
			Preview:                 "question from agent",
			DisplayCount:            2,
			MaterializedCount:       1,
		},
		Target: testQuestionTarget(),
	}
}

func testSessionPromptNotification(id string) clientui.AttentionNotification {
	return clientui.AttentionNotification{
		ID:         attentionNotificationID(clientui.AttentionNotificationKindQuestion, id),
		Kind:       clientui.AttentionNotificationKindQuestion,
		OccurredAt: testTime(),
		Revision:   1,
		Target: clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetSessionPrompt,
			SessionID: "session-1",
		},
		Question: &clientui.AttentionNotificationQuestionState{
			PreparedAskIDs:          []string{id},
			MaterializedAskIDs:      []string{id},
			CurrentUnresolvedAskIDs: []string{id},
			Preview:                 "question from agent",
			DisplayCount:            1,
			MaterializedCount:       1,
		},
	}
}

func testQuestionTarget() clientui.AttentionNotificationTarget {
	return clientui.AttentionNotificationTarget{
		Kind:        clientui.AttentionNotificationTargetWorkflowTask,
		TaskID:      "task-1",
		TaskShortID: "KT-1",
		Focus: &clientui.AttentionNotificationTaskDetailFocus{
			Kind:   clientui.AttentionNotificationFocusQuestion,
			AskIDs: []string{"ask-1", "ask-2"},
		},
	}
}

func testTime() time.Time {
	return time.Unix(1, 0).UTC()
}
