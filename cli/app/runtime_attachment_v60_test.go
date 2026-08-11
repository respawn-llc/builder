package app

import (
	"context"
	"io"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty/appfixture"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/lifecyclecontract"
	"core/shared/serverapi"
)

type runtimeAttachmentTestServer struct {
	runtime           apicontract.SessionRuntimeService
	sessionTranscript apicontract.SessionTranscriptService
	sessionViews      apicontract.SessionViewService
	runtimeControl    apicontract.RuntimeControlService
}

type recordingSessionRuntimeClient struct {
	activate func(context.Context, serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error)
	release  func(context.Context, serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error)
}

func sessionRuntimeActivateResponse(sessionID string, generation uint64) serverapi.SessionRuntimeActivateResponse {
	return serverapi.SessionRuntimeActivateResponse{Attachment: serverapi.SessionRuntimeAttachment{
		SessionID:  sessionID,
		Generation: generation,
	}}
}

func (c *recordingSessionRuntimeClient) ActivateSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeActivateRequest) (serverapi.SessionRuntimeActivateResponse, error) {
	if c.activate != nil {
		return c.activate(ctx, req)
	}
	return sessionRuntimeActivateResponse(req.SessionID, 1), nil
}

func (c *recordingSessionRuntimeClient) ReleaseSessionRuntime(ctx context.Context, req serverapi.SessionRuntimeReleaseRequest) (serverapi.SessionRuntimeReleaseResponse, error) {
	if c.release != nil {
		return c.release(ctx, req)
	}
	return serverapi.SessionRuntimeReleaseResponse{}, nil
}

func (s runtimeAttachmentTestServer) RuntimeAttachmentClients() runtimeAttachmentClients {
	return runtimeAttachmentClients{
		RuntimeControls:   s.runtimeControl,
		SessionRuntime:    s.runtime,
		SessionTranscript: s.sessionTranscript,
		SessionViews:      s.sessionViews,
	}
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

func TestRuntimeAttachmentUsesTranscriptBellHook(t *testing.T) {
	releaseCount := 0
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
		sessionTranscript: &recordingTranscriptSubscriber{subs: []*scriptedTranscriptSubscription{{}}},
		sessionViews:      sessionViews,
		runtimeControl:    &reconnectRetryRuntimeControlClient{},
	}

	plan, err := prepareSharedRuntime(context.Background(), server, sessionLaunchPlan{SessionID: "session-1"}, io.Discard, "test")
	if err != nil {
		t.Fatalf("prepareSharedRuntime: %v", err)
	}
	if plan.Wiring.eventDispatcher == nil || plan.Wiring.eventDispatcher.transcriptEvents == nil {
		t.Fatal("runtime wiring omitted the transcript event dispatcher")
	}
	promptHook, promptOK := plan.Wiring.promptAttention.(*bellHooks)
	turnHook, turnOK := plan.Wiring.turnQueueHook.(*bellHooks)
	if !promptOK || !turnOK || promptHook != turnHook {
		t.Fatal("runtime wiring did not make the bell hook authoritative for prompt activation")
	}
	plan.Close()
	if releaseCount != 1 {
		t.Fatalf("release count = %d, want 1", releaseCount)
	}
	if got := sessionViews.mainViewCount.Load(); got != 0 {
		t.Fatalf("startup main-view reads = %d, want 0 before feed hydration", got)
	}
}

func TestRuntimeAttachmentKeepsPromptActivationIndependentFromLifecycleHooks(t *testing.T) {
	recordPath := filepath.Join(t.TempDir(), "lifecycle.jsonl")
	command, err := lifecycleHookProductRecorderCommand(
		recordPath,
		appfixture.LifecycleHookBehaviorSuccess,
		nil,
	)
	if err != nil {
		t.Fatalf("lifecycle recorder command: %v", err)
	}
	server := runtimeAttachmentTestServer{
		sessionTranscript: &recordingTranscriptSubscriber{subs: []*scriptedTranscriptSubscription{{}}},
		sessionViews:      &countingSessionViewClient{},
		runtimeControl:    &reconnectRetryRuntimeControlClient{},
	}
	sessionID := ongoingTestSessionID().String()
	wiring, stop, err := prepareSharedRuntimeWiring(t.Context(), server.RuntimeAttachmentClients(), sessionLaunchPlan{
		SessionID:                  sessionID,
		ClientLifecycleCommand:     command,
		ClientLifecycleOpeningKind: lifecyclecontract.OpeningKindResumed,
	}, nil)
	if err != nil {
		t.Fatalf("prepareSharedRuntimeWiring: %v", err)
	}
	t.Cleanup(stop)

	promptHook, promptOK := wiring.promptAttention.(*bellHooks)
	turnHooks, turnOK := wiring.turnQueueHook.(*turnQueueHooks)
	if !promptOK || !turnOK || turnHooks.notifications != promptHook {
		t.Fatal("lifecycle hooks changed native prompt-activation ownership")
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
