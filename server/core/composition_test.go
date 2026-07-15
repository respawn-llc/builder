package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/scriptedllm"
	"core/internal/testharness/testsetup"
	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/llm"
	"core/server/registry"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/skillcatalog"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowrunner"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

func TestNewWithContextComposesRequiredBundles(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}

	appCore, err := NewWithContext(t.Context(), resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("NewWithContext: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	if appCore.bundles == nil {
		t.Fatal("expected bundles")
	}
	if appCore.bundles.Auth == nil || appCore.bundles.Auth.authBootstrap == nil || appCore.bundles.Auth.authStatus == nil {
		t.Fatal("expected auth bundle clients")
	}
	if appCore.bundles.Persistence == nil || appCore.bundles.Persistence.rootLock == nil || appCore.bundles.Persistence.metadataStore == nil || appCore.bundles.Persistence.sessionStores == nil {
		t.Fatal("expected persistence bundle resources")
	}
	if appCore.bundles.Processes == nil || appCore.bundles.Processes.processControls == nil || appCore.bundles.Processes.processOutput == nil || appCore.bundles.Processes.processViews == nil {
		t.Fatal("expected process bundle clients")
	}
	if appCore.bundles.Projects == nil || appCore.bundles.Projects.projectViews == nil {
		t.Fatal("expected project bundle client")
	}
	if appCore.bundles.Prompts == nil || appCore.bundles.Prompts.askViews == nil || appCore.bundles.Prompts.approvalViews == nil || appCore.bundles.Prompts.promptControl == nil || appCore.bundles.Prompts.promptActivity == nil {
		t.Fatal("expected prompt bundle clients")
	}
	if appCore.bundles.Runtime == nil || appCore.bundles.Runtime.background == nil || appCore.bundles.Runtime.backgroundRouter == nil || appCore.bundles.Runtime.runtimeRegistry == nil || appCore.bundles.Runtime.runtimeControls == nil || appCore.bundles.Runtime.sessionRuntime == nil || appCore.bundles.Runtime.sessionActivity == nil || appCore.bundles.Runtime.sessionTranscript == nil {
		t.Fatal("expected runtime bundle services")
	}
	if appCore.bundles.Sessions == nil || appCore.bundles.Sessions.sessionLaunch == nil || appCore.bundles.Sessions.sessionViews == nil || appCore.bundles.Sessions.sessionLifecycle == nil || appCore.bundles.Sessions.runPrompt == nil {
		t.Fatal("expected session bundle clients")
	}
	if appCore.bundles.Updates == nil || appCore.bundles.Updates.updateStatus == nil {
		t.Fatal("expected update status bundle")
	}
	if appCore.bundles.Worktrees == nil || appCore.bundles.Worktrees.worktrees == nil {
		t.Fatal("expected worktree bundle client")
	}
	if appCore.bundles.Workflows == nil || appCore.bundles.Workflows.workflows == nil {
		t.Fatal("expected workflow bundle client")
	}
	if appCore.bundles.Workflows.scheduler == nil || !appCore.bundles.Workflows.scheduler.Started() {
		t.Fatal("expected started workflow scheduler")
	}
	scheduler := appCore.bundles.Workflows.scheduler
	if err := appCore.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !scheduler.Stopped() {
		t.Fatal("expected workflow scheduler to stop during core close")
	}
}

