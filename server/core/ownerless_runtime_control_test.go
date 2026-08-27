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
	"core/shared/config"
	"core/shared/protoapi"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

func TestSecondClientLiveControlsActiveRun(t *testing.T) {
	t.Run("live steer", func(t *testing.T) {
		runSecondClientLiveControlsActiveRun(t, "steered final answer", func(t *testing.T, appCore *Core, sessionID string) func() {
			type result struct {
				response serverapi.RuntimeLiveSteerResponse
				err      error
			}
			done := make(chan result, 1)
			go func() {
				response, err := appCore.RuntimeLiveControlClient().LiveSteer(context.Background(), serverapi.RuntimeLiveSteerRequest{
					SessionID: sessionID,
					Text:      "steer me",
				})
				done <- result{response: response, err: err}
			}()
			return func() {
				select {
				case result := <-done:
					if result.err != nil {
						t.Fatalf("LiveSteer during active run: %v", result.err)
					}
					if result.response.QueueItemID == "" {
						t.Fatal("LiveSteer during active run returned no queue item id")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for queued live Steering")
				}
			}
		})
	})
	t.Run("runtime control submit user turn", func(t *testing.T) {
		runSecondClientLiveControlsActiveRun(t, "steered final answer", func(t *testing.T, appCore *Core, sessionID string) func() {
			type result struct {
				response serverapi.RuntimeSubmitUserTurnResponse
				err      error
			}
			done := make(chan result, 1)
			go func() {
				response, err := appCore.RuntimeControlClient().SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
					SessionID: sessionID,
					Input:     runtimeinput.Text("steer me"),
				})
				done <- result{response: response, err: err}
			}()
			return func() {
				select {
				case result := <-done:
					if result.err != nil {
						t.Fatalf("SubmitUserTurn during active run: %v", result.err)
					}
					if !result.response.Steered || result.response.QueueItemID == "" {
						t.Fatalf("SubmitUserTurn response = %+v, want steered queue item", result.response)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for queued Runtime Control submission")
				}
			}
		})
	})
}

func runSecondClientLiveControlsActiveRun(
	t *testing.T,
	wantCurrentResult string,
	steer func(*testing.T, *Core, string) func(),
) {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	release := make(chan struct{})
	started := make(chan struct{})
	secondStarted := make(chan struct{})
	var startOnce sync.Once
	var secondOnce sync.Once
	var releaseOnce sync.Once
	var requestMu sync.Mutex
	requestIndex := 0
	releaseRun := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRun()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		requestMu.Lock()
		requestIndex++
		currentRequest := requestIndex
		requestMu.Unlock()
		startOnce.Do(func() { close(started) })
		if currentRequest == 2 {
			secondOnce.Do(func() { close(secondStarted) })
		}
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

	appCore, err := NewWithContextOptions(t.Context(), resolved.Config, authSupport, runtimeSupport, Options{
		WorkspaceConfigLoadOptions: config.LoadOptions{
			Model:         "gpt-5",
			OpenAIBaseURL: server.URL,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	launchClient, err := appCore.SessionLaunchClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	intent, err := protoapi.SessionLaunchIntentToProto(
		serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	)
	if err != nil {
		t.Fatalf("convert Session launch intent: %v", err)
	}
	plan, err := launchClient.PlanSession(context.Background(), &sessionlaunchpb.SessionPlanRequest{
		Mode:   sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE,
		Intent: intent,
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	sessionID := plan.Plan.SessionId
	if sessionID == "" {
		t.Fatal("PlanSession returned empty session id")
	}
	typedSessionID, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}

	runClient, err := appCore.RunPromptClientForProjectWorkspace(context.Background(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("RunPromptClientForProjectWorkspace: %v", err)
	}
	runDone := make(chan error, 1)
	runResult := make(chan serverapi.RunPromptResponse, 1)
	go func() {
		resp, runErr := runClient.RunPrompt(context.Background(), serverapi.RunPromptRequest{
			Intent: serverapi.OpenExistingSessionLaunchIntent(typedSessionID),
			Prompt: "drive the run",
		}, nil)
		runResult <- resp
		runDone <- runErr
	}()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for run to reach model request")
	}

	finishSteer := steer(t, appCore, sessionID)

	waitDone := make(chan error, 1)
	waitResult := make(chan serverapi.RuntimeLiveWaitResponse, 1)
	go func() {
		resp, waitErr := appCore.RuntimeLiveControlClient().LiveWait(context.Background(), serverapi.RuntimeLiveWaitRequest{SessionID: sessionID})
		waitResult <- resp
		waitDone <- waitErr
	}()

	releaseRun()
	if finishSteer != nil {
		finishSteer()
	}
	select {
	case runErr := <-runDone:
		if runErr != nil {
			t.Fatalf("RunPrompt: %v", runErr)
		}
	case <-time.After(5 * time.Second):
		requestMu.Lock()
		count := requestIndex
		requestMu.Unlock()
		t.Fatalf("timed out waiting for RunPrompt to finish after %d model requests", count)
	}
	resp := <-runResult
	if resp.Result != wantCurrentResult {
		t.Fatalf("RunPrompt result = %q, want %q", resp.Result, wantCurrentResult)
	}
	select {
	case waitErr := <-waitDone:
		if waitErr != nil {
			t.Fatalf("LiveWait: %v", waitErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for LiveWait to finish")
	}
	waitResp := <-waitResult
	if waitResp.Result == nil || *waitResp.Result != wantCurrentResult {
		t.Fatalf("LiveWait result = %v, want %q", waitResp.Result, wantCurrentResult)
	}
	select {
	case <-secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for queued or steered follow-up request")
	}
}
