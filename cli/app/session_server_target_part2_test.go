package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"core/cli/app/internal/projectbinding"
	serverstartup "core/server/startup"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/toolspec"
)

func TestRemoteNoAuthUnregisteredWorkspaceBindingCanPrepareRuntime(t *testing.T) {
	newAppTestHome(t)
	workspace := t.TempDir()
	configureAppTestServerPort(t)
	fakeResponses, hits := newNoAuthFakeResponsesServer(t, []string{"rebound no-auth reply"})
	defer fakeResponses.Close()

	srv, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		Model:                "gpt-5",
		AllowUnauthenticated: true,
	}, memoryAuthHandler{}, autoOnboarding)
	if err != nil {
		t.Fatalf("serve.Start: %v", err)
	}
	defer func() { _ = srv.Close() }()
	stopServing := serveAppServer(t, srv)
	defer stopServing()
	waitForConfiguredRunPromptDaemon(t, workspace)

	originalPicker := runProjectBindingPickerFlow
	originalPrompt := runProjectNamePromptFlow
	t.Cleanup(func() {
		runProjectBindingPickerFlow = originalPicker
		runProjectNamePromptFlow = originalPrompt
	})
	runProjectBindingPickerFlow = func(context.Context, []clientui.ProjectSummary, string, projectbinding.ProjectPickerSnapshot) (projectBindingPickerResult, error) {
		return projectbinding.ProjectPickerCreateNew{}, nil
	}
	runProjectNamePromptFlow = func(defaultName string, theme string) (string, error) {
		return "Remote No Auth Project", nil
	}

	authPickerCalls := 0
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			authPickerCalls++
			if authPickerCalls > 1 {
				t.Fatal("remote no-auth binding flow must not re-enter auth picker")
			}
			return authMethodPickerResult{Choice: authMethodChoiceSkip}, nil
		},
	}
	server, err := startSessionServer(context.Background(), Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true, Model: "gpt-5"}, interactor, true)
	if err != nil {
		t.Fatalf("startSessionServer: %v", err)
	}
	defer func() { _ = server.Close() }()
	bound, err := ensureInteractiveProjectBinding(context.Background(), server)
	if err != nil {
		t.Fatalf("ensureInteractiveProjectBinding: %v", err)
	}
	_, runtimePlan := prepareAppRuntimePlanWithOpenAIBaseURL(t, bound, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, fakeResponses.URL, io.Discard, "test remote no-auth rebound runtime")
	submission, err := submitRuntimeClientForTest(t, runtimePlan.Wiring.runtimeClient, "hello after rebound no auth")
	requireQueuedAppTestUserTurn(t, submission, err)
	waitForRemoteTranscriptAssistantFinal(
		t,
		runtimePlan.Wiring.eventDispatcher.transcriptEvents,
		"rebound no-auth reply",
	)
	runtimePlan.Close()
	if hits.Load() != 1 {
		t.Fatalf("expected fake LLM call once, got %d", hits.Load())
	}
}

func TestStartSessionServerUsesInvocationOverridesWhenAttachingToDiscoveredDaemon(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)

	defaultResponses, defaultHits := newFakeResponsesServer(t, []string{"interactive daemon default"})
	defer defaultResponses.Close()
	overrideResponses, overrideHits := newFakeResponsesServer(t, []string{"interactive daemon override"})
	defer overrideResponses.Close()

	fixture := startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5.4",
		OpenAIBaseURL:         defaultResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"))

	server := fixture.attachRemoteSessionServer(t, Options{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5.3-codex",
		Tools:                 "shell",
		OpenAIBaseURL:         overrideResponses.URL,
		OpenAIBaseURLExplicit: true,
	}, newHeadlessAuthInteractor())

	plan, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test remote interactive runtime override")
	defer closeRuntimeLaunchPlan(t, runtimePlan)
	if plan.ActiveSettings.Model != "gpt-5.3-codex" {
		t.Fatalf("model = %q, want gpt-5.3-codex", plan.ActiveSettings.Model)
	}
	if plan.StatusConfig.AuthSelection == nil ||
		plan.StatusConfig.AuthSelection.OpenaiBaseUrl == nil ||
		!strings.HasPrefix(*plan.StatusConfig.AuthSelection.OpenaiBaseUrl, "http://127.0.0.1:") {
		t.Fatalf("status auth selection = %+v, want invocation override", plan.StatusConfig.AuthSelection)
	}
	if len(plan.EnabledTools) != 1 || plan.EnabledTools[0] != toolspec.ToolExecCommand {
		t.Fatalf("enabled tools = %+v, want only shell", plan.EnabledTools)
	}

	submission, err := submitRuntimeClientForTest(t, runtimePlan.Wiring.runtimeClient, "hello through interactive override")
	requireQueuedAppTestUserTurn(t, submission, err)
	waitForRemoteTranscriptAssistantFinal(
		t,
		runtimePlan.Wiring.eventDispatcher.transcriptEvents,
		"interactive daemon override",
	)
	if overrideHits.Load() != 1 {
		t.Fatalf("expected override llm call once, got %d", overrideHits.Load())
	}
	if defaultHits.Load() != 0 {
		t.Fatalf("expected daemon default llm endpoint unused, got %d", defaultHits.Load())
	}
}

