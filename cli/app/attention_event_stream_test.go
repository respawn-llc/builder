package app

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/textutil"
)

func TestNormalizeAttentionEventBuildsBoundedImmutableFacts(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  clientui.AttentionNotificationEvent
		kind attentionFactKind
	}{
		{name: "question", raw: adversarialQuestionAttentionEvent(), kind: attentionFactKindQuestion},
		{name: "approval", raw: adversarialApprovalAttentionEvent(), kind: attentionFactKindApproval},
	} {
		t.Run(test.name, func(t *testing.T) {
			fact, ok := normalizeAttentionEvent(test.raw).(*attentionFact)
			if !ok {
				t.Fatalf("normalized outcome = %T, want *attentionFact", normalizeAttentionEvent(test.raw))
			}
			if fact.kind != test.kind {
				t.Fatalf("fact kind = %d, want %d", fact.kind, test.kind)
			}
			if len(fact.notificationKey) != 32 {
				t.Fatalf("notification key size = %d, want 32", len(fact.notificationKey))
			}
			if len(fact.summary) > textutil.MarkdownSummaryLimitBytes || !utf8.ValidString(fact.summary) || !fact.summaryTruncated {
				t.Fatalf("fact summary = %d bytes valid=%t truncated=%t", len(fact.summary), utf8.ValidString(fact.summary), fact.summaryTruncated)
			}
			if fact.workflowTaskID == nil || fact.workflowTaskID.String() != "task-1" {
				t.Fatalf("workflow task id = %+v, want task-1", fact.workflowTaskID)
			}
			summary := fact.summary
			key := fact.notificationKey

			mutateRawAttentionEvent(test.raw)
			if fact.summary != summary || fact.notificationKey != key {
				t.Fatalf("fact changed after raw mutation: %+v", fact)
			}
			if fact.workflowTaskID == nil || fact.workflowTaskID.String() != "task-1" {
				t.Fatalf("workflow task id changed after raw mutation: %+v", fact.workflowTaskID)
			}
		})
	}
}

func TestAttentionEventStreamBoundsQueuedFactsBeforeBlockedSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstRaw := adversarialQuestionAttentionEvent()
	secondRaw := adversarialQuestionAttentionEvent()
	subscription := &scriptedAttentionSubscription{
		events: []clientui.AttentionNotificationEvent{
			firstRaw,
			secondRaw,
		},
		calls: make(chan int, 2),
	}
	stream := startAttentionEventStream(ctx, subscription)
	if got, want := cap(stream.events), attentionEventStreamOutputCapacity; got != want {
		t.Fatalf("stream output capacity = %d, want %d", got, want)
	}
	waitForAttentionSubscriptionCalls(t, subscription.calls, 2)
	if got := len(stream.events); got != attentionEventStreamOutputCapacity {
		t.Fatalf("queued normalized facts = %d, want bounded capacity %d", got, attentionEventStreamOutputCapacity)
	}
	first, ok := <-stream.events
	if !ok {
		t.Fatal("attention stream closed before first normalized fact")
	}
	fact, ok := first.(*attentionFact)
	if !ok {
		t.Fatalf("queued outcome = %T, want *attentionFact", first)
	}
	if retained := attentionFactRetainedBytes(fact); retained > attentionFactMaxRetainedBytes {
		t.Fatalf("queued fact retained bytes = %d, want <= %d", retained, attentionFactMaxRetainedBytes)
	}
}

