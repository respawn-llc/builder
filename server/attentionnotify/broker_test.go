package attentionnotify

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestBrokerDeliversTaskDetailPendingAndTargetlessResolvedToDesktop(t *testing.T) {
	fixture := newBrokerFixture(t)
	sub := fixture.subscribeDesktop()
	scope := RoutingScope{Kind: RoutingWorkflowTask, TaskID: "task-1"}
	notification := testQuestionNotification("batch-1", 1)
	fixture.publishPending(scope, notification)
	pending := fixture.next(sub)
	if pending.Sequence != 1 || pending.Type != clientui.AttentionNotificationEventPending || pending.Pending == nil {
		t.Fatalf("pending event = %+v", pending)
	}
	if pending.Pending.Target.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		t.Fatalf("pending target = %+v", pending.Pending.Target)
	}
	fixture.publishResolved(scope, notification.ID, notification.Kind, testTime().Add(time.Second))
	resolved := fixture.next(sub)
	if resolved.Sequence != 2 || resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, notification.ID) || resolved.Pending != nil {
		t.Fatalf("resolved event = %+v", resolved)
	}
}

func TestBrokerDeliversSameIDHigherRevisionPendingUpdates(t *testing.T) {
	fixture := newBrokerFixture(t)
	sub := fixture.subscribeDesktop()
	scope := RoutingScope{Kind: RoutingWorkflowTask, TaskID: "task-1"}
	fixture.publishPending(scope, testQuestionNotification("batch-1", 1))
	fixture.publishPending(scope, testQuestionNotification("batch-1", 2))

	first := fixture.next(sub)
	second := fixture.next(sub)
	expectedID := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1")
	if first.Pending.ID != expectedID || first.Pending.Revision != 1 || second.Pending.ID != expectedID || second.Pending.Revision != 2 {
		t.Fatalf("pending updates = %+v, %+v; want ID %+v with revisions 1, 2", first.Pending, second.Pending, expectedID)
	}
}

func TestBrokerKeepsSessionPromptOffDesktopRoot(t *testing.T) {
	fixture := newBrokerFixture(t)
	desktop := fixture.subscribeDesktop()
	session := fixture.subscribeSession("session-1")
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	fixture.publishPending(scope, testSessionPromptNotification("prompt-1"))
	fixture.requireNoEvent(desktop, "desktop received session prompt event")
	if event := fixture.next(session); event.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt {
		t.Fatalf("session target = %+v", event.Pending.Target)
	}
}

func TestBrokerSessionRouteReceivesMatchingTaskDetailEvents(t *testing.T) {
	fixture := newBrokerFixture(t)
	matching := fixture.subscribeSession("session-1")
	other := fixture.subscribeSession("session-2")
	scope := RoutingScope{Kind: RoutingWorkflowTask, SessionID: "session-1", TaskID: "task-1"}
	fixture.publishPending(scope, testQuestionNotification("batch-1", 1))
	if event := fixture.next(matching); event.Pending.ID != attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1") {
		t.Fatalf("matching event = %+v", event)
	}
	fixture.requireNoEvent(other, "wrong session received event")
}

func TestBrokerDeliversScopedResolvedWithoutActiveID(t *testing.T) {
	fixture := newBrokerFixture(t)
	sub := fixture.subscribeDesktop()
	approvalID := attentionNotificationID(clientui.AttentionNotificationKindApproval, "transition-1")
	fixture.publishResolved(RoutingScope{Kind: RoutingWorkflowTask}, approvalID, clientui.AttentionNotificationKindApproval, testTime())
	event := fixture.next(sub)
	if event.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(event, approvalID) {
		t.Fatalf("resolved event = %+v", event)
	}
}

func TestBrokerEnqueuesInitialEventsOnlyForRegisteredSubscriber(t *testing.T) {
	fixture := newBrokerFixture(t)
	fixture.publishPending(RoutingScope{Kind: RoutingWorkflowTask}, testQuestionNotification("batch-1", 1))
	sub := fixture.subscribeDesktop()
	fixture.requireNoEvent(sub, "default subscription replayed old event")
	initial := clientui.AttentionNotificationEvent{
		Source:    clientui.AttentionNotificationSourceSnapshot,
		Type:      clientui.AttentionNotificationEventSnapshotComplete,
		SessionID: "session-1",
	}
	fixture.enqueueInitial(sub, RoutingScope{Kind: RoutingWorkflowTask}, initial)
	if event := fixture.next(sub); event.Type != clientui.AttentionNotificationEventSnapshotComplete {
		t.Fatalf("initial event = %+v", event)
	}
}

