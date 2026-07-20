package app

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

type runtimeAttachmentTestServer struct {
	runtime           apicontract.SessionRuntimeService
	attention         apicontract.AttentionNotificationService
	sessionTranscript apicontract.SessionTranscriptService
	sessionViews      apicontract.SessionViewService
	runtimeControl    apicontract.RuntimeControlService
}

func (s runtimeAttachmentTestServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	return runtimeAttachmentClients{
		RuntimeControls:   s.runtimeControl,
		SessionRuntime:    s.runtime,
		SessionTranscript: s.sessionTranscript,
		SessionViews:      s.sessionViews,
	}
}

type noOpAttentionNotificationSubscription struct{}

func (noOpAttentionNotificationSubscription) Next(context.Context) (clientui.AttentionNotificationEvent, error) {
	return clientui.AttentionNotificationEvent{}, io.EOF
}

func (noOpAttentionNotificationSubscription) Close() error { return nil }

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

func TestRuntimeAttachmentRequiresTranscriptServiceAndReleasesRuntime(t *testing.T) {
	releaseCount := 0
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return sessionRuntimeActivateResponse(req.SessionID, 1), nil
			},
			release: func(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				releaseCount++
				if req.Attachment.SessionID != "session-1" || req.Attachment.Generation != 1 {
					t.Fatalf("unexpected release request: %+v", req)
				}
				deadline, ok := ctx.Deadline()
				if !ok {
					t.Fatal("expected bounded release context")
				}
				if remaining := time.Until(deadline); remaining <= 0 || remaining > runtimeReleaseTimeout {
					t.Fatalf("release deadline remaining = %v, want within %v", remaining, runtimeReleaseTimeout)
				}
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
	}

	_, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1"}, io.Discard, "test")
	if err == nil {
		t.Fatal("expected missing transcript service error")
	}
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", releaseCount)
	}
}

func TestRuntimeAttachmentUnsupportedAttentionUsesTranscriptAndClosesLease(t *testing.T) {
	releaseCount := 0
	attentionCalls := 0
	sessionViews := &countingSessionViewClient{}
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return sessionRuntimeActivateResponse(req.SessionID, 1), nil
			},
			release: func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				releaseCount++
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
		attention: &recordingAttentionNotificationClient{
			subscribeSession: func(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
				attentionCalls++
				return nil, errors.New("unexpected attention subscription")
			},
		},
		sessionTranscript: &recordingTranscriptSubscriber{subs: []*scriptedTranscriptSubscription{{}}},
		sessionViews:      sessionViews,
		runtimeControl:    &reconnectRetryRuntimeControlClient{},
	}

	plan, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1"}, io.Discard, "test")
	if err != nil {
		t.Fatalf("prepareSharedRuntime: %v", err)
	}
	plan.Close()
	if attentionCalls != 0 {
		t.Fatalf("attention calls = %d, want 0", attentionCalls)
	}
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", releaseCount)
	}
	if got := sessionViews.mainViewCount.Load(); got != 0 {
		t.Fatalf("startup main-view reads = %d, want 0 before feed hydration", got)
	}
}

func TestRuntimeAttachmentSupportedAttentionStillUsesTranscriptWithoutSubscription(t *testing.T) {
	releaseCount := 0
	attentionCalls := 0
	sessionViews := &countingSessionViewClient{}
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				return sessionRuntimeActivateResponse(req.SessionID, 1), nil
			},
			release: func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				releaseCount++
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
		attention: &recordingAttentionNotificationClient{
			subscribeSession: func(context.Context, serverapi.AttentionSessionNotificationSubscribeRequest) (serverapi.AttentionNotificationSubscription, error) {
				attentionCalls++
				return noOpAttentionNotificationSubscription{}, nil
			},
		},
		sessionTranscript: &recordingTranscriptSubscriber{subs: []*scriptedTranscriptSubscription{{}}},
		sessionViews:      sessionViews,
		runtimeControl:    &reconnectRetryRuntimeControlClient{},
	}

	plan, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1"}, io.Discard, "test")
	if err != nil {
		t.Fatalf("prepareSharedRuntime: %v", err)
	}
	if plan.Wiring.promptAttention == nil || plan.Wiring.promptAttention != plan.Wiring.turnQueueHook {
		t.Fatal("runtime wiring did not make the existing bell hook authoritative for prompt activation")
	}
	if attentionCalls != 0 {
		t.Fatalf("attention subscription calls = %d, want 0", attentionCalls)
	}
	if err := plan.Close(); err != nil {
		t.Fatalf("close runtime plan: %v", err)
	}
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", releaseCount)
	}
}

func TestRuntimeAttachmentReactivationUsesActivation(t *testing.T) {
	activateCalls := 0
	var released serverapi.SessionRuntimeAttachment
	server := runtimeAttachmentTestServer{
		runtime: &recordingSessionRuntimeClient{
			activate: func(_ context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
				activateCalls++
				if req.SessionID != "session-recover" || req.ActiveSettings.Model != "gpt-test" {
					t.Fatalf("unexpected activation request: %+v", req)
				}
				return sessionRuntimeActivateResponse(req.SessionID, uint64(activateCalls)), nil
			},
			release: func(_ context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
				released = req.Attachment
				return serverapi.SessionRuntimeReleaseResponse{}, nil
			},
		},
	}
	reactivator, lease, err := activateSharedRuntime(context.Background(), server.RuntimeAttachmentClients(), sessionLaunchPlan{
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
	if err := lease.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.SessionID != "session-recover" || released.Generation != 2 {
		t.Fatalf("released attachment = %+v, want reactivated generation 2", released)
	}
}
