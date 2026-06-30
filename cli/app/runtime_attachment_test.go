package app

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"core/shared/client"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type runtimeAttachmentTestServer struct {
	runtime                         client.SessionRuntimeClient
	sessionEvents                   client.SessionActivityClient
	attention                       client.AttentionNotificationClient
	attentionNotificationsSupported *bool
	promptEvents                    client.PromptActivityClient
	sessionViews                    client.SessionViewClient
	runtimeControl                  client.RuntimeControlClient
}

func (s runtimeAttachmentTestServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	attentionSupported := s.attention != nil
	if s.attentionNotificationsSupported != nil {
		attentionSupported = *s.attentionNotificationsSupported
	}
	return runtimeAttachmentClients{
		PromptActivity:                  s.promptEvents,
		Attention:                       s.attention,
		AttentionNotificationsSupported: attentionSupported,
		RuntimeControls:                 s.runtimeControl,
		SessionActivity:                 s.sessionEvents,
		SessionRuntime:                  s.runtime,
		SessionViews:                    s.sessionViews,
	}
}

type noOpAttentionNotificationSubscription struct{}

func (noOpAttentionNotificationSubscription) Next(context.Context) (clientui.AttentionNotificationEvent, error) {
	return clientui.AttentionNotificationEvent{}, io.EOF
}

func (noOpAttentionNotificationSubscription) Close() error { return nil }

type noOpPromptActivitySubscription struct{}

func (noOpPromptActivitySubscription) Next(context.Context) (clientui.PendingPromptEvent, error) {
	return clientui.PendingPromptEvent{}, io.EOF
}

func (noOpPromptActivitySubscription) Close() error { return nil }

type recordingAttentionNotificationClient struct {
	subscribe        func(context.Context, serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error)
	subscribeSession func(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error)
}

func (c *recordingAttentionNotificationClient) SubscribeAttentionNotifications(ctx context.Context, req serverapi.AttentionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	if c.subscribe != nil {
		return c.subscribe(ctx, req)
	}
	return noOpAttentionNotificationSubscription{}, nil
}

func (c *recordingAttentionNotificationClient) SubscribeSessionAttentionNotifications(ctx context.Context, req serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
	if c.subscribeSession != nil {
		return c.subscribeSession(ctx, req)
	}
	return noOpAttentionNotificationSubscription{}, nil
}

func TestRuntimeAttachmentSubscribeFailureReleasesRuntime(t *testing.T) {
	for _, tc := range []struct {
		name            string
		sessionErr      error
		promptErr       error
		wantPromptStart bool
	}{
		{name: "session subscribe failure", sessionErr: errors.New("session subscribe failed")},
		{name: "prompt subscribe failure", promptErr: errors.New("prompt subscribe failed"), wantPromptStart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			releaseCount := 0
			released := make(chan context.Context, 1)
			promptStarted := false
			server := runtimeAttachmentTestServer{
				runtime: &recordingSessionRuntimeClient{
					activate: func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
						return serverapi.SessionRuntimeActivateResponse{}, nil
					},
					release: func(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
						releaseCount++
						released <- ctx
						if req.SessionID != "session-1" {
							t.Fatalf("unexpected release request: %+v", req)
						}
						return serverapi.SessionRuntimeReleaseResponse{}, nil
					},
				},
				sessionEvents: &recordingSessionActivityClient{
					subscribe: func(context.Context, serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
						if tc.sessionErr != nil {
							return nil, tc.sessionErr
						}
						return noOpSessionActivitySubscription{}, nil
					},
				},
				promptEvents: &recordingPromptActivityClient{
					subscribe: func(context.Context, serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
						promptStarted = true
						if tc.promptErr != nil {
							return nil, tc.promptErr
						}
						return nil, nil
					},
				},
				attention: &recordingAttentionNotificationClient{
					subscribeSession: func(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
						return noOpAttentionNotificationSubscription{}, nil
					},
				},
			}

			_, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1"}, io.Discard, "test")
			wantErr := tc.sessionErr
			if wantErr == nil {
				wantErr = tc.promptErr
			}
			if !errors.Is(err, wantErr) {
				t.Fatalf("prepareSharedRuntime error = %v, want %v", err, wantErr)
			}
			if promptStarted != tc.wantPromptStart {
				t.Fatalf("prompt started = %v, want %v", promptStarted, tc.wantPromptStart)
			}
			if releaseCount != 1 {
				t.Fatalf("release count = %d, want exactly 1", releaseCount)
			}
			select {
			case ctx := <-released:
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("expected bounded release context")
				}
				if remaining := time.Until(deadline); remaining <= 0 || remaining > runtimeReleaseTimeout {
					t.Fatalf("release deadline remaining = %v, want within %v", remaining, runtimeReleaseTimeout)
				}
			default:
				t.Fatal("expected release context")
			}
		})
	}
}

