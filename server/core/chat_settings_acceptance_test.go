package core

import (
	"context"
	"sync"
	"testing"
	"time"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtimewire"
	brand "core/shared/config"
	"core/shared/protoapi"
	sessionlaunchpb "core/shared/protoapi/gen/kent/api/session_launch"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
)

type chatSettingsBoundaryLLMClient struct {
	started     chan struct{}
	startedOnce sync.Once
	release     chan struct{}
	releaseOnce sync.Once
}

func newChatSettingsBoundaryLLMClient() *chatSettingsBoundaryLLMClient {
	return &chatSettingsBoundaryLLMClient{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (c *chatSettingsBoundaryLLMClient) Generate(
	ctx context.Context,
	_ llm.Request,
	_ llm.StreamCallbacks,
) (llm.Response, error) {
	c.startedOnce.Do(func() { close(c.started) })
	select {
	case <-c.release:
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200_000},
		}, nil
	case <-ctx.Done():
		return llm.Response{}, context.Cause(ctx)
	}
}

func (*chatSettingsBoundaryLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}

func (c *chatSettingsBoundaryLLMClient) unblock() {
	c.releaseOnce.Do(func() { close(c.release) })
}

func TestChatSettingsMutationReturnsAfterRuntimeAcceptance(t *testing.T) {
	workspace := t.TempDir()
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{
		WorkspaceRoot: workspace,
		LoadOptions:   brand.LoadOptions{ConfigRoot: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	binding, err := metadata.RegisterBinding(t.Context(), resolved.Config.PersistenceRoot, workspace)
	if err != nil {
		t.Fatalf("RegisterBinding: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	t.Cleanup(func() { _ = runtimeSupport.Background.Close() })
	model := newChatSettingsBoundaryLLMClient()
	appCore, err := NewWithContextOptions(t.Context(), resolved.Config, authSupport, runtimeSupport, Options{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(
			context.Context,
			runtimewire.RuntimeClientRequest,
		) (llm.Client, error) {
			return model, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewWithContextOptions: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	launch, err := appCore.SessionLaunchClientForProjectWorkspace(t.Context(), binding.ProjectID, workspace)
	if err != nil {
		t.Fatalf("SessionLaunchClientForProjectWorkspace: %v", err)
	}
	intent, err := protoapi.SessionLaunchIntentToProto(
		serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	)
	if err != nil {
		t.Fatalf("convert Session launch intent: %v", err)
	}
	planned, err := launch.PlanSession(t.Context(), &sessionlaunchpb.SessionPlanRequest{
		Mode:   sessionlaunchpb.SessionLaunchMode_SESSION_LAUNCH_MODE_INTERACTIVE,
		Intent: intent,
	})
	if err != nil {
		t.Fatalf("PlanSession: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(planned.Plan.SessionId)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	chatSettings := appCore.ChatSettingsClient()
	before, err := chatSettings.ReadChatSettings(t.Context(), serverapi.ChatSettingsReadRequest{
		Target: serverapi.SessionChatSettingsTarget(sessionID),
	})
	if err != nil {
		t.Fatalf("ReadChatSettings before mutation: %v", err)
	}
	initialQuestions := before.Session.Settings.Questions.Enabled
	requestedQuestions := !initialQuestions

	const ownerID = "chat-settings-acceptance-test"
	activation, err := appCore.SessionRuntimeClient().ActivateSessionRuntime(t.Context(), serverapi.SessionRuntimeActivateRequest{
		SessionID:             sessionID.String(),
		OwnerID:               ownerID,
		ActiveSettings:        resolved.Config.Settings,
		QuestionsEnabled:      textutil.Value(initialQuestions),
		AutoCompactionEnabled: textutil.Value(before.Session.Settings.AutoCompaction.Effective),
		Source:                resolved.Config.Source,
	})
	if err != nil {
		t.Fatalf("ActivateSessionRuntime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = appCore.SessionRuntimeClient().ReleaseSessionRuntime(context.Background(), serverapi.SessionRuntimeReleaseRequest{
			Attachment:  activation.Attachment,
			OwnerID:     ownerID,
			DropOwner:   true,
			ClosePolicy: serverapi.SessionRuntimeReleaseClosePolicyDetachOnly,
		})
	})
	t.Cleanup(model.unblock)

	turnDone := make(chan error, 1)
	go func() {
		_, submitErr := appCore.RuntimeControlClient().SubmitUserTurn(t.Context(), serverapi.RuntimeSubmitUserTurnRequest{
			SessionID: sessionID.String(),
			Input:     runtimeinput.Text("hold the Agent Step"),
		})
		turnDone <- submitErr
	}()
	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("Agent Step did not reach the model request")
	}

	type mutationResult struct {
		response serverapi.ChatSettingsMutationResponse
		err      error
	}
	mutationDone := make(chan mutationResult, 1)
	go func() {
		response, mutationErr := chatSettings.MutateChatSettings(t.Context(), serverapi.ChatSettingsMutationRequest{
			SessionID: sessionID,
			Operation: serverapi.ChatSettingsMutationOperation{
				Kind:    serverapi.ChatSettingsMutationQuestions,
				Enabled: textutil.Value(requestedQuestions),
			},
		})
		mutationDone <- mutationResult{response: response, err: mutationErr}
	}()

	var mutation mutationResult
	select {
	case mutation = <-mutationDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Chat settings mutation waited for the active Agent Step to end")
	}
	if mutation.err != nil {
		t.Fatalf("MutateChatSettings: %v", mutation.err)
	}
	if mutation.response.Result.Applied == nil || !mutation.response.Result.Applied.Changed {
		t.Fatalf("mutation result = %+v, want a durable change", mutation.response.Result)
	}
	if mutation.response.Settings.Questions.Enabled != requestedQuestions {
		t.Fatalf(
			"response Questions = %t, want durably committed %t",
			mutation.response.Settings.Questions.Enabled,
			requestedQuestions,
		)
	}
	if live := currentCoreRuntimeQuestions(t, appCore, sessionID); live != initialQuestions {
		t.Fatalf("live Questions = %t before Agent Step boundary, want %t", live, initialQuestions)
	}

	model.unblock()
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatalf("SubmitUserTurn: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Agent Step did not finish after model release")
	}
	waitForCoreRuntimeQuestions(t, appCore, sessionID, requestedQuestions)
}

func currentCoreRuntimeQuestions(t *testing.T, appCore *Core, sessionID runtimeids.SessionID) bool {
	t.Helper()
	response, err := appCore.SessionViewClient().GetSessionMainView(t.Context(), serverapi.SessionMainViewRequest{
		SessionID: sessionID.String(),
	})
	if err != nil {
		t.Fatalf("GetSessionMainView: %v", err)
	}
	return response.MainView.Status.QuestionsEnabled
}

func waitForCoreRuntimeQuestions(t *testing.T, appCore *Core, sessionID runtimeids.SessionID, expected bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if currentCoreRuntimeQuestions(t, appCore, sessionID) == expected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("live Questions did not converge to %t after the Agent Step boundary", expected)
		}
		time.Sleep(time.Millisecond)
	}
}