func TestBrokerSnapshotPendingActivatesLaterResolvedEvent(t *testing.T) {
	fixture := newBrokerFixture(t)
	sub := fixture.subscribeSession("session-1")
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	notification := testSessionPromptNotification("ask-1")
	initial := snapshotPending(notification)
	fixture.enqueueInitial(sub, scope, initial)
	if event := fixture.next(sub); event.Type != clientui.AttentionNotificationEventPending || event.Pending.ID != notification.ID {
		t.Fatalf("initial pending event = %+v", event)
	}
	fixture.publishResolved(scope, notification.ID, notification.Kind, testTime().Add(time.Second))
	resolved := fixture.next(sub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, notification.ID) {
		t.Fatalf("resolved event = %+v", resolved)
	}
}

func TestBrokerClosesLaggingSubscriberWithStreamGap(t *testing.T) {
	fixture := newBrokerFixture(t, WithBufferSize(1))
	sub := fixture.subscribeDesktop()
	scope := RoutingScope{Kind: RoutingWorkflowTask}
	fixture.publishPending(scope, testQuestionNotification("batch-1", 1))
	fixture.publishPending(scope, testQuestionNotification("batch-2", 1))
	_ = fixture.next(sub)
	_, err := sub.Next(context.Background())
	fixture.requireStreamGap("Next", err)
}

func TestBrokerInitialEnqueueOverflowReturnsStreamGap(t *testing.T) {
	fixture := newBrokerFixture(t, WithBufferSize(1))
	sub := fixture.subscribeSession("session-1")
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	first := snapshotPending(testSessionPromptNotification("ask-1"))
	second := snapshotPending(testSessionPromptNotification("ask-2"))
	fixture.enqueueInitial(sub, scope, first)
	fixture.requireStreamGap("EnqueueInitial overflow", fixture.EnqueueInitial(sub, scope, second))
	_ = fixture.next(sub)
	_, err := sub.Next(context.Background())
	fixture.requireStreamGap("Next", err)
}

func TestQuestionBatchTrackerPublishesMaterializedUpdatesAndResolvesAfterClears(t *testing.T) {
	fixture := newBrokerFixture(t)
	sub := fixture.subscribeDesktop()
	tracker, batch := fixture.questionBatch()
	fixture.noError("Prepare", tracker.Prepare(batch))
	fixture.noError("MarkMaterialized ask-1", tracker.MarkMaterialized(batch.ID, "ask-1"))
	first := fixture.next(sub)
	if first.Pending.Question.DisplayCount != 2 || len(first.Pending.Question.CurrentUnresolvedAskIDs) != 1 {
		t.Fatalf("first question state = %+v", first.Pending.Question)
	}
	if first.Pending.Question.Preview != "question from agent" {
		t.Fatalf("first preview = %q", first.Pending.Question.Preview)
	}
	batch.Preview = "later question from agent"
	fixture.noError("Prepare emitted batch update", tracker.Prepare(batch))
	fixture.noError("MarkMaterialized ask-2", tracker.MarkMaterialized(batch.ID, "ask-2"))
	second := fixture.next(sub)
	if second.Type != clientui.AttentionNotificationEventPending || second.Pending.Question.MaterializedCount != 2 || len(second.Pending.Question.CurrentUnresolvedAskIDs) != 2 {
		t.Fatalf("second materialized update = %+v", second)
	}
	if second.Pending.Revision <= first.Pending.Revision {
		t.Fatalf("second revision = %d, want > %d", second.Pending.Revision, first.Pending.Revision)
	}
	fixture.noError("MarkDurablyCleared ask-1", tracker.MarkDurablyCleared(batch.ID, "ask-1"))
	fixture.noError("MarkDurablyCleared ask-2", tracker.MarkDurablyCleared(batch.ID, "ask-2"))
	resolved := fixture.next(sub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, attentionNotificationID(clientui.AttentionNotificationKindQuestion, batch.ID)) {
		t.Fatalf("resolved event = %+v", resolved)
	}
	if err := tracker.MarkMaterialized(batch.ID, "ask-1"); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("MarkMaterialized after resolved = %v, want ErrBatchNotFound", err)
	}
}

