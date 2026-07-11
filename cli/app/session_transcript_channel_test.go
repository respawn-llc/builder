package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestStartSessionTranscriptEventsWaitsForExplicitRehydrationAfterLoss(t *testing.T) {
	originalDelay := sessionActivityResubscribeDelay
	sessionActivityResubscribeDelay = time.Millisecond
	defer func() { sessionActivityResubscribeDelay = originalDelay }()

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
	defer stream.Stop()

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

	stream.Stop()

	select {
	case _, ok := <-stream.Events:
		if ok {
			t.Fatal("expected transcript events channel to close after local cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for transcript events channel close")
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
	defer stream.Stop()

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