func TestRuntimeAttachmentAttentionSubscribeFailureFallsBackToPromptActivity(t *testing.T) {
	releaseCount := 0
	subscribeErr := errors.New("attention stream unavailable")
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return serverapi.SessionRuntimeActivateResponse{}, nil
			},
			release: func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				releaseCount++
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
		sessionEvents: &recordingSessionActivityClient{
			subscribe: func(context.Context, serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
				return noOpSessionActivitySubscription{}, nil
			},
		},
		promptEvents: &recordingPromptActivityClient{
			subscribe: func(context.Context, serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
				return noOpPromptActivitySubscription{}, nil
			},
		},
		attention: &recordingAttentionNotificationClient{
			subscribeSession: func(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
				return nil, subscribeErr
			},
		},
		sessionViews:   &countingSessionViewClient{},
		runtimeControl: &reconnectRetryRuntimeControlClient{},
	}

	plan, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-fallback"}, io.Discard, "test")
	if err != nil {
		t.Fatalf("prepareSharedRuntime: %v", err)
	}
	plan.Close()
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", releaseCount)
	}
}

func TestRuntimeAttachmentPromptActivityFallbackWhenAttentionUnsupported(t *testing.T) {
	releaseCount := 0
	attentionSubscribeCalls := 0
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return serverapi.SessionRuntimeActivateResponse{}, nil
			},
			release: func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				releaseCount++
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
		sessionEvents: &recordingSessionActivityClient{
			subscribe: func(context.Context, serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
				return noOpSessionActivitySubscription{}, nil
			},
		},
		promptEvents: &recordingPromptActivityClient{
			subscribe: func(context.Context, serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
				return noOpPromptActivitySubscription{}, nil
			},
		},
		attention: &recordingAttentionNotificationClient{
			subscribeSession: func(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
				attentionSubscribeCalls++
				return nil, errors.New("attention stream should not be used without capability")
			},
		},
		attentionNotificationsSupported: boolPointer(false),
		sessionViews:                    &countingSessionViewClient{},
		runtimeControl:                  &reconnectRetryRuntimeControlClient{},
	}

	plan, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-fallback"}, io.Discard, "test")
	if err != nil {
		t.Fatalf("prepareSharedRuntime: %v", err)
	}
	plan.Close()
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", releaseCount)
	}
	if attentionSubscribeCalls != 0 {
		t.Fatalf("attention subscribe calls = %d, want 0", attentionSubscribeCalls)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestRuntimeAttachmentCloseReleasesRuntime(t *testing.T) {
	releaseCount := 0
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return serverapi.SessionRuntimeActivateResponse{}, nil
			},
			release: func(_ context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				releaseCount++
				if req.SessionID != "session-close" {
					t.Fatalf("unexpected release request: %+v", req)
				}
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
		sessionEvents: &recordingSessionActivityClient{
			subscribe: func(context.Context, serverapi.SessionActivitySubscribeRequest) (serverapi.SessionActivitySubscription, error) {
				return noOpSessionActivitySubscription{}, nil
			},
		},
		promptEvents: &recordingPromptActivityClient{
			subscribe: func(context.Context, serverapi.PromptActivitySubscribeRequest) (serverapi.PromptActivitySubscription, error) {
				return nil, nil
			},
		},
		attention: &recordingAttentionNotificationClient{
			subscribeSession: func(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
				return noOpAttentionNotificationSubscription{}, nil
			},
		},
		sessionViews:   &countingSessionViewClient{},
		runtimeControl: &reconnectRetryRuntimeControlClient{},
	}

	plan, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-close"}, io.Discard, "test")
	if err != nil {
		t.Fatalf("prepareSharedRuntime: %v", err)
	}
	plan.Close()
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want exactly 1", releaseCount)
	}
}

func TestRuntimeAttachmentReactivationUsesActivation(t *testing.T) {
	activateCalls := 0
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				activateCalls++
				if req.SessionID != "session-recover" {
					t.Fatalf("session id = %q, want session-recover", req.SessionID)
				}
				if req.ActiveSettings.Model != "gpt-test" {
					t.Fatalf("model = %q, want gpt-test", req.ActiveSettings.Model)
				}
				return serverapi.SessionRuntimeActivateResponse{}, nil
			},
		},
	}
	reactivator, _, err := activateSharedRuntime(context.Background(), server.RuntimeAttachmentClients(), sessionLaunchPlan{
		SessionID:      "session-recover",
		ActiveSettings: config.Settings{Model: "gpt-test"},
	})
	if err != nil {
		t.Fatalf("activateSharedRuntime: %v", err)
	}
	if err := reactivator.Reactivate(context.Background()); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if activateCalls != 2 {
		t.Fatalf("activate calls = %d, want 2", activateCalls)
	}
}