func TestQuestionBatchTrackerResolvesWithoutRepublishingWhenAskSkipped(t *testing.T) {
	fixture := newBrokerFixture(t)
	sub := fixture.subscribeDesktop()
	tracker, batch := fixture.questionBatch()
	fixture.noError("Prepare", tracker.Prepare(batch))
	fixture.noError("MarkMaterialized ask-1", tracker.MarkMaterialized(batch.ID, "ask-1"))
	_ = fixture.next(sub)
	fixture.noError("MarkSkipped ask-2", tracker.MarkSkipped(batch.ID, "ask-2"))
	fixture.requireNoEvent(sub, "skipped ask published duplicate pending attention")
	fixture.noError("MarkDurablyCleared ask-1", tracker.MarkDurablyCleared(batch.ID, "ask-1"))
	resolved := fixture.next(sub)
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, attentionNotificationID(clientui.AttentionNotificationKindQuestion, batch.ID)) {
		t.Fatalf("resolved event = %+v", resolved)
	}
}

type brokerFixture struct {
	*testing.T
	*Broker
}

func newBrokerFixture(t *testing.T, options ...Option) brokerFixture {
	t.Helper()
	return brokerFixture{T: t, Broker: NewBroker(options...)}
}

func (f brokerFixture) subscribeDesktop() *Subscription {
	f.Helper()
	sub, err := f.SubscribeDesktop()
	f.noError("SubscribeDesktop", err)
	return sub
}

func (f brokerFixture) subscribeSession(sessionID string) *Subscription {
	f.Helper()
	sub, err := f.SubscribeSession(sessionID)
	f.noError("SubscribeSession", err)
	return sub
}

func (f brokerFixture) publishPending(scope RoutingScope, notification clientui.AttentionNotification) {
	f.Helper()
	f.noError("PublishPending", f.PublishPending(scope, notification))
}

func (f brokerFixture) publishResolved(scope RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, occurredAt time.Time) {
	f.Helper()
	f.noError("PublishResolved", f.PublishResolved(scope, id, kind, occurredAt))
}

func (f brokerFixture) enqueueInitial(sub *Subscription, scope RoutingScope, event clientui.AttentionNotificationEvent) {
	f.Helper()
	f.noError("EnqueueInitial", f.EnqueueInitial(sub, scope, event))
}

func (f brokerFixture) next(sub *Subscription) clientui.AttentionNotificationEvent {
	f.Helper()
	event, err := sub.Next(context.Background())
	f.noError("Next", err)
	return event
}

func (f brokerFixture) requireNoEvent(sub *Subscription, failure string) {
	f.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	event, err := sub.Next(ctx)
	if err == nil {
		f.Fatalf("%s: %+v", failure, event)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		f.Fatalf("%s: Next error = %v, want context deadline", failure, err)
	}
}

func (f brokerFixture) requireStreamGap(operation string, err error) {
	f.Helper()
	if !errors.Is(err, serverapi.ErrStreamGap) {
		f.Fatalf("%s error = %v, want ErrStreamGap", operation, err)
	}
}

func (f brokerFixture) noError(operation string, err error) {
	f.Helper()
	if err != nil {
		f.Fatalf("%s: %v", operation, err)
	}
}

func (f brokerFixture) questionBatch() (*QuestionBatchTracker, QuestionBatch) {
	return NewQuestionBatchTracker(f.Broker), QuestionBatch{
		ID:             "batch-1",
		Route:          RoutingScope{Kind: RoutingWorkflowTask, TaskID: "task-1"},
		Target:         testQuestionTarget(),
		Preview:        "question from agent",
		PreparedAskIDs: []string{"ask-1", "ask-2"},
		OccurredAt:     testTime(),
	}
}

func snapshotPending(notification clientui.AttentionNotification) clientui.AttentionNotificationEvent {
	return clientui.AttentionNotificationEvent{
		Source:  clientui.AttentionNotificationSourceSnapshot,
		Type:    clientui.AttentionNotificationEventPending,
		Pending: &notification,
	}
}

func attentionNotificationID(kind clientui.AttentionNotificationKind, uuid string) clientui.AttentionNotificationID {
	return clientui.AttentionNotificationID{Kind: kind, UUID: uuid}
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
	workflowID := runtimeids.NewWorkflowID()
	return clientui.AttentionNotificationTarget{
		Kind:        clientui.AttentionNotificationTargetWorkflowTask,
		WorkflowID:  &workflowID,
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
