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

type fakeAttentionNotificationSubscription struct {
	closed bool
}

func (s *fakeAttentionNotificationSubscription) Next(context.Context) (clientui.AttentionNotificationEvent, error) {
	return clientui.AttentionNotificationEvent{}, io.EOF
}

func (s *fakeAttentionNotificationSubscription) Close() error {
	s.closed = true
	return nil
}

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

type fakeAttentionNotificationService struct {
	subscribeRequests []serverapi.AttentionSessionNotificationSubscribeRequest
	sub               serverapi.AttentionNotificationSubscription
	err               error
}

func (s *fakeAttentionNotificationService) SubscribeAttentionNotifications(context.Context, serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	return nil, errors.New("desktop attention route is not used by runtime attach")
}

func (s *fakeAttentionNotificationService) SubscribeSessionAttentionNotifications(_ context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	s.subscribeRequests = append(s.subscribeRequests, req)
	return s.sub, s.err
}

func (s *fakePromptActivityService) SubscribePromptActivity(_ context.Context, req serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
	s.subscribeRequests = append(s.subscribeRequests, req)
	return s.sub, s.err
}

func TestSubscribeActivitiesReturnsBothSubscriptions(t *testing.T) {
	sessionSub := &fakeSessionActivitySubscription{}
	promptSub := fakePromptActivitySubscription{}
	attentionSub := &fakeAttentionNotificationSubscription{}
	sessionActivity := &fakeSessionActivityService{sub: sessionSub}
	promptActivity := &fakePromptActivityService{sub: promptSub}
	attention := &fakeAttentionNotificationService{sub: attentionSub}
	activities, err := SubscribeActivities(context.Background(), ActivityRequest{
		SessionID:                       "session-1",
		SessionActivity:                 sessionActivity,
		PromptActivity:                  promptActivity,
		Attention:                       attention,
		AttentionNotificationsSupported: true,
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
	if activities.Attention != attentionSub {
		t.Fatal("expected attention notification subscription")
	}
	if sessionActivity.subscribeRequests[0].SessionID != "session-1" || promptActivity.subscribeRequests[0].SessionID != "session-1" {
		t.Fatalf("unexpected subscribe requests: %#v %#v", sessionActivity.subscribeRequests, promptActivity.subscribeRequests)
	}
	if len(attention.subscribeRequests) != 1 || attention.subscribeRequests[0].SessionID != "session-1" || !attention.subscribeRequests[0].IncludePendingPromptSnapshot {
		t.Fatalf("unexpected attention subscribe requests: %#v", attention.subscribeRequests)
	}
}

func TestSubscribeActivitiesReleasesOnSessionSubscribeFailure(t *testing.T) {
	runtime := &fakeRuntimeService{}
	subscribeErr := errors.New("session subscribe failed")
	_, err := SubscribeActivities(context.Background(), ActivityRequest{
		SessionID:                       "session-1",
		Runtime:                         runtime,
		SessionActivity:                 &fakeSessionActivityService{err: subscribeErr},
		PromptActivity:                  &fakePromptActivityService{sub: fakePromptActivitySubscription{}},
		Attention:                       &fakeAttentionNotificationService{sub: &fakeAttentionNotificationSubscription{}},
		AttentionNotificationsSupported: true,
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
		SessionID:                       "session-1",
		Runtime:                         runtime,
		SessionActivity:                 &fakeSessionActivityService{sub: sessionSub},
		PromptActivity:                  &fakePromptActivityService{err: subscribeErr},
		Attention:                       &fakeAttentionNotificationService{sub: &fakeAttentionNotificationSubscription{}},
		AttentionNotificationsSupported: true,
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

func TestSubscribeActivitiesFallsBackToPromptActivityOnSupportedAttentionSubscribeFailure(t *testing.T) {
	sessionSub := &fakeSessionActivitySubscription{}
	promptSub := &fakeAttentionPromptSubscription{}
	subscribeErr := errors.New("attention subscribe failed")
	activities, err := SubscribeActivities(context.Background(), ActivityRequest{
		SessionID:                       "session-1",
		Runtime:                         &fakeRuntimeService{},
		SessionActivity:                 &fakeSessionActivityService{sub: sessionSub},
		PromptActivity:                  &fakePromptActivityService{sub: promptSub},
		Attention:                       &fakeAttentionNotificationService{err: subscribeErr},
		AttentionNotificationsSupported: true,
	})
	if err != nil {
		t.Fatalf("SubscribeActivities: %v", err)
	}
	if activities.Session != sessionSub || activities.Prompt != promptSub || activities.Attention != nil {
		t.Fatalf("activities = %+v, want session and prompt fallback only", activities)
	}
	if sessionSub.closed || promptSub.closed {
		t.Fatalf("subscriptions should stay open after attention fallback; session=%v prompt=%v", sessionSub.closed, promptSub.closed)
	}
}

func TestSubscribeActivitiesFallsBackToPromptActivityWhenAttentionUnsupported(t *testing.T) {
	sessionSub := &fakeSessionActivitySubscription{}
	promptSub := &fakeAttentionPromptSubscription{}
	attention := &fakeAttentionNotificationService{err: errors.New("attention route should not be used")}
	activities, err := SubscribeActivities(context.Background(), ActivityRequest{
		SessionID:       "session-1",
		SessionActivity: &fakeSessionActivityService{sub: sessionSub},
		PromptActivity:  &fakePromptActivityService{sub: promptSub},
		Attention:       attention,
	})
	if err != nil {
		t.Fatalf("SubscribeActivities: %v", err)
	}
	if sessionSub.closed || promptSub.closed {
		t.Fatalf("subscriptions should remain open for prompt-activity fallback; session=%v prompt=%v", sessionSub.closed, promptSub.closed)
	}
	if len(attention.subscribeRequests) != 0 {
		t.Fatalf("unsupported attention should not subscribe: %#v", attention.subscribeRequests)
	}
	if activities.Session != sessionSub || activities.Prompt != promptSub || activities.Attention != nil {
		t.Fatalf("unexpected fallback activities: %+v", activities)
	}
}

func TestSubscribeActivitiesAllowsMissingAttentionService(t *testing.T) {
	sessionSub := &fakeSessionActivitySubscription{}
	promptSub := fakePromptActivitySubscription{}
	activities, err := SubscribeActivities(context.Background(), ActivityRequest{
		SessionID:       "session-1",
		SessionActivity: &fakeSessionActivityService{sub: sessionSub},
		PromptActivity:  &fakePromptActivityService{sub: promptSub},
	})
	if err != nil {
		t.Fatalf("SubscribeActivities: %v", err)
	}
	if activities.Session != sessionSub || activities.Prompt == nil || activities.Attention != nil {
		t.Fatalf("unexpected activities: %+v", activities)
	}
}

type fakeAttentionPromptSubscription struct {
	closed bool
}

func (s *fakeAttentionPromptSubscription) Next(context.Context) (clientui.PendingPromptEvent, error) {
	return clientui.PendingPromptEvent{}, io.EOF
}

func (s *fakeAttentionPromptSubscription) Close() error {
	s.closed = true
	return nil
}