func TestNormalizeNextAttentionEventReleasesRawPayloadBeforeBlockedSend(t *testing.T) {
	raw := adversarialQuestionAttentionEvent()
	subscription := &scriptedAttentionSubscription{events: []clientui.AttentionNotificationEvent{raw}}
	outcome, err := normalizeNextAttentionEvent(context.Background(), subscription)
	if err != nil {
		t.Fatalf("normalize next attention event: %v", err)
	}
	fact, ok := outcome.(*attentionFact)
	if !ok {
		t.Fatalf("normalized outcome = %T, want *attentionFact", outcome)
	}
	summary := fact.summary
	mutateRawAttentionEvent(raw)
	if fact.summary != summary {
		t.Fatalf("normalized fact retained a raw payload alias: %q", fact.summary)
	}
	if retained := attentionFactRetainedBytes(fact); retained > attentionFactMaxRetainedBytes {
		t.Fatalf("normalized fact retained bytes = %d, want <= %d", retained, attentionFactMaxRetainedBytes)
	}

	out := make(chan attentionStreamOutcome, 1)
	out <- attentionStreamControl{kind: attentionStreamControlResolved}
	sendStarted := make(chan struct{})
	sendDone := make(chan struct{})
	go func() {
		close(sendStarted)
		emitAttentionStreamOutcome(context.Background(), out, outcome)
		close(sendDone)
	}()
	<-sendStarted
	select {
	case <-sendDone:
		t.Fatal("normalized fact bypassed the bounded downstream channel")
	case <-time.After(10 * time.Millisecond):
	}
	<-out
	select {
	case <-sendDone:
	case <-time.After(time.Second):
		t.Fatal("timed out unblocking normalized fact send")
	}
}

func TestAttentionEventStreamNormalizesEveryPendingAndEmitsOnlyControlsOtherwise(t *testing.T) {
	first := adversarialQuestionAttentionEvent()
	first.Pending.Revision = 1
	second := adversarialQuestionAttentionEvent()
	second.Pending.Revision = 99
	second.Source = clientui.AttentionNotificationSourceLive
	resolvedID := first.Pending.ID
	resolvedAt := time.Now().UTC()
	subscription := &scriptedAttentionSubscription{events: []clientui.AttentionNotificationEvent{
		first,
		second,
		{
			Sequence:   3,
			Source:     clientui.AttentionNotificationSourceLive,
			Type:       clientui.AttentionNotificationEventResolved,
			ID:         &resolvedID,
			Kind:       resolvedID.Kind,
			OccurredAt: &resolvedAt,
		},
		{
			Sequence:  4,
			Source:    clientui.AttentionNotificationSourceSnapshot,
			Type:      clientui.AttentionNotificationEventSnapshotComplete,
			SessionID: "session-1",
		},
	}}
	stream := startAttentionEventStream(context.Background(), subscription)

	for index := 0; index < 2; index++ {
		outcome := nextAttentionStreamOutcome(t, stream.events)
		if _, ok := outcome.(*attentionFact); !ok {
			t.Fatalf("pending outcome %d = %T, want *attentionFact", index, outcome)
		}
	}
	if control, ok := nextAttentionStreamOutcome(t, stream.events).(attentionStreamControl); !ok || control.kind != attentionStreamControlResolved {
		t.Fatalf("resolved outcome = %+v / %t, want resolved control", control, ok)
	}
	if control, ok := nextAttentionStreamOutcome(t, stream.events).(attentionStreamControl); !ok || control.kind != attentionStreamControlSnapshotComplete {
		t.Fatalf("snapshot outcome = %+v / %t, want snapshot_complete control", control, ok)
	}
}

func TestAttentionEventStreamEmitsTypedDiscontinuitiesWithoutRawPayload(t *testing.T) {
	stream := startAttentionEventStream(context.Background(), &scriptedAttentionSubscription{
		events: []clientui.AttentionNotificationEvent{adversarialInterruptedRunAttentionEvent()},
	})
	outcome := nextAttentionStreamOutcome(t, stream.events)
	discontinuity, ok := outcome.(attentionStreamDiscontinuity)
	if !ok {
		t.Fatalf("outcome = %T, want attentionStreamDiscontinuity", outcome)
	}
	if discontinuity.reason != attentionStreamDiscontinuityUnsupportedKind {
		t.Fatalf("discontinuity reason = %d, want unsupported kind", discontinuity.reason)
	}

	loss := startAttentionEventStream(context.Background(), &scriptedAttentionSubscription{err: serverapi.ErrStreamGap})
	outcome = nextAttentionStreamOutcome(t, loss.events)
	discontinuity, ok = outcome.(attentionStreamDiscontinuity)
	if !ok || discontinuity.reason != attentionStreamDiscontinuitySubscriptionLoss {
		t.Fatalf("subscription loss = %+v / %t, want typed subscription discontinuity", discontinuity, ok)
	}

	oversizedTaskID := adversarialQuestionAttentionEvent()
	oversizedTaskID.Pending.Target.TaskID = strings.Repeat("task", textutil.MarkdownSummaryLimitBytes)
	outcome = normalizeAttentionEvent(oversizedTaskID)
	discontinuity, ok = outcome.(attentionStreamDiscontinuity)
	if !ok || discontinuity.reason != attentionStreamDiscontinuityInvalidTaskID {
		t.Fatalf("oversized task id = %+v / %t, want invalid task id discontinuity", discontinuity, ok)
	}
}

