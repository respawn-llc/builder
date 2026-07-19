package attentionnotify

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/invariant"
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

func TestBrokerSnapshotSubscriptionDoesNotReplayEarlierLiveEvents(t *testing.T) {
	fixture := newBrokerFixture(t)
	fixture.publishPending(RoutingScope{Kind: RoutingWorkflowTask}, testQuestionNotification("batch-1", 1))
	sub, err := fixture.SubscribeSessionSnapshot("session-1", nil, 0)
	fixture.noError("SubscribeSessionSnapshot", err)
	if event := fixture.nextSnapshot(sub); event.Type != clientui.AttentionNotificationEventSnapshotComplete {
		t.Fatalf("snapshot complete = %+v", event)
	}
	fixture.requireNoSnapshotEvent(sub, "snapshot subscription replayed old live event")
}

func TestBrokerSnapshotPendingActivatesLaterResolvedEvent(t *testing.T) {
	fixture := newBrokerFixture(t)
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	notification := testSessionPromptNotification("ask-1")
	sub, err := fixture.SubscribeSessionSnapshot("session-1", []SnapshotPendingDescriptor{{
		Notification: notification,
	}}, 0)
	fixture.noError("SubscribeSessionSnapshot", err)
	if event := fixture.nextSnapshot(sub); event.Type != clientui.AttentionNotificationEventPending || event.Pending.ID != notification.ID {
		t.Fatalf("snapshot pending event = %+v", event)
	}
	if event := fixture.nextSnapshot(sub); event.Type != clientui.AttentionNotificationEventSnapshotComplete {
		t.Fatalf("snapshot complete event = %+v", event)
	}
	fixture.publishResolved(scope, notification.ID, notification.Kind, testTime().Add(time.Second))
	resolved := fixture.nextSnapshot(sub)
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

func TestBrokerSnapshotSubscriptionDoesNotConsumeLiveBufferAndPreservesLiveOverflow(t *testing.T) {
	fixture := newBrokerFixture(t, WithBufferSize(1))
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	descriptors := []SnapshotPendingDescriptor{
		{Notification: testSessionPromptNotification("ask-1")},
		{Notification: testSessionPromptNotification("ask-2")},
		{Notification: testSessionPromptNotification("ask-3")},
	}
	sub, err := fixture.SubscribeSessionSnapshot("session-1", descriptors, 0)
	fixture.noError("SubscribeSessionSnapshot", err)
	fixture.publishPending(scope, testSessionPromptNotification("live-1"))
	fixture.publishPending(scope, testSessionPromptNotification("live-2"))

	for range descriptors {
		event := fixture.nextSnapshot(sub)
		if event.Source != clientui.AttentionNotificationSourceSnapshot || event.Type != clientui.AttentionNotificationEventPending {
			t.Fatalf("snapshot event = %+v", event)
		}
	}
	if event := fixture.nextSnapshot(sub); event.Type != clientui.AttentionNotificationEventSnapshotComplete {
		t.Fatalf("snapshot complete = %+v", event)
	}
	if event := fixture.nextSnapshot(sub); event.Pending == nil || event.Pending.ID.UUID != "live-1" {
		t.Fatalf("first live event = %+v", event)
	}
	_, err = sub.Next(context.Background())
	fixture.requireStreamGap("live overflow", err)
}

func TestBrokerKeepsOccurrenceMetadataInsideEnvelopes(t *testing.T) {
	fixture := newBrokerFixture(t)
	live := fixture.subscribeSession("session-1")
	ordinary := NewOrdinaryOccurrenceMetadata(7)
	fixture.noError("PublishPendingWithOccurrence", fixture.PublishPendingWithOccurrence(
		RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"},
		testSessionPromptNotification("ordinary"),
		ordinary,
	))
	liveEnvelope, err := live.nextEnvelope(context.Background())
	fixture.noError("nextEnvelope", err)
	if ordinal, ok := liveEnvelope.occurrence.OrdinaryOrdinal(); !ok || ordinal != 7 {
		t.Fatalf("live ordinary occurrence = %d / %t, want 7 / true", ordinal, ok)
	}

	batch := NewTaskQuestionBatchOccurrenceMetadata("batch-1")
	snapshot, err := fixture.SubscribeSessionSnapshot("session-1", []SnapshotPendingDescriptor{{
		Notification: testSessionPromptNotification("task-batch"),
		Occurrence:   batch,
	}}, 7)
	fixture.noError("SubscribeSessionSnapshot", err)
	if snapshot.OpeningOrdinaryWatermark() != 7 {
		t.Fatalf("opening ordinary watermark = %d, want 7", snapshot.OpeningOrdinaryWatermark())
	}
	if len(snapshot.snapshot) != 2 {
		t.Fatalf("snapshot envelopes = %+v, want descriptor plus complete marker", snapshot.snapshot)
	}
	key, ok := snapshot.snapshot[0].occurrence.TaskQuestionBatchKey()
	wantKey, wantOK := batch.TaskQuestionBatchKey()
	if !ok || !wantOK || key != wantKey {
		t.Fatalf("snapshot task batch key = %v / %t, want %v / %t", key, ok, wantKey, wantOK)
	}
}

func TestBrokerSnapshotSubscriptionSuppressesDelayedOrdinaryOccurrencesAtOpeningWatermark(t *testing.T) {
	fixture := newBrokerFixture(t, WithBufferSize(1))
	scope := RoutingScope{Kind: RoutingSessionPrompt, SessionID: "session-1"}
	snapshotNotification := testSessionPromptNotification("snapshot-ordinary")
	sub, err := fixture.SubscribeSessionSnapshot("session-1", []SnapshotPendingDescriptor{{
		Notification: snapshotNotification,
		Occurrence:   NewOrdinaryOccurrenceMetadata(1),
	}}, 1)
	fixture.noError("SubscribeSessionSnapshot", err)
	_ = fixture.nextSnapshot(sub)
	_ = fixture.nextSnapshot(sub)

	fixture.noError("PublishPendingWithOccurrence delayed", fixture.PublishPendingWithOccurrence(
		scope,
		testSessionPromptNotification("delayed-pending"),
		NewOrdinaryOccurrenceMetadata(1),
	))
	delayedID := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "delayed-resolved")
	fixture.noError("PublishResolvedWithOccurrence delayed", fixture.PublishResolvedWithOccurrence(
		scope,
		delayedID,
		clientui.AttentionNotificationKindQuestion,
		testTime(),
		NewOrdinaryOccurrenceMetadata(1),
	))
	fixture.noError("PublishPendingWithOccurrence later", fixture.PublishPendingWithOccurrence(
		scope,
		testSessionPromptNotification("later-pending"),
		NewOrdinaryOccurrenceMetadata(2),
	))

	event := fixture.nextSnapshot(sub)
	if event.Type != clientui.AttentionNotificationEventPending || event.Pending == nil || event.Pending.ID.UUID != "later-pending" {
		t.Fatalf("projected event = %+v, want only later ordinary pending", event)
	}
}

