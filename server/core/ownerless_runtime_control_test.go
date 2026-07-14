package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	modelstub "core/internal/testharness/pty/blackbox"
	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"
	"core/shared/clientui"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestSecondClientLiveControlsActiveRun(t *testing.T) {
	t.Run("live steer", func(t *testing.T) {
		runSecondClientLiveControlsActiveRun(t, func(t *testing.T, appCore *Core, sessionID string) {
			steerResp, err := appCore.RuntimeLiveControlClient().LiveSteer(context.Background(), serverapi.RuntimeLiveSteerRequest{
				ClientRequestID: uuid.NewString(),
				SessionID:       sessionID,
				Text:            "steer me",
			})
			if err != nil {
				t.Fatalf("LiveSteer during active run: %v", err)
			}
			if steerResp.QueueItemID == "" {
				t.Fatal("LiveSteer during active run returned no queue item id")
			}
		})
	})
	t.Run("runtime control queue user message", func(t *testing.T) {
		runSecondClientLiveControlsActiveRun(t, func(t *testing.T, appCore *Core, sessionID string) {
			clientRequestID := uuid.NewString()
			queueResp, err := appCore.RuntimeControlClient().QueueUserMessage(context.Background(), serverapi.RuntimeQueueUserMessageRequest{
				ClientRequestID: clientRequestID,
				SessionID:       sessionID,
				OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: clientRequestID},
				Text:            "steer me",
			})
			if err != nil {
				t.Fatalf("QueueUserMessage during active run: %v", err)
			}
			if queueResp.QueueItemID == "" {
				t.Fatal("QueueUserMessage during active run returned no queue item id")
			}
		})
	})
	t.Run("runtime control submit user turn", func(t *testing.T) {
		runSecondClientLiveControlsActiveRun(t, func(t *testing.T, appCore *Core, sessionID string) {
			clientRequestID := uuid.NewString()
			submitResp, err := appCore.RuntimeControlClient().SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
				ClientRequestID: clientRequestID,
				SessionID:       sessionID,
				OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: clientRequestID},
				Text:            "steer me",
			})
			if err != nil {
				t.Fatalf("SubmitUserTurn during active run: %v", err)
			}
			if !submitResp.Steered || submitResp.QueueItemID == "" {
				t.Fatalf("SubmitUserTurn response = %+v, want steered queue item", submitResp)
			}
		})
	})
}

func runSecondClientLiveControlsActiveRun(t *testing.T, steer func(*testing.T, *Core, string)) {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	release := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	var releaseOnce sync.Once
	var requestMu sync.Mutex
	requestIndex := 0
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRun()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if modelstub.HandleInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		requestMu.Lock()
		requestIndex++
		currentRequest := requestIndex
		requestMu.Unlock()
		startOnce.Do(func() { close(started) })
		if currentRequest == 1 {
			<-release
			modelstub.WriteCompletedResponseStream(w, "first answer before steering", 1, 1)
			return
		}
		modelstub.WriteCompletedResponseStream(w, "steered final answer", 1, 1)
	}))
	defer server.Close()

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	resolved.Config.Settings.Model = "gpt-5"
	resolved.Config.Settings.OpenAIBaseURL = server.URL
	binding, err := metadata.RegisterBinding(context.Background(), resolved.Config.PersistenceRoot, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.State{
		Scope:  auth.ScopeGlobal,
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}},
	}), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })

	appCore, err := New(resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	launchClient, err := appCore.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	plan, err := launchClient.PlanSession(context.Background(), serverapi.SessionPlanRequest{
		ClientRequestID: "plan-1",
		Mode:            serverapi.SessionLaunchModeInteractive,
		ForceNewSession: true,
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	sessionID := plan.Plan.SessionID
	if sessionID == "" {
		t.Fatal("PlanSession returned empty session id")
	}

	runClient, err := appCore.RunPromptClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("RunPromptClientForProjectWorkspace: %v", err)
	}
	runDone := make(chan error, 1)
	runResult := make(chan serverapi.RunPromptResponse, 1)
	go func() {
		resp, runErr := runClient.RunPrompt(context.Background(), serverapi.RunPromptRequest{
			ClientRequestID:   uuid.NewString(),
			SelectedSessionID: sessionID,
			Prompt:            "drive the run",
		}, nil)
		runResult <- resp
		runDone <- runErr
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for run to reach model request")
	}

	steer(t, appCore, sessionID)

	waitDone := make(chan error, 1)
	waitResult := make(chan serverapi.RuntimeLiveWaitResponse, 1)
	go func() {
		resp, waitErr := appCore.RuntimeLiveControlClient().LiveWait(context.Background(), serverapi.RuntimeLiveWaitRequest{SessionID: sessionID})
		waitResult <- resp
		waitDone <- waitErr
	}()

	releaseRun()
	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("RunPrompt: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		requestMu.Lock()
		count := requestIndex
		requestMu.Unlock()
		t.Fatalf("timed out waiting for RunPrompt to finish after %d model requests", count)
	}
	resp := <-runResult
	if resp.Result != "steered final answer" {
		t.Fatalf("RunPrompt result = %q, want steered final answer", resp.Result)
	}
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			t.Fatalf("LiveWait: %v", waitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for LiveWait to finish")
	}
	waitResp := <-waitResult
	if waitResp.Result == nil || *waitResp.Result != "steered final answer" {
		t.Fatalf("LiveWait result = %v, want steered final answer", waitResp.Result)
	}
}
