package app

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestStartSessionTranscriptEventsWaitsForExplicitRehydrationAfterLoss(t *testing.T) {
	subscriber := &recordingTranscriptSubscriber{
		subs: []*scriptedTranscriptSubscription{
			{
				messages: []clientui.TranscriptMessage{ongoingHydrationMessage(1)},
				err:      serverapi.ErrStreamGap,
			},
			{messages: []clientui.TranscriptMessage{ongoingHydrationMessage(1)}},
		},
	}
	stream := startSessionTranscriptEvents(context.Background(), "session-1", subscriber.SubscribeSessionTranscript)
	defer stream.Close()

	first := nextTranscriptEvent(t, stream.Events)
	if first.Kind != ongoingTranscriptEventMessage || first.Message.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("first event = %+v, want hydration message", first)
	}
	loss := nextTranscriptEvent(t, stream.Events)
	if loss.Kind != ongoingTranscriptEventLoss || !errors.Is(loss.Err, serverapi.ErrStreamGap) {
		t.Fatalf("loss event = %+v, want stream gap loss", loss)
	}
	select {
	case event, ok := <-stream.Events:
		t.Fatalf("unexpected event before explicit rehydration request: ok=%v event=%+v", ok, event)
	case <-time.After(25 * time.Millisecond):
	}

	stream.RequestRehydration()
	second := nextTranscriptEvent(t, stream.Events)
	if second.Kind != ongoingTranscriptEventMessage || second.Message.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("second event = %+v, want reopened hydration message", second)
	}
	if got, want := subscriber.sessionIDs, []string{"session-1", "session-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("subscribe session IDs = %v, want %v", got, want)
	}
}