func TestBrokerSnapshotSubscriptionInitializesTaskBatchKeyFromSnapshot(t *testing.T) {
	fixture := newBrokerFixture(t, WithBufferSize(2))
	scope := RoutingScope{Kind: RoutingWorkflowTask, SessionID: "session-1", TaskID: "task-1"}
	occurrence := NewTaskQuestionBatchOccurrenceMetadata("batch-1")
	sub, err := fixture.SubscribeSessionSnapshot("session-1", []SnapshotPendingDescriptor{{
		Notification: testQuestionNotification("batch-1", 1),
		Occurrence:   occurrence,
	}}, 0)
	fixture.noError("SubscribeSessionSnapshot", err)
	_ = fixture.nextSnapshot(sub)
	_ = fixture.nextSnapshot(sub)

	fixture.noError("PublishPendingWithOccurrence", fixture.PublishPendingWithOccurrence(
		scope,
		testQuestionNotification("batch-1", 2),
		occurrence,
	))
	id := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1")
	fixture.noError("PublishResolvedWithOccurrence", fixture.PublishResolvedWithOccurrence(
		scope,
		id,
		clientui.AttentionNotificationKindQuestion,
		testTime(),
		occurrence,
	))

	event := fixture.nextSnapshot(sub)
	if event.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(event, id) {
		t.Fatalf("projected event = %+v, want final task batch resolved", event)
	}
	nextOccurrence := NewTaskQuestionBatchOccurrenceMetadata("batch-2")
	fixture.noError("PublishPendingWithOccurrence after final resolve", fixture.PublishPendingWithOccurrence(
		scope,
		testQuestionNotification("batch-2", 1),
		nextOccurrence,
	))
	event = fixture.nextSnapshot(sub)
	if event.Type != clientui.AttentionNotificationEventPending || event.Pending == nil || event.Pending.ID.UUID != "batch-2" {
		t.Fatalf("projected event after final task batch resolve = %+v, want new task batch pending", event)
	}
}