func TestAttentionEventStreamClosesSubscriptionOnCancellationAndLoss(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	subscription := &scriptedAttentionSubscription{}
	stream := startAttentionEventStream(ctx, subscription)
	cancel()
	waitForAttentionStreamClose(t, stream.events)
	if !subscription.isClosed() {
		t.Fatal("attention subscription remained open after stream cancellation")
	}

	lostSubscription := &scriptedAttentionSubscription{err: serverapi.ErrStreamGap}
	lostStream := startAttentionEventStream(context.Background(), lostSubscription)
	_ = nextAttentionStreamOutcome(t, lostStream.events)
	waitForAttentionStreamClose(t, lostStream.events)
	if !lostSubscription.isClosed() {
		t.Fatal("attention subscription remained open after stream loss")
	}

	closeFailureSubscription := &scriptedAttentionSubscription{
		err:      serverapi.ErrStreamGap,
		closeErr: context.DeadlineExceeded,
	}
	closeFailureStream := startAttentionEventStream(context.Background(), closeFailureSubscription)
	if discontinuity, ok := nextAttentionStreamOutcome(t, closeFailureStream.events).(attentionStreamDiscontinuity); !ok || discontinuity.reason != attentionStreamDiscontinuitySubscriptionLoss {
		t.Fatalf("subscription loss outcome = %+v / %t, want loss discontinuity", discontinuity, ok)
	}
	if discontinuity, ok := nextAttentionStreamOutcome(t, closeFailureStream.events).(attentionStreamDiscontinuity); !ok || discontinuity.reason != attentionStreamDiscontinuitySubscriptionCloseFailure {
		t.Fatalf("subscription close outcome = %+v / %t, want close failure discontinuity", discontinuity, ok)
	}
	waitForAttentionStreamClose(t, closeFailureStream.events)
}

type scriptedAttentionSubscription struct {
	mu       sync.Mutex
	events   []clientui.AttentionNotificationEvent
	err      error
	calls    chan int
	closed   bool
	closeErr error
}

func (s *scriptedAttentionSubscription) Next(ctx context.Context) (clientui.AttentionNotificationEvent, error) {
	s.mu.Lock()
	if len(s.events) > 0 {
		event := s.events[0]
		s.events = s.events[1:]
		calls := len(s.events)
		s.mu.Unlock()
		if s.calls != nil {
			s.calls <- calls
		}
		return event, nil
	}
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return clientui.AttentionNotificationEvent{}, err
	}
	<-ctx.Done()
	return clientui.AttentionNotificationEvent{}, ctx.Err()
}

func (s *scriptedAttentionSubscription) Close() error {
	s.mu.Lock()
	s.closed = true
	err := s.closeErr
	s.mu.Unlock()
	return err
}

func (s *scriptedAttentionSubscription) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func nextAttentionStreamOutcome(t *testing.T, events <-chan attentionStreamOutcome) attentionStreamOutcome {
	t.Helper()
	select {
	case outcome, ok := <-events:
		if !ok {
			t.Fatal("attention stream closed")
		}
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for attention stream outcome")
		return nil
	}
}

func waitForAttentionSubscriptionCalls(t *testing.T, calls <-chan int, want int) {
	t.Helper()
	for count := 0; count < want; count++ {
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for attention subscription call %d", count+1)
		}
	}
}

func waitForAttentionStreamClose(t *testing.T, events <-chan attentionStreamOutcome) {
	t.Helper()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("attention stream remained open")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for attention stream close")
	}
}

