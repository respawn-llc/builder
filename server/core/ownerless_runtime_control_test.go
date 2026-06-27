package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/metadata"
	"core/shared/serverapi"
)

func TestSecondClientSteersDuringActiveRun(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	release := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if handleTestOpenAIInputTokenCount(w, r, 1) {
			return
		}
		if r.URL.Path != "/responses" {
			t.Errorf("unexpected path %q", r.URL.Path)
			return
		}
		startOnce.Do(func() { close(started) })
		<-release
		writeTestOpenAICompletedResponseStream(w, "ok", 1, 1)
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
	go func() {
		_, runErr := runClient.RunPrompt(context.Background(), serverapi.RunPromptRequest{
			ClientRequestID:   "run-1",
			SelectedSessionID: sessionID,
			Prompt:            "drive the run",
		}, nil)
		runDone <- runErr
	}()

	<-started

	steerResp, err := appCore.RuntimeControlClient().QueueUserMessage(context.Background(), serverapi.RuntimeQueueUserMessageRequest{
		ClientRequestID: "steer-1",
		SessionID:       sessionID,
		Text:            "steer me",
	})
	if err != nil {
		t.Fatalf("QueueUserMessage during active run: %v", err)
	}
	if steerResp.QueueItemID == "" {
		t.Fatal("QueueUserMessage during active run returned no queue item id")
	}

	close(release)
	if runErr := <-runDone; runErr != nil {
		t.Fatalf("RunPrompt: %v", runErr)
	}
}