func TestBrokerSnapshotSubscriptionProjectsFirstLiveTaskBatchOnlyOnceUntilResolved(t *testing.T) {
	fixture := newBrokerFixture(t, WithBufferSize(1))
	scope := RoutingScope{Kind: RoutingWorkflowTask, SessionID: "session-1", TaskID: "task-1"}
	occurrence := NewTaskQuestionBatchOccurrenceMetadata("batch-1")
	sub, err := fixture.SubscribeSessionSnapshot("session-1", nil, 0)
	fixture.noError("SubscribeSessionSnapshot", err)
	_ = fixture.nextSnapshot(sub)

	fixture.noError("PublishPendingWithOccurrence first", fixture.PublishPendingWithOccurrence(
		scope,
		testQuestionNotification("batch-1", 7),
		occurrence,
	))
	first := fixture.nextSnapshot(sub)
	if first.Type != clientui.AttentionNotificationEventPending || first.Pending == nil || first.Pending.Revision != 7 {
		t.Fatalf("first task batch event = %+v, want revision 7 pending", first)
	}

	fixture.noError("PublishPendingWithOccurrence duplicate", fixture.PublishPendingWithOccurrence(
		scope,
		testQuestionNotification("batch-1", 8),
		occurrence,
	))
	id := attentionNotificationID(clientui.AttentionNotificationKindQuestion, "batch-1")
	fixture.noError("PublishResolvedWithOccurrence", fixture.PublishResolvedWithOccurrence(
		scope,
		id,
		clientui.AttentionNotificationKindQuestion,
		testTime(),
		occurrence,
	))

	event := fixture.nextSnapshot(sub)
	if event.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(event, id) {
		t.Fatalf("projected event = %+v, want final task batch resolved", event)
	}
}