func TestNewWithContextOptionsAcceptsRuntimeClientFactory(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	appCore, err := NewWithContextOptions(t.Context(), resolved.Config, authSupport, runtimeSupport, Options{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return nil, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewWithContextOptions: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	if appCore.bundles == nil || appCore.bundles.Runtime == nil || appCore.bundles.Runtime.sessionRuntimeService == nil {
		t.Fatal("expected runtime bundle with session runtime service")
	}
}

func TestComposedWorkflowTaskSetupPrecedesFirstModelRequest(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KENT_WORKTREE_SESSION_ID", "stale-parent-session")
	testsetup.InitializeGitRepository(t, workspace)
	sourceWorkspaceRoot, err := config.CanonicalWorkspaceRoot(workspace)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	resolved.Config.Settings.Workflow.CompletionMode = config.WorkflowCompletionModeStructuredOutput
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}

	observation := make(chan error, 1)
	const skillName = "setup-created-workflow-skill"
	const skillDescription = "created before the first workflow model request"
	markerRelativePath := filepath.Join(".kent", "setup-marker")
	invocationCountRelativePath := filepath.Join(".kent", "setup-invocations")
	setup := testsetup.New(t, testsetup.Options{
		MarkerRelativePath:          &markerRelativePath,
		InvocationCountRelativePath: &invocationCountRelativePath,
		Skill: &testsetup.Skill{
			Name:        skillName,
			Description: skillDescription,
		},
	})
	resolved.Config.Settings.Worktrees.SetupScript = setup.InstallInSourceWorkspace(t, workspace)
	observerClient := &firstGenerateObserverClient{
		delegate: scriptedllm.NewLegacyOnlyClient(
			llm.ProviderCapabilities{ProviderID: "scripted-workflow", SupportsResponsesAPI: true},
			scriptedllm.FinalAnswer(`{"commentary":"done"}`),
		),
		observation:      observation,
		setup:            setup,
		skillName:        skillName,
		skillDescription: skillDescription,
	}
	appCore, err := NewWithContextOptions(ctx, resolved.Config, authSupport, runtimeSupport, Options{
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return observerClient, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewWithContextOptions: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	project, err := appCore.ProjectViewClient().CreateProject(ctx, serverapi.ProjectCreateRequest{
		DisplayName:   "Workflow setup composition",
		ProjectKey:    "SETUP",
		WorkspaceRoot: workspace,
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	workflowStore, err := workflowstore.New(appCore.bundles.Persistence.metadataStore)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflowID := createCoreValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, project.Binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := appCore.WorkflowClient().CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID:         project.Binding.ProjectID,
		WorkflowID:        string(workflowID),
		Title:             "Provision task worktree",
		SourceWorkspaceID: project.Binding.WorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	started, err := appCore.WorkflowClient().StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if err := started.Validate(); err != nil || started.Applied == nil || started.Applied.RunID == "" {
		t.Fatalf("StartWorkflowTask response = %+v, validation error = %v", started, err)
	}

	select {
	case err := <-observation:
		if err != nil {
			t.Fatalf("first workflow model request observed setup violation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first workflow model request")
	}

	invocation, err := setup.Invocation()
	if err != nil {
		t.Fatalf("setup invocation: %v", err)
	}
	payload, err := invocation.Payload()
	if err != nil {
		t.Fatalf("setup payload: %v", err)
	}
	if payload.SourceWorkspaceRoot != sourceWorkspaceRoot || payload.BranchName != task.Task.ShortID || payload.WorktreeRoot == "" {
		t.Fatalf("workflow setup payload = %+v", payload)
	}
	if payload.SessionID != nil {
		t.Fatalf("workflow setup session_id = %q, want nil", *payload.SessionID)
	}
	if err := invocation.Verify(payload); err != nil {
		t.Fatalf("workflow setup process contract: %v", err)
	}
	waitForCoreWorkflowTaskDone(t, appCore, task.Task.ID)
}

func TestRuntimePendingAskResolverUsesPendingPromptSource(t *testing.T) {
	resolver := runtimePendingAskResolver{prompts: fakePendingPromptSource{items: map[string][]registry.PendingPromptSnapshot{
		"session-1": {
			{Request: askquestion.AskQuestionRequest{ID: "ask-1", Question: "Need input?"}, CreatedAt: time.Unix(1, 0)},
			{Request: askquestion.AskQuestionRequest{ID: "approval-1", Question: "Approve?", Approval: true}, CreatedAt: time.Unix(2, 0)},
			{Request: askquestion.AskQuestionRequest{ID: "approval-2", Question: "Approve?", Approval: true, AttentionTarget: &clientui.AttentionNotificationTarget{
				Kind: clientui.AttentionNotificationTargetWorkflowTask,
				Focus: &clientui.AttentionNotificationTaskDetailFocus{
					Kind:   clientui.AttentionNotificationFocusQuestion,
					AskIDs: []string{"approval-2"},
				},
			}}, CreatedAt: time.Unix(3, 0)},
		},
	}}}

	ok, err := resolver.CanRehydrate(t.Context(), "session-1", workflow.RunID("run-1"), "ask-1")
	if err != nil {
		t.Fatalf("CanRehydrate ask: %v", err)
	}
	if !ok {
		t.Fatal("expected pending ordinary ask to rehydrate")
	}
	ok, err = resolver.CanRehydrate(t.Context(), "session-1", workflow.RunID("run-1"), "approval-1")
	if err != nil {
		t.Fatalf("CanRehydrate approval: %v", err)
	}
	if ok {
		t.Fatal("generic approval prompt must not satisfy workflow ask rehydration")
	}
	ok, err = resolver.CanRehydrate(t.Context(), "session-1", workflow.RunID("run-1"), "approval-2")
	if err != nil {
		t.Fatalf("CanRehydrate task approval: %v", err)
	}
	if !ok {
		t.Fatal("task-scoped approval prompt should satisfy workflow ask rehydration")
	}
	ok, err = resolver.CanRehydrate(t.Context(), "session-1", workflow.RunID("run-1"), "missing")
	if err != nil {
		t.Fatalf("CanRehydrate missing: %v", err)
	}
	if ok {
		t.Fatal("missing ask should not rehydrate")
	}
}

func TestComposedWorkflowTaskDetailResolvesPendingQuestionFromSessionTranscript(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	appCore, err := NewWithContext(ctx, resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("NewWithContext: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })

	metadataStore := appCore.bundles.Persistence.metadataStore
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, resolved.Config.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	workflowStore, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(workflow.StaticRoleResolver{"coder": true}))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflowID := createCoreValidWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	task, err := workflowStore.CreateTask(ctx, workflowstore.CreateTaskRequest{ProjectID: binding.ProjectID, Title: "Needs answer", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := workflowStore.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	claimed, err := workflowStore.ClaimRun(ctx, started.RunID, 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	sessionsDir := filepath.Join(filepath.Join(resolved.Config.PersistenceRoot, "projects"), binding.ProjectID, "sessions")
	sessionStore, err := session.Create(sessionsDir, filepath.Base(sessionsDir), resolved.Config.WorkspaceRoot, sessioncontract.SessionCategoryMain, metadataStore.AuthoritativeSessionStoreOptions()...)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	askInput := json.RawMessage(`{"question":"Question from composed session transcript?"}`)
	if _, _, err := sessionStore.AppendEvent("step-ask", "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "ask-core", Name: string(toolspec.ToolAskQuestion), Input: askInput}}}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := workflowStore.AttachRunSession(ctx, started.RunID, claimed.Generation, sessionStore.Meta().SessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	if err := workflowStore.SetRunWaitingAsk(ctx, started.RunID, claimed.Generation, "ask-core"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}

	detail, err := appCore.WorkflowClient().GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("GetWorkflowTask: %v", err)
	}
	if len(detail.Task.Attention) != 1 || detail.Task.Attention[0].Message != "Question from composed session transcript?" {
		t.Fatalf("attention = %+v", detail.Task.Attention)
	}
}

func TestRuntimeTaskWorktreeRestorerRequiresService(t *testing.T) {
	if err := (runtimeTaskWorktreeRestorer{}).RestoreLockedTaskWorktree(
		t.Context(),
		workflowrunner.LockedTaskWorktreeRestoreRequest{},
	); err == nil {
		t.Fatal("RestoreLockedTaskWorktree succeeded without a worktree service")
	}
}

func TestComposedAttentionClientUsesOneRootGlobalTaskDetailBroker(t *testing.T) {
	ctx := context.Background()
	appCore := newComposedCoreForAttentionTest(t)
	registerCoreRuntime(t, appCore, "session-1")
	registerCoreRuntime(t, appCore, "session-2")

	desktop, err := appCore.AttentionNotificationClient().SubscribeAttentionNotifications(ctx, serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	sessionOne, err := appCore.AttentionNotificationClient().SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications session-1: %v", err)
	}
	sessionTwo, err := appCore.AttentionNotificationClient().SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: "session-2"})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications session-2: %v", err)
	}

	appCore.BeginPendingPrompt("session-1", coreTaskBatchAskRequest("ask-a", "project-a", "task-a", "session-1"))
	appCore.BeginPendingPrompt("session-2", coreTaskBatchAskRequest("ask-b", "project-b", "task-b", "session-2"))

	firstDesktop := nextCoreAttentionEvent(t, desktop)
	secondDesktop := nextCoreAttentionEvent(t, desktop)
	if firstDesktop.Pending.Target.ProjectID != "project-a" || secondDesktop.Pending.Target.ProjectID != "project-b" {
		t.Fatalf("desktop project delivery = %+v then %+v", firstDesktop, secondDesktop)
	}
	if event := nextCoreAttentionEvent(t, sessionOne); event.Pending.Target.TaskID != "task-a" {
		t.Fatalf("session-1 task-detail event = %+v", event)
	}
	if event := nextCoreAttentionEvent(t, sessionTwo); event.Pending.Target.TaskID != "task-b" {
		t.Fatalf("session-2 task-detail event = %+v", event)
	}
}

func TestComposedAttentionClientKeepsGenericPromptsOffDesktopRootStream(t *testing.T) {
	ctx := context.Background()
	appCore := newComposedCoreForAttentionTest(t)
	registerCoreRuntime(t, appCore, "session-1")

	desktop, err := appCore.AttentionNotificationClient().SubscribeAttentionNotifications(ctx, serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	sessionSub, err := appCore.AttentionNotificationClient().SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}

	appCore.BeginPendingPrompt("session-1", askquestion.AskQuestionRequest{ID: "ask-generic", Question: "Generic prompt?"})
	pending := nextCoreAttentionEvent(t, sessionSub)
	if pending.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt {
		t.Fatalf("generic prompt target = %+v", pending.Pending.Target)
	}
	if event, err := desktop.Next(shortCoreAttentionContext(t)); err == nil {
		t.Fatalf("desktop received generic pending event: %+v", event)
	}

	appCore.CompletePendingPrompt("session-1", "ask-generic")
	resolved := nextCoreAttentionEvent(t, sessionSub)
	genericID := clientui.AttentionNotificationID{
		Kind: clientui.AttentionNotificationKindQuestion,
		UUID: "ask-generic",
	}
	if resolved.Type != clientui.AttentionNotificationEventResolved || !attentionNotificationEventIDMatches(resolved, genericID) {
		t.Fatalf("generic resolved event = %+v", resolved)
	}
	if event, err := desktop.Next(shortCoreAttentionContext(t)); err == nil {
		t.Fatalf("desktop received generic resolved event: %+v", event)
	}
}

func TestComposedAttentionClientRoutesTargetlessTaskDetailResolvedToDesktop(t *testing.T) {
	ctx := context.Background()
	appCore := newComposedCoreForAttentionTest(t)
	registerCoreRuntime(t, appCore, "session-1")

	desktop, err := appCore.AttentionNotificationClient().SubscribeAttentionNotifications(ctx, serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	req := coreTaskBatchAskRequest("ask-a", "project-a", "task-a", "session-1")
	appCore.BeginPendingPrompt("session-1", req)
	pending := nextCoreAttentionEvent(t, desktop)
	if pending.Type != clientui.AttentionNotificationEventPending {
		t.Fatalf("pending event = %+v", pending)
	}
	appCore.bundles.Runtime.runtimeRegistry.MarkTaskQuestionCleared(*req.QuestionBatch, "ask-a")
	resolved := nextCoreAttentionEvent(t, desktop)
	if resolved.Type != clientui.AttentionNotificationEventResolved || resolved.Pending != nil || !attentionNotificationEventIDMatches(resolved, pending.Pending.ID) {
		t.Fatalf("targetless resolved event = %+v", resolved)
	}
}

func newComposedCoreForAttentionTest(t *testing.T) *Core {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)

	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: workspace})
	if err != nil {
		t.Fatalf("ResolveConfig: %v", err)
	}
	authSupport, err := serverbootstrap.BuildAuthSupport(auth.NewMemoryStore(auth.EmptyState()), nil, nil)
	if err != nil {
		t.Fatalf("BuildAuthSupport: %v", err)
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(resolved.Config)
	if err != nil {
		t.Fatalf("BuildRuntimeSupport: %v", err)
	}
	appCore, err := NewWithContext(t.Context(), resolved.Config, authSupport, runtimeSupport)
	if err != nil {
		t.Fatalf("NewWithContext: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	return appCore
}

func registerCoreRuntime(t *testing.T, appCore *Core, sessionID string) {
	t.Helper()
	claim, _, _ := appCore.bundles.Runtime.runtimeRegistry.AcquireRuntimeClaim(sessionID, "")
	if claim == nil {
		t.Fatalf("AcquireRuntimeClaim(%q) returned nil claim", sessionID)
	}
	claim.Resolve(&runtime.Engine{}, nil, nil)
	t.Cleanup(func() {
		if active := appCore.bundles.Runtime.runtimeRegistry.RuntimeClaimFor(sessionID); active != nil {
			_, _ = active.Close(context.Background(), nil)
		}
	})
}

func coreTaskBatchAskRequest(askID string, projectID string, taskID string, sessionID string) askquestion.AskQuestionRequest {
	return askquestion.AskQuestionRequest{
		ID:       askID,
		Question: "Task question?",
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               "run-" + taskID,
			StepID:              "step-" + taskID,
			BatchID:             "batch-" + taskID,
			PromptID:            askID,
			BatchPromptIDs:      []string{askID},
			CandidateOrdinal:    0,
			PreparedPromptCount: 1,
		},
		AttentionTarget: &clientui.AttentionNotificationTarget{
			Kind:      clientui.AttentionNotificationTargetWorkflowTask,
			ProjectID: projectID,
			TaskID:    taskID,
			SessionID: sessionID,
			RunID:     "run-" + taskID,
			Focus: &clientui.AttentionNotificationTaskDetailFocus{
				Kind:   clientui.AttentionNotificationFocusQuestion,
				AskIDs: []string{askID},
			},
		},
	}
}

func nextCoreAttentionEvent(t *testing.T, sub serverapi.AttentionNotificationSubscription) clientui.AttentionNotificationEvent {
	t.Helper()
	event, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return event
}

func shortCoreAttentionContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func attentionNotificationEventIDMatches(event clientui.AttentionNotificationEvent, id clientui.AttentionNotificationID) bool {
	return event.ID != nil && *event.ID == id
}

type fakePendingPromptSource struct {
	items map[string][]registry.PendingPromptSnapshot
}

func waitForCoreWorkflowTaskDone(t *testing.T, appCore *Core, taskID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		detail, err := appCore.WorkflowClient().GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{TaskID: taskID})
		if err != nil {
			t.Fatalf("GetWorkflowTask: %v", err)
		}
		if detail.Task.Summary.Done {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("workflow task %q did not complete", taskID)
}

type firstGenerateObserverClient struct {
	delegate         *scriptedllm.LegacyClient
	observation      chan<- error
	setup            testsetup.Fixture
	skillName        string
	skillDescription string
	once             sync.Once
}

func (c *firstGenerateObserverClient) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	c.once.Do(func() {
		c.observation <- c.observe(request)
	})
	return c.delegate.Generate(ctx, request)
}

func (c *firstGenerateObserverClient) ProviderCapabilities(ctx context.Context) (llm.ProviderCapabilities, error) {
	return c.delegate.ProviderCapabilities(ctx)
}

func (c *firstGenerateObserverClient) observe(request llm.Request) error {
	worktreeRoot := ""
	for _, item := range request.Items {
		if item.MessageType != llm.MessageTypeWorktreeMode {
			continue
		}
		if item.WorktreeContext != nil {
			worktreeRoot = strings.TrimSpace(item.WorktreeContext.EffectiveCwd)
		}
	}
	if worktreeRoot == "" {
		return errors.New("first request has no typed worktree effective cwd")
	}
	if _, err := os.Stat(c.setup.MarkerPath(worktreeRoot)); err != nil {
		return fmt.Errorf("setup marker before first request: %w", err)
	}
	invocations, err := c.setup.InvocationCount(worktreeRoot)
	if err != nil {
		return fmt.Errorf("read setup invocation count: %w", err)
	}
	if invocations != 1 {
		return fmt.Errorf("setup invocation count = %d, want exactly one", invocations)
	}
	discovery, err := skillcatalog.Discover(skillcatalog.Options{WorkspaceRoot: worktreeRoot})
	if err != nil {
		return fmt.Errorf("discover setup-created skill: %w", err)
	}
	skillPath := c.setup.SkillPath(worktreeRoot)
	skillFound := false
	for _, skill := range discovery.Skills {
		if skill.Path != skillPath {
			continue
		}
		skillFound = true
		if skill.Name != c.skillName || skill.Description != c.skillDescription {
			return fmt.Errorf("setup-created skill = %+v, want name %q description %q", skill, c.skillName, c.skillDescription)
		}
	}
	if !skillFound {
		return fmt.Errorf("setup-created skill is not discoverable at %q", skillPath)
	}
	for _, item := range request.Items {
		if item.Role != llm.RoleDeveloper || item.MessageType != llm.MessageTypeSkills {
			continue
		}
		return nil
	}
	return errors.New("first request has no structured skills item")
}

func (f fakePendingPromptSource) ListPendingPrompts(sessionID string) []registry.PendingPromptSnapshot {
	return append([]registry.PendingPromptSnapshot(nil), f.items[sessionID]...)
}

func createCoreValidWorkflow(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
	t.Helper()
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := coreWorkflowNodeByKind(t, def, workflow.NodeKindStart)
	done := coreWorkflowNodeByKind(t, def, workflow.NodeKindTerminal)
	agentID := workflow.NodeID("node-agent-" + string(created.ID))
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: workflow.DefaultAgentRole}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	startGroupID := workflow.TransitionGroupID("group-start-" + string(created.ID))
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	doneGroupID := workflow.TransitionGroupID("group-done-" + string(created.ID))
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: workflow.NodeIDOf(done), ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func coreWorkflowNodeByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind() == kind {
			return node
		}
	}
	t.Fatalf("missing workflow node kind %q in %+v", kind, def.Nodes)
	return nil
}