func TestStartSessionTranscriptEventsLocalCancelClosesChannel(t *testing.T) {
	subscriber := &recordingTranscriptSubscriber{subs: []*scriptedTranscriptSubscription{{}}}
	stream := startSessionTranscriptEvents(context.Background(), "session-1", subscriber.SubscribeSessionTranscript)

	stream.Close()

	select {
	case _, ok := <-stream.Events:
		if ok {
			t.Fatal("expected transcript events channel to close after local cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transcript events channel close")
	}
}

func TestSessionTranscriptEventStreamCloseUnblocksAndJoinsNext(t *testing.T) {
	subscription := newBlockingTranscriptSubscription()
	stream := startSessionTranscriptEvents(
		context.Background(),
		"session-1",
		func(context.Context, serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
			return subscription, nil
		},
	)
	waitForSignal(t, subscription.nextStarted, "transcript Next start")

	closed := make(chan struct{})
	go func() {
		stream.Close()
		close(closed)
	}()
	waitForSignal(t, subscription.closeCalled, "transcript subscription close")
	assertNoSignal(t, closed, "stream close before Next joined")

	close(subscription.allowNextReturn)
	waitForSignal(t, subscription.nextReturned, "transcript Next return")
	waitForSignal(t, closed, "joined transcript stream close")
	stream.Close()

	if calls := subscription.closeCalls.Load(); calls != 1 {
		t.Fatalf("subscription close calls = %d, want 1", calls)
	}
	select {
	case _, ok := <-stream.Events:
		if ok {
			t.Fatal("transcript event published after joined close")
		}
	case <-time.After(time.Second):
		t.Fatal("transcript events channel remained open after joined close")
	}
}

func TestSessionTranscriptRehydrationJoinsNextBeforeReopen(t *testing.T) {
	first := newBlockingTranscriptSubscription()
	second := &scriptedTranscriptSubscription{messages: []clientui.TranscriptMessage{ongoingHydrationMessage(1)}}
	secondSubscribe := make(chan struct{})
	var subscribeCalls atomic.Int32
	stream := startSessionTranscriptEvents(
		context.Background(),
		"session-1",
		func(context.Context, serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
			switch subscribeCalls.Add(1) {
			case 1:
				return first, nil
			case 2:
				close(secondSubscribe)
				return second, nil
			default:
				return nil, context.Canceled
			}
		},
	)
	defer stream.Close()
	waitForSignal(t, first.nextStarted, "first transcript Next start")

	stream.RequestRehydration()
	waitForSignal(t, first.closeCalled, "first transcript subscription close")
	assertNoSignal(t, secondSubscribe, "transcript reopen before first Next joined")

	close(first.allowNextReturn)
	waitForSignal(t, first.nextReturned, "first transcript Next return")
	waitForSignal(t, secondSubscribe, "second transcript subscribe")
	event := nextTranscriptEvent(t, stream.Events)
	if event.Kind != ongoingTranscriptEventMessage || event.Message.Kind != clientui.TranscriptMessageHydration {
		t.Fatalf("reopened event = %+v, want hydration message", event)
	}
}

func TestSessionTranscriptCloseJoinsBlockedRehydrationSubscription(t *testing.T) {
	first := &scriptedTranscriptSubscription{err: serverapi.ErrStreamGap}
	rehydrationStarted := make(chan struct{})
	rehydrationReturned := make(chan struct{})
	var subscribeCalls atomic.Int32
	stream := startSessionTranscriptEvents(
		context.Background(),
		"session-1",
		func(ctx context.Context, _ serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
			switch subscribeCalls.Add(1) {
			case 1:
				return first, nil
			case 2:
				close(rehydrationStarted)
				<-ctx.Done()
				close(rehydrationReturned)
				return nil, ctx.Err()
			default:
				return nil, context.Canceled
			}
		},
	)
	loss := nextTranscriptEvent(t, stream.Events)
	if loss.Kind != ongoingTranscriptEventLoss || !errors.Is(loss.Err, serverapi.ErrStreamGap) {
		t.Fatalf("transcript loss = %+v, want stream gap", loss)
	}
	stream.RequestRehydration()
	waitForSignal(t, rehydrationStarted, "blocked transcript rehydration")

	stream.Close()
	waitForSignal(t, rehydrationReturned, "joined transcript rehydration return")
	select {
	case _, ok := <-stream.Events:
		if ok {
			t.Fatal("transcript event published after closing blocked rehydration")
		}
	case <-time.After(time.Second):
		t.Fatal("transcript events remained open after closing blocked rehydration")
	}
}

func TestStartSessionTranscriptEventsReopensOnLocalRehydrationRequest(t *testing.T) {
	subscriber := &recordingTranscriptSubscriber{
		subs: []*scriptedTranscriptSubscription{
			{messages: []clientui.TranscriptMessage{ongoingHydrationMessage(1)}},
			{messages: []clientui.TranscriptMessage{ongoingHydrationMessage(1)}},
		},
	}
	stream := startSessionTranscriptEvents(context.Background(), "session-1", subscriber.SubscribeSessionTranscript)
	defer stream.Close()

	first := nextTranscriptEvent(t, stream.Events)
	if first.Kind != ongoingTranscriptEventMessage || first.Message.Sequence != 1 {
		t.Fatalf("first event = %+v, want hydration", first)
	}

	stream.RequestRehydration()

	second := nextTranscriptEvent(t, stream.Events)
	if second.Kind != ongoingTranscriptEventMessage || second.Message.Sequence != 1 {
		t.Fatalf("second event = %+v, want reopened hydration", second)
	}
	if got, want := len(subscriber.sessionIDs), 2; got != want {
		t.Fatalf("subscribe count = %d, want %d", got, want)
	}
}

type recordingTranscriptSubscriber struct {
	sessionIDs []string
	subs       []*scriptedTranscriptSubscription
}

func (s *recordingTranscriptSubscriber) SubscribeSessionTranscript(_ context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
	s.sessionIDs = append(s.sessionIDs, req.SessionID)
	if len(s.subs) == 0 {
		return nil, context.Canceled
	}
	sub := s.subs[0]
	s.subs = s.subs[1:]
	return sub, nil
}

type scriptedTranscriptSubscription struct {
	messages []clientui.TranscriptMessage
	err      error
	closed   bool
}

type blockingTranscriptSubscription struct {
	nextStarted     chan struct{}
	closeCalled     chan struct{}
	allowNextReturn chan struct{}
	nextReturned    chan struct{}
	closeOnce       sync.Once
	closeCalls      atomic.Int32
}

func newBlockingTranscriptSubscription() *blockingTranscriptSubscription {
	return &blockingTranscriptSubscription{
		nextStarted:     make(chan struct{}),
		closeCalled:     make(chan struct{}),
		allowNextReturn: make(chan struct{}),
		nextReturned:    make(chan struct{}),
	}
}

func (s *blockingTranscriptSubscription) Next(context.Context) (clientui.TranscriptMessage, error) {
	close(s.nextStarted)
	<-s.closeCalled
	<-s.allowNextReturn
	close(s.nextReturned)
	return clientui.TranscriptMessage{}, context.Canceled
}

func (s *blockingTranscriptSubscription) Close() error {
	s.closeCalls.Add(1)
	s.closeOnce.Do(func() {
		close(s.closeCalled)
	})
	return nil
}

func (s *scriptedTranscriptSubscription) Next(ctx context.Context) (clientui.TranscriptMessage, error) {
	if len(s.messages) > 0 {
		message := s.messages[0]
		s.messages = s.messages[1:]
		return message, nil
	}
	if s.err != nil {
		return clientui.TranscriptMessage{}, s.err
	}
	<-ctx.Done()
	return clientui.TranscriptMessage{}, ctx.Err()
}

func (s *scriptedTranscriptSubscription) Close() error {
	s.closed = true
	return nil
}

func nextTranscriptEvent(t *testing.T, events <-chan ongoingTranscriptEvent) ongoingTranscriptEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("transcript events channel closed")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transcript event")
		return ongoingTranscriptEvent{}
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", operation)
	}
}

func assertNoSignal(t *testing.T, signal <-chan struct{}, operation string) {
	t.Helper()
	select {
	case <-signal:
		t.Fatalf("unexpected %s", operation)
	case <-time.After(25 * time.Millisecond):
	}
}