func TestBrokerSnapshotSubscriptionClosesWithTypedDiscontinuityForSecondTaskBatchKey(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", string(invariant.ModeDiagnostic))
	fixture := newBrokerFixture(t, WithBufferSize(2))
	scope := RoutingScope{Kind: RoutingWorkflowTask, SessionID: "session-1", TaskID: "task-1"}
	firstKey := NewTaskQuestionBatchOccurrenceMetadata("batch-1")
	sub, err := fixture.SubscribeSessionSnapshot("session-1", []SnapshotPendingDescriptor{{
		Notification: testQuestionNotification("batch-1", 7),
		Occurrence:   firstKey,
	}}, 3)
	fixture.noError("SubscribeSessionSnapshot", err)
	_ = fixture.nextSnapshot(sub)
	_ = fixture.nextSnapshot(sub)

	secondKey := NewTaskQuestionBatchOccurrenceMetadata("batch-2")
	fixture.noError("PublishPendingWithOccurrence second", fixture.PublishPendingWithOccurrence(
		scope,
		testQuestionNotification("batch-2", 8),
		secondKey,
	))

	_, err = sub.Next(context.Background())
	var discontinuity AttentionProjectionDiscontinuity
	if !errors.As(err, &discontinuity) {
		t.Fatalf("Next error = %T %v, want AttentionProjectionDiscontinuity", err, err)
	}
	if !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("Next error = %v, want ErrStreamGap", err)
	}
	if !discontinuity.RequiresSnapshotReopen() {
		t.Fatal("typed discontinuity did not require a fresh snapshot")
	}
	if discontinuity.Diagnostic.Scope != invariant.ScopeAttentionProjection {
		t.Fatalf("diagnostic scope = %q, want %q", discontinuity.Diagnostic.Scope, invariant.ScopeAttentionProjection)
	}
	for _, field := range []invariant.Field{
		invariant.FieldSessionID,
		invariant.FieldSubscriptionGeneration,
		invariant.FieldOpeningWatermark,
		invariant.FieldObservedTaskBatchKey,
		invariant.FieldIncomingTaskBatchKey,
		invariant.FieldAttentionEventSource,
		invariant.FieldAttentionEventRevision,
	} {
		if discontinuity.Diagnostic.Fields[field] == "" {
			t.Fatalf("diagnostic %q is empty: %+v", field, discontinuity.Diagnostic)
		}
	}
	if discontinuity.Diagnostic.Stack == "" {
		t.Fatal("typed discontinuity diagnostic has no stack")
	}
	fixture.mu.Lock()
	subscriberCount := len(fixture.subscribers)
	fixture.mu.Unlock()
	if subscriberCount != 0 {
		t.Fatalf("broker subscribers after discontinuity = %d, want 0", subscriberCount)
	}
	_, repeatErr := sub.Next(context.Background())
	if !errors.As(repeatErr, &discontinuity) {
		t.Fatalf("Next after close error = %T %v, want AttentionProjectionDiscontinuity", repeatErr, repeatErr)
	}
}

func TestBrokerSnapshotSubscriptionPanicsForSecondTaskBatchKeyInDebug(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", string(invariant.ModePanic))
	fixture := newBrokerFixture(t, WithBufferSize(2))
	scope := RoutingScope{Kind: RoutingWorkflowTask, SessionID: "session-1", TaskID: "task-1"}
	sub, err := fixture.SubscribeSessionSnapshot("session-1", []SnapshotPendingDescriptor{{
		Notification: testQuestionNotification("batch-1", 1),
		Occurrence:   NewTaskQuestionBatchOccurrenceMetadata("batch-1"),
	}}, 0)
	fixture.noError("SubscribeSessionSnapshot", err)
	_ = fixture.nextSnapshot(sub)
	_ = fixture.nextSnapshot(sub)

	defer func() {
		recovered := recover()
		diagnostic, ok := recovered.(invariant.Diagnostic)
		if !ok {
			t.Fatalf("panic = %T %v, want invariant.Diagnostic", recovered, recovered)
		}
		if diagnostic.Scope != invariant.ScopeAttentionProjection {
			t.Fatalf("panic diagnostic scope = %q, want %q", diagnostic.Scope, invariant.ScopeAttentionProjection)
		}
		if diagnostic.Stack == "" {
			t.Fatal("panic diagnostic has no stack")
		}
		for _, field := range []invariant.Field{
			invariant.FieldSessionID,
			invariant.FieldSubscriptionGeneration,
			invariant.FieldOpeningWatermark,
			invariant.FieldObservedTaskBatchKey,
			invariant.FieldIncomingTaskBatchKey,
			invariant.FieldAttentionEventSource,
			invariant.FieldAttentionEventRevision,
		} {
			if diagnostic.Fields[field] == "" {
				t.Fatalf("panic diagnostic %q is empty: %+v", field, diagnostic)
			}
		}
	}()
	fixture.noError("PublishPendingWithOccurrence second", fixture.PublishPendingWithOccurrence(
		scope,
		testQuestionNotification("batch-2", 2),
		NewTaskQuestionBatchOccurrenceMetadata("batch-2"),
	))
}

