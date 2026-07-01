package runtimeattach

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type fakeSessionActivitySubscription struct {
	closed bool
}

func (s *fakeSessionActivitySubscription) Next(context.Context) (clientui.Event, error) {
	return clientui.Event{}, io.EOF
}

func (s *fakeSessionActivitySubscription) Close() error {
	s.closed = true
	return nil
}

type fakePromptActivitySubscription struct{}

func (s fakePromptActivitySubscription) Next(context.Context) (clientui.PendingPromptEvent, error) {
	return clientui.PendingPromptEvent{}, io.EOF
}

func (s fakePromptActivitySubscription) Close() error { return nil }

type fakeSessionActivityService struct {
	subscribeRequests []serverapi.SessionActivitySubscribeRequest
	sub               serverapi.SessionActivitySubscription
	err               error
}

func (s *fakeSessionActivityService) SubscribeSessionActivity(_ context.Context, req serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
	s.subscribeRequests = append(s.subscribeRequests, req)
	return s.sub, s.err
}

type fakePromptActivityService struct {
	subscribeRequests []serverapi.PromptActivitySubscribeRequest
	sub               serverapi.PromptActivitySubscription
	err               error
}

func (s *fakePromptActivityService) SubscribePromptActivity(_ context.Context, req serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
	s.subscribeRequests = append(s.subscribeRequests, req)
	return s.sub, s.err
}

func TestSubscribeActivitiesReturnsBothSubscriptions(t *testing.T) {
	sessionSub := &fakeSessionActivitySubscription{}
	promptSub := fakePromptActivitySubscription{}
	sessionActivity := &fakeSessionActivityService{sub: sessionSub}
	promptActivity := &fakePromptActivityService{sub: promptSub}
	activities, err := SubscribeActivities(context.Background(), ActivityRequest{
		SessionID:       "session-1",
		SessionActivity: sessionActivity,
		PromptActivity:  promptActivity,
	})
	if err != nil {
		t.Fatalf("SubscribeActivities: %v", err)
	}
	if activities.Session != sessionSub {
		t.Fatal("expected session subscription")
	}
	if activities.Prompt == nil {
		t.Fatal("expected prompt subscription")
	}
	if sessionActivity.subscribeRequests[0].SessionID != "session-1" || promptActivity.subscribeRequests[0].SessionID != "session-1" {
		t.Fatalf("unexpected subscribe requests: %#v %#v", sessionActivity.subscribeRequests, promptActivity.subscribeRequests)
	}
}

func TestSubscribeActivitiesReleasesOnSessionSubscribeFailure(t *testing.T) {
	runtime := &fakeRuntimeService{}
	subscribeErr := errors.New("session subscribe failed")
	_, err := SubscribeActivities(context.Background(), ActivityRequest{
		SessionID:       "session-1",
		Runtime:         runtime,
		SessionActivity: &fakeSessionActivityService{err: subscribeErr},
		PromptActivity:  &fakePromptActivityService{sub: fakePromptActivitySubscription{}},
	})
	if !errors.Is(err, subscribeErr) {
		t.Fatalf("error = %v, want %v", err, subscribeErr)
	}
	if len(runtime.releaseRequests) != 1 {
		t.Fatalf("release requests = %d, want 1", len(runtime.releaseRequests))
	}
}

func TestSubscribeActivitiesClosesSessionSubscriptionAndReleasesOnPromptFailure(t *testing.T) {
	sessionSub := &fakeSessionActivitySubscription{}
	runtime := &fakeRuntimeService{}
	subscribeErr := errors.New("prompt subscribe failed")
	_, err := SubscribeActivities(context.Background(), ActivityRequest{
		SessionID:       "session-1",
		Runtime:         runtime,
		SessionActivity: &fakeSessionActivityService{sub: sessionSub},
		PromptActivity:  &fakePromptActivityService{err: subscribeErr},
	})
	if !errors.Is(err, subscribeErr) {
		t.Fatalf("error = %v, want %v", err, subscribeErr)
	}
	if !sessionSub.closed {
		t.Fatal("expected session subscription close")
	}
	if len(runtime.releaseRequests) != 1 {
		t.Fatalf("release requests = %d, want 1", len(runtime.releaseRequests))
	}
}