func adversarialQuestionAttentionEvent() clientui.AttentionNotificationEvent {
	large := strings.Repeat("界", 1<<20)
	return clientui.AttentionNotificationEvent{
		Sequence: 1,
		Source:   clientui.AttentionNotificationSourceSnapshot,
		Type:     clientui.AttentionNotificationEventPending,
		Pending: &clientui.AttentionNotification{
			ID:         clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindQuestion, UUID: large},
			Kind:       clientui.AttentionNotificationKindQuestion,
			OccurredAt: time.Now().UTC(),
			Revision:   7,
			Question: &clientui.AttentionNotificationQuestionState{
				PreparedAskIDs:          []string{large},
				MaterializedAskIDs:      []string{large},
				CurrentUnresolvedAskIDs: []string{large},
				Preview:                 large,
				DisplayCount:            1,
				MaterializedCount:       1,
			},
			Target: clientui.AttentionNotificationTarget{
				Kind:        clientui.AttentionNotificationTargetWorkflowTask,
				TaskID:      "task-1",
				TaskShortID: large,
				TaskTitle:   large,
				SessionID:   large,
				Focus: &clientui.AttentionNotificationTaskDetailFocus{
					Kind:   clientui.AttentionNotificationFocusQuestion,
					AskIDs: []string{large},
				},
			},
		},
	}
}

func adversarialApprovalAttentionEvent() clientui.AttentionNotificationEvent {
	large := strings.Repeat("界", 1<<20)
	return clientui.AttentionNotificationEvent{
		Sequence: 1,
		Source:   clientui.AttentionNotificationSourceLive,
		Type:     clientui.AttentionNotificationEventPending,
		Pending: &clientui.AttentionNotification{
			ID:         clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindApproval, UUID: large},
			Kind:       clientui.AttentionNotificationKindApproval,
			OccurredAt: time.Now().UTC(),
			Revision:   3,
			Approval: &clientui.AttentionNotificationApprovalState{
				TaskTransitionID: large,
				Message:          large,
			},
			Target: clientui.AttentionNotificationTarget{
				Kind:        clientui.AttentionNotificationTargetWorkflowTask,
				TaskID:      "task-1",
				TaskShortID: large,
				TaskTitle:   large,
				SessionID:   large,
				Focus: &clientui.AttentionNotificationTaskDetailFocus{
					Kind:             clientui.AttentionNotificationFocusApproval,
					TaskTransitionID: large,
				},
			},
		},
	}
}

func adversarialInterruptedRunAttentionEvent() clientui.AttentionNotificationEvent {
	large := strings.Repeat("x", 1<<20)
	return clientui.AttentionNotificationEvent{
		Sequence: 1,
		Source:   clientui.AttentionNotificationSourceLive,
		Type:     clientui.AttentionNotificationEventPending,
		Pending: &clientui.AttentionNotification{
			ID:         clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindInterruptedRun, UUID: large},
			Kind:       clientui.AttentionNotificationKindInterruptedRun,
			OccurredAt: time.Now().UTC(),
			Revision:   1,
			InterruptedRun: &clientui.AttentionNotificationInterruptedRunState{
				RunID:      large,
				Message:    large,
				Reason:     large,
				DetailJSON: large,
			},
			Target: clientui.AttentionNotificationTarget{
				Kind:      clientui.AttentionNotificationTargetWorkflowTask,
				TaskID:    "task-1",
				RunID:     large,
				TaskTitle: large,
				Focus: &clientui.AttentionNotificationTaskDetailFocus{
					Kind:  clientui.AttentionNotificationFocusInterruptedRun,
					RunID: large,
				},
			},
		},
	}
}

func mutateRawAttentionEvent(event clientui.AttentionNotificationEvent) {
	if event.Pending == nil {
		return
	}
	event.Pending.ID.UUID = "changed"
	event.Pending.Target.TaskTitle = "changed"
	if event.Pending.Target.Focus != nil {
		event.Pending.Target.Focus.AskIDs = []string{"changed"}
		event.Pending.Target.Focus.TaskTransitionID = "changed"
	}
	if event.Pending.Question != nil {
		event.Pending.Question.Preview = "changed"
		event.Pending.Question.PreparedAskIDs = []string{"changed"}
	}
	if event.Pending.Approval != nil {
		event.Pending.Approval.Message = "changed"
		event.Pending.Approval.TaskTransitionID = "changed"
	}
}

func attentionFactRetainedBytes(fact *attentionFact) int {
	if fact == nil {
		return 0
	}
	retained := len(fact.notificationKey) + len(fact.summary)
	if fact.workflowTaskID != nil {
		retained += len(fact.workflowTaskID.String())
	}
	return retained
}