func TestBrokerSnapshotSubscriptionClosesWithTypedDiscontinuityForConflictingSnapshotTaskKeys(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", string(invariant.ModeDiagnostic))
	fixture := newBrokerFixture(t)
	sub, err := fixture.SubscribeSessionSnapshot("session-1", []SnapshotPendingDescriptor{
		{
			Notification: testQuestionNotification("batch-1", 1),
			Occurrence:   NewTaskQuestionBatchOccurrenceMetadata("batch-1"),
		},
		{
			Notification: testQuestionNotification("batch-2", 2),
			Occurrence:   NewTaskQuestionBatchOccurrenceMetadata("batch-2"),
		},
	}, 4)
	fixture.noError("SubscribeSessionSnapshot", err)

	_, err = sub.Next(context.Background())
	var discontinuity AttentionProjectionDiscontinuity
	if !errors.As(err, &discontinuity) {
		t.Fatalf("Next error = %T %v, want AttentionProjectionDiscontinuity", err, err)
	}
	if discontinuity.Diagnostic.Fields[invariant.FieldAttentionEventSource] != string(clientui.AttentionNotificationSourceSnapshot) {
		t.Fatalf("snapshot conflict source = %q, want %q", discontinuity.Diagnostic.Fields[invariant.FieldAttentionEventSource], clientui.AttentionNotificationSourceSnapshot)
	}
	if discontinuity.Diagnostic.Fields[invariant.FieldAttentionEventRevision] != "2" {
		t.Fatalf("snapshot conflict revision = %q, want 2", discontinuity.Diagnostic.Fields[invariant.FieldAttentionEventRevision])
	}
	fixture.mu.Lock()
	subscriberCount := len(fixture.subscribers)
	fixture.mu.Unlock()
	if subscriberCount != 0 {
		t.Fatalf("broker subscribers after snapshot conflict = %d, want 0", subscriberCount)
	}
}

func TestBrokerSnapshotSubscriptionPanicsForConflictingSnapshotTaskKeysInDebug(t *testing.T) {
	t.Setenv("KENT_INVARIANT_MODE", string(invariant.ModePanic))
	fixture := newBrokerFixture(t)
	defer func() {
		recovered := recover()
		diagnostic, ok := recovered.(invariant.Diagnostic)
		if !ok {
			t.Fatalf("panic = %T %v, want invariant.Diagnostic", recovered, recovered)
		}
		if diagnostic.Scope != invariant.ScopeAttentionProjection {
			t.Fatalf("panic diagnostic scope = %q, want %q", diagnostic.Scope, invariant.ScopeAttentionProjection)
		}
		if diagnostic.Fields[invariant.FieldAttentionEventSource] != string(clientui.AttentionNotificationSourceSnapshot) {
			t.Fatalf("panic snapshot conflict source = %q, want %q", diagnostic.Fields[invariant.FieldAttentionEventSource], clientui.AttentionNotificationSourceSnapshot)
		}
		if diagnostic.Stack == "" {
			t.Fatal("panic diagnostic has no stack")
		}
	}()
	_, _ = fixture.SubscribeSessionSnapshot("session-1", []SnapshotPendingDescriptor{
		{
			Notification: testQuestionNotification("batch-1", 1),
			Occurrence:   NewTaskQuestionBatchOccurrenceMetadata("batch-1"),
		},
		{
			Notification: testQuestionNotification("batch-2", 2),
			Occurrence:   NewTaskQuestionBatchOccurrenceMetadata("batch-2"),
		},
	}, 0)
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

func (f brokerFixture) nextSnapshot(sub *SnapshotSubscription) clientui.AttentionNotificationEvent {
	f.Helper()
	event, err := sub.Next(context.Background())
	f.noError("SnapshotSubscription.Next", err)
	return event
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

func (f brokerFixture) requireNoSnapshotEvent(sub *SnapshotSubscription, failure string) {
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