func TestStartSessionServerUsesConfiguredDaemonForPromptRoundTrip(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	t.Setenv("KENT_REVIEWER_FREQUENCY", "off")
	model := newAppTestModelServer(t,
		appTestModelStep{Calls: []appTestModelToolCall{
			appTestAskCall("ask-1", "Pick one", []string{"one", "two"}, 2),
		}},
		appTestModelStep{Calls: []appTestModelToolCall{
			appTestOutsidePatchCall("patch-1", appTestOutsidePatchPath(t)),
		}},
		appTestModelStep{Final: "prompt round trip complete"},
	)
	defer model.Close()

	fixture := startConfiguredDaemonFixture(t, workspace, serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		Model:                 "gpt-5",
		OpenAIBaseURL:         model.URL(),
		OpenAIBaseURLExplicit: true,
	}, apiKeyMemoryAuthHandler("test-key"))

	server := fixture.attachRemoteSessionServer(t, Options{WorkspaceRoot: workspace, WorkspaceRootExplicit: true}, readyMemoryAuthHandler())
	_, runtimePlan := prepareAppRuntimePlan(t, server, sessionLaunchRequest{Mode: launchModeInteractive, Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())}, io.Discard, "test remote prompt round trip")
	defer closeRuntimeLaunchPlan(t, runtimePlan)

	submissionDone, submissionFailed := startAppTestRuntimeSubmission(t, runtimePlan.Wiring.runtimeClient, "start prompt round trip")
	requireQueuedAppTestRuntimeSubmission(t, submissionDone)
	askPrompt := waitForRemoteTranscriptPrompt(t, runtimePlan.Wiring.eventDispatcher.transcriptEvents, "ask-1", submissionFailed)
	if askPrompt.Kind != clientui.TranscriptPromptKindQuestion || askPrompt.Question != "Pick one" {
		t.Fatalf("unexpected ask prompt: %+v", askPrompt)
	}
	answerRemoteTranscriptPrompt(t, runtimePlan.Wiring.promptAnswers, askPrompt, clientui.PromptAnswer{
		ToolCallID:           askPrompt.ToolCallID,
		SelectedOptionNumber: func() *int { selected := 2; return &selected }(),
	})
	approvalPrompt := waitForRemoteTranscriptPrompt(t, runtimePlan.Wiring.eventDispatcher.transcriptEvents, "", submissionFailed)
	if approvalPrompt.Kind != clientui.TranscriptPromptKindApproval {
		t.Fatalf("unexpected approval prompt: %+v", approvalPrompt)
	}
	answerRemoteTranscriptPrompt(t, runtimePlan.Wiring.promptAnswers, approvalPrompt, clientui.PromptAnswer{
		ToolCallID: approvalPrompt.ToolCallID,
		Approval: &clientui.ApprovalPromptAnswer{
			Decision:   clientui.ApprovalDecisionAllowOnce,
			Commentary: "trusted",
		},
	})
	waitForRemoteTranscriptAssistantFinal(
		t,
		runtimePlan.Wiring.eventDispatcher.transcriptEvents,
		"prompt round trip complete",
		submissionFailed,
	)
}
