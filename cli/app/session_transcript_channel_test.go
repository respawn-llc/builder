package app

import (
	"context"
	"errors"
	"io"
	"reflect"
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
	reachable := nextTranscriptEvent(t, stream.Events)
	if reachable.Kind != ongoingTranscriptEventConnectionObservation || reachable.Err != nil {
		t.Fatalf("reconnect event = %+v, want reachable", reachable)
	}
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

	reachable := nextTranscriptEvent(t, stream.Events)
	if reachable.Kind != ongoingTranscriptEventConnectionObservation || reachable.Err != nil {
		t.Fatalf("reconnect event = %+v, want reachable", reachable)
	}
	second := nextTranscriptEvent(t, stream.Events)
	if second.Kind != ongoingTranscriptEventMessage || second.Message.Sequence != 1 {
		t.Fatalf("second event = %+v, want reopened hydration", second)
	}
	if got, want := len(subscriber.sessionIDs), 2; got != want {
		t.Fatalf("subscribe count = %d, want %d", got, want)
	}
}

func TestStartSessionTranscriptEventsPublishesResubscribeConnectionFailure(t *testing.T) {
	connectionFailure := errors.New("server unavailable")
	subscriber := &recordingTranscriptSubscriber{
		results: []transcriptSubscribeResult{
			{sub: &scriptedTranscriptSubscription{err: io.EOF}},
			{err: connectionFailure},
			{sub: &scriptedTranscriptSubscription{messages: []clientui.TranscriptMessage{ongoingHydrationMessage(1)}}},
		},
	}
	stream := startSessionTranscriptEvents(context.Background(), "session-1", subscriber.SubscribeSessionTranscript)
	defer stream.Stop()

	loss := nextTranscriptEvent(t, stream.Events)
	if loss.Kind != ongoingTranscriptEventLoss || !errors.Is(loss.Err, io.EOF) {
		t.Fatalf("loss event = %+v, want graceful stream EOF", loss)
	}
	stream.RequestRehydration()

	failed := nextTranscriptEvent(t, stream.Events)
	if failed.Kind != ongoingTranscriptEventConnectionObservation || !errors.Is(failed.Err, connectionFailure) {
		t.Fatalf("connection event = %+v, want failed resubscribe observation", failed)
	}
	reachable := nextTranscriptEvent(t, stream.Events)
	if reachable.Kind != ongoingTranscriptEventConnectionObservation || reachable.Err != nil {
		t.Fatalf("connection event = %+v, want reachable resubscribe observation", reachable)
	}
}

type recordingTranscriptSubscriber struct {
	sessionIDs []string
	subs       []*scriptedTranscriptSubscription
	results    []transcriptSubscribeResult
}

func (s *recordingTranscriptSubscriber) SubscribeSessionTranscript(_ context.Context, req serverapi.TranscriptSubscribeRequest) (serverapi.TranscriptSubscription, error) {
	s.sessionIDs = append(s.sessionIDs, req.SessionID)
	if len(s.results) > 0 {
		result := s.results[0]
		s.results = s.results[1:]
		return result.sub, result.err
	}
	if len(s.subs) == 0 {
		return nil, context.Canceled
	}
	sub := s.subs[0]
	s.subs = s.subs[1:]
	return sub, nil
}

type transcriptSubscribeResult struct {
	sub serverapi.TranscriptSubscription
	err error
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
