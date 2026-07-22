package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"core/server/sessionruntime"
	"core/server/skillcatalog"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

func TestComposedWorkflowTaskSetupPrecedesFirstModelRequest(t *testing.T) {
	t.Setenv("KENT_WORKTREE_SESSION_ID", "stale-parent-session")
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
	blockedBase := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedBase, []byte("blocked"), 0o644); err != nil {
		t.Fatalf("write blocked base path: %v", err)
	}
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
	var configPath string
	var validSetupConfig string
	fixture := newComposedWorkflowFixture(t, composedWorkflowFixtureOptions{
		PrepareWorkspace: func(t *testing.T, workspaceRoot string) {
			t.Helper()
			setupScript := setup.InstallInSourceWorkspace(t, workspaceRoot)
			configPath = filepath.Join(workspaceRoot, config.ConfigDirName, "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatalf("create source workspace config directory: %v", err)
			}
			validSetupConfig = fmt.Sprintf(
				"[worktrees]\nbase_dir = %q\nsetup_script = %q\n",
				filepath.Join(blockedBase, "worktrees"),
				filepath.ToSlash(setupScript),
			)
			if err := os.WriteFile(configPath, []byte("[worktrees]\nsetup_scrip = \"scripts/misspelled.sh\"\n"), 0o644); err != nil {
				t.Fatalf("write invalid source workspace config: %v", err)
			}
		},
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return observerClient, nil
		}),
		Project: serverapi.ProjectCreateRequest{
			DisplayName: "Workflow setup composition",
			ProjectKey:  "SETUP",
		},
		CreateWorkflow: createCoreValidWorkflow,
	})
	if fixture.config.Settings.Worktrees.SetupScript != "" {
		t.Fatalf("server startup config unexpectedly contains source workspace setup script")
	}
	workflowID := fixture.workflowID
	persistedWorkflowID := string(workflowID)
	task, err := fixture.appCore.WorkflowClient().CreateWorkflowTask(fixture.ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID:         fixture.projectID,
		WorkflowID:        &persistedWorkflowID,
		Title:             "Provision task worktree",
		SourceWorkspaceID: fixture.workspaceID,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	if _, err := fixture.appCore.WorkflowClient().StartWorkflowTask(fixture.ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	}); err == nil {
		t.Fatal("StartWorkflowTask accepted invalid source workspace setup configuration")
	}
	if err := os.WriteFile(configPath, []byte(validSetupConfig), 0o644); err != nil {
		t.Fatalf("write corrected source workspace config: %v", err)
	}
	started, err := fixture.appCore.WorkflowClient().StartWorkflowTask(fixture.ctx, serverapi.WorkflowTaskStartRequest{
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

	waitForCoreWorkflowTaskDone(t, fixture.appCore, task.Task.ID)
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

	tests := []struct {
		name  string
		askID string
		want  bool
	}{
		{name: "ordinary ask", askID: "ask-1", want: true},
		{name: "generic approval", askID: "approval-1"},
		{name: "task approval", askID: "approval-2", want: true},
		{name: "missing", askID: "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolver.CanRehydrate(t.Context(), "session-1", workflow.RunID("run-1"), tt.askID)
			if err != nil {
				t.Fatalf("CanRehydrate: %v", err)
			}
			if got != tt.want {
				t.Fatalf("CanRehydrate(%q) = %t, want %t", tt.askID, got, tt.want)
			}
		})
	}
}

func TestComposedWorkflowTaskResumesDetachedManagedWorktreeWithoutMutation(t *testing.T) {
	markerRelativePath := filepath.Join(".kent", "detached-resume-marker")
	invocationCountRelativePath := filepath.Join(".kent", "detached-resume-setup-invocations")
	setup := testsetup.New(t, testsetup.Options{
		MarkerRelativePath:          &markerRelativePath,
		InvocationCountRelativePath: &invocationCountRelativePath,
	})
	releaseInitialResponse := make(chan struct{})
	client := scriptedllm.NewClient(scriptedllm.Script{
		Capabilities: &llm.ProviderCapabilities{ProviderID: "scripted-detached-worktree", SupportsResponsesAPI: true},
		Steps: []scriptedllm.Step{
			{
				BeforeResponse: func(ctx context.Context) error {
					select {
					case <-releaseInitialResponse:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				},
				Response: scriptedllm.FinalAnswer(`{"commentary":"interrupted initial response"}`).Response,
			},
			scriptedllm.FinalAnswer(`{"commentary":"resumed first agent"}`),
			scriptedllm.FinalAnswer(`{"commentary":"second agent"}`),
		},
	})
	fixture := newComposedWorkflowFixture(t, composedWorkflowFixtureOptions{
		PrepareWorkspace: func(t *testing.T, workspaceRoot string) {
			t.Helper()
			setupScript := setup.InstallInSourceWorkspace(t, workspaceRoot)
			configPath := filepath.Join(workspaceRoot, config.ConfigDirName, "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
				t.Fatalf("create source workspace config directory: %v", err)
			}
			if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[worktrees]\nsetup_script = %q\n", filepath.ToSlash(setupScript))), 0o644); err != nil {
				t.Fatalf("write source workspace config: %v", err)
			}
		},
		RuntimeClientFactory: runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return client, nil
		}),
		Project: serverapi.ProjectCreateRequest{
			DisplayName: "Detached managed worktree resume",
			ProjectKey:  "DETACHED",
		},
		CreateWorkflow: func(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID {
			return createCoreWorkflowWithAgents(t, ctx, store, 2)
		},
	})
	workflowID := fixture.workflowID
	persistedWorkflowID := string(workflowID)
	task, err := fixture.appCore.WorkflowClient().CreateWorkflowTask(fixture.ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID:         fixture.projectID,
		WorkflowID:        &persistedWorkflowID,
		Title:             "Resume detached managed worktree",
		SourceWorkspaceID: fixture.workspaceID,
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	started, err := fixture.appCore.WorkflowClient().StartWorkflowTask(fixture.ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget:  &serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeHead},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if started.Applied == nil || started.Applied.RunID == "" {
		t.Fatalf("StartWorkflowTask response = %+v, want applied run", started)
	}

	activeCtx, cancelActive := context.WithTimeout(fixture.ctx, 5*time.Second)
	defer cancelActive()
	if err := client.WaitUntilActive(activeCtx); err != nil {
		t.Fatalf("wait for initial workflow model request: %v", err)
	}
	var initial serverapi.WorkflowTaskDetail
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 20*time.Millisecond, func() bool {
		detail, err := fixture.appCore.WorkflowClient().GetWorkflowTask(fixture.ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID})
		if err != nil {
			t.Fatalf("GetWorkflowTask initial: %v", err)
		}
		if len(detail.Task.CurrentSessionIDs) != 1 {
			return false
		}
		if detail.Task.ExecutionTarget == nil || detail.Task.WorktreePath == nil {
			return false
		}
		initial = detail.Task
		return true
	}, "initial workflow run did not attach its session and managed worktree")
	initialSessionID := initial.CurrentSessionIDs[0]
	worktreeRoot := *initial.WorktreePath
	if setupCount, err := setup.InvocationCount(worktreeRoot); err != nil || setupCount != 1 {
		t.Fatalf("initial managed-worktree setup count = %d, %v, want exactly one", setupCount, err)
	}
	if _, err := os.Stat(setup.MarkerPath(worktreeRoot)); err != nil {
		t.Fatalf("stat initial managed-worktree setup marker: %v", err)
	}

	testsetup.RunGit(t, worktreeRoot, "checkout", "--detach")
	detachedHead := testsetup.RunGit(t, worktreeRoot, "rev-parse", "HEAD")
	gitStatusBeforeResume := testsetup.RunGit(t, worktreeRoot, "status", "--porcelain=v1")
	interrupted, err := fixture.appCore.WorkflowClient().InterruptWorkflowTask(fixture.ctx, serverapi.WorkflowTaskInterruptRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("InterruptWorkflowTask: %v", err)
	}
	if len(interrupted.Runs) != 1 || interrupted.Runs[0].SessionID != initialSessionID {
		t.Fatalf("InterruptWorkflowTask response = %+v, want current session", interrupted)
	}
	initialRun := interrupted.Runs[0]

	resumed, err := fixture.appCore.WorkflowClient().ResumeWorkflowTask(fixture.ctx, serverapi.WorkflowTaskResumeRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if len(resumed.Runs) != 1 ||
		resumed.Runs[0].Generation <= initialRun.Generation ||
		resumed.Runs[0].SessionID != initialRun.SessionID {
		t.Fatalf("ResumeWorkflowTask response = %+v, want same session with higher generation", resumed)
	}
	assertDetachedSession := func(sessionID string, label string) {
		t.Helper()
		parsedSessionID, err := runtimeids.ParseSessionID(sessionID)
		if err != nil {
			t.Fatalf("ParseSessionID(%s %q): %v", label, sessionID, err)
		}
		environment, err := fixture.appCore.SessionViewClient().GetSessionExecutionEnvironment(fixture.ctx, serverapi.SessionExecutionEnvironmentRequest{SessionID: parsedSessionID})
		if err != nil {
			t.Fatalf("GetSessionExecutionEnvironment %s: %v", label, err)
		}
		sessionRoot, available := environment.Environment.Workspace.Value()
		if !available || sessionRoot.Path != worktreeRoot {
			t.Fatalf("%s session workspace = %+v/%v, want detached managed root %q", label, sessionRoot, available, worktreeRoot)
		}
		branchReason, unavailable := environment.Environment.Branch.UnavailableReason()
		if !unavailable || branchReason != serverapi.SessionExecutionBranchUnavailableDetachedHead {
			t.Fatalf("%s session branch = %+v, want detached HEAD", label, environment.Environment.Branch)
		}
	}
	assertDetachedSession(initialSessionID, "resumed first agent")

	waitForCoreWorkflowTaskDone(t, fixture.appCore, task.Task.ID)
	final, err := fixture.appCore.WorkflowClient().GetWorkflowTask(fixture.ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTask final: %v", err)
	}
	if !final.Task.Summary.Done || final.Task.ExecutionTarget == nil ||
		final.Task.WorktreePath == nil ||
		*final.Task.WorktreePath != worktreeRoot {
		t.Fatalf("final task = %+v, want done in the same detached managed worktree", final.Task)
	}
	assertDetachedSession(initialSessionID, "completed resumed first agent")
	if testsetup.RunGit(t, worktreeRoot, "rev-parse", "HEAD") != detachedHead ||
		testsetup.RunGit(t, worktreeRoot, "status", "--porcelain=v1") != gitStatusBeforeResume {
		t.Fatal("detached managed worktree changed while resuming workflow execution")
	}
	if setupCount, err := setup.InvocationCount(worktreeRoot); err != nil || setupCount != 1 {
		t.Fatalf("detached resume setup count = %d, %v, want unchanged one", setupCount, err)
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
	workflowStore, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(configRoleResolver{settings: config.Settings{
		EnabledTools: map[toolspec.ID]bool{toolspec.ToolAskQuestion: true},
		Subagents:    map[string]config.SubagentRole{"coder": {}},
	}}))
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
	if detail.Task.AttentionCount != 1 {
		t.Fatalf("attention count = %d, want 1", detail.Task.AttentionCount)
	}
	attention, err := appCore.WorkflowClient().ListWorkflowTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: string(task.ID)})
	if err != nil {
		t.Fatalf("ListWorkflowTaskAttention: %v", err)
	}
	if len(attention.Items) != 1 || attention.Items[0].Message != "Question from composed session transcript?" {
		t.Fatalf("attention = %+v", attention.Items)
	}
}

func TestComposedAttentionClientUsesOneRootGlobalTaskDetailBroker(t *testing.T) {
	ctx := context.Background()
	appCore := newComposedCoreForAttentionTest(t)
	sessionOne := runtimeids.NewSessionID().String()
	sessionTwo := runtimeids.NewSessionID().String()
	registerCoreRuntime(t, appCore, sessionOne)
	registerCoreRuntime(t, appCore, sessionTwo)

	desktop, err := appCore.AttentionNotificationClient().SubscribeAttentionNotifications(ctx, serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	sessionOneSub, err := appCore.AttentionNotificationClient().SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionOne})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications session-1: %v", err)
	}
	sessionTwoSub, err := appCore.AttentionNotificationClient().SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionTwo})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications session-2: %v", err)
	}

	projectCorePrompt(t, appCore, sessionOne, coreTaskBatchAskRequest("ask-a", "project-a", "task-a", sessionOne))
	projectCorePrompt(t, appCore, sessionTwo, coreTaskBatchAskRequest("ask-b", "project-b", "task-b", sessionTwo))

	firstDesktop := nextCoreAttentionEvent(t, desktop)
	secondDesktop := nextCoreAttentionEvent(t, desktop)
	if firstDesktop.Pending.Target.ProjectID != "project-a" || secondDesktop.Pending.Target.ProjectID != "project-b" {
		t.Fatalf("desktop project delivery = %+v then %+v", firstDesktop, secondDesktop)
	}
	if event := nextCoreAttentionEvent(t, sessionOneSub); event.Pending.Target.TaskID != "task-a" {
		t.Fatalf("session-1 task-detail event = %+v", event)
	}
	if event := nextCoreAttentionEvent(t, sessionTwoSub); event.Pending.Target.TaskID != "task-b" {
		t.Fatalf("session-2 task-detail event = %+v", event)
	}
}

func TestComposedAttentionClientKeepsGenericPromptsOffDesktopRootStream(t *testing.T) {
	ctx := context.Background()
	appCore := newComposedCoreForAttentionTest(t)
	sessionID := runtimeids.NewSessionID().String()
	registerCoreRuntime(t, appCore, sessionID)

	desktop, err := appCore.AttentionNotificationClient().SubscribeAttentionNotifications(ctx, serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	sessionSub, err := appCore.AttentionNotificationClient().SubscribeSessionAttentionNotifications(ctx, serverapi.AttentionSessionNotificationSubscribeRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("SubscribeSessionAttentionNotifications: %v", err)
	}

	resource, scope := projectCorePrompt(t, appCore, sessionID, askquestion.AskQuestionRequest{
		ID:         "ask-generic",
		Question:   "Generic prompt?",
		Origin:     askquestion.AskQuestionOriginModelTool,
		RunID:      "11111111-1111-4111-8111-111111111111",
		StepID:     "22222222-2222-4222-8222-222222222222",
		ToolCallID: "tool-ask-generic",
	})
	pending := nextCoreAttentionEvent(t, sessionSub)
	if pending.Pending.Target.Kind != clientui.AttentionNotificationTargetSessionPrompt {
		t.Fatalf("generic prompt target = %+v", pending.Pending.Target)
	}
	if event, err := desktop.Next(shortCoreAttentionContext(t)); err == nil {
		t.Fatalf("desktop received generic pending event: %+v", event)
	}

	appCore.bundles.Runtime.runtimeRegistry.PromptResolved(resource, scope, "ask-generic")
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
	sessionID := runtimeids.NewSessionID().String()
	registerCoreRuntime(t, appCore, sessionID)

	desktop, err := appCore.AttentionNotificationClient().SubscribeAttentionNotifications(ctx, serverapi.AttentionNotificationSubscribeRequest{})
	if err != nil {
		t.Fatalf("SubscribeAttentionNotifications: %v", err)
	}
	req := coreTaskBatchAskRequest("ask-a", "project-a", "task-a", sessionID)
	projectCorePrompt(t, appCore, sessionID, req)
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
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", sessionID, err)
	}
	ref, err := runtimeids.NewSessionResourceRef(id, 1)
	if err != nil {
		t.Fatalf("NewSessionResourceRef(%q): %v", sessionID, err)
	}
	resource := sessionruntime.AgentResourceDescriptor{Ref: ref, State: sessionruntime.AgentResourceReady}
	if err := appCore.bundles.Runtime.runtimeRegistry.ResourceReady(
		context.Background(),
		resource,
		&runtime.Engine{},
		func() (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil },
	); err != nil {
		t.Fatalf("ResourceReady(%q): %v", sessionID, err)
	}
	t.Cleanup(func() {
		if err := appCore.bundles.Runtime.runtimeRegistry.ResourceDraining(context.Background(), resource); err != nil {
			t.Errorf("ResourceDraining(%q): %v", sessionID, err)
		}
	})
}

func projectCorePrompt(t *testing.T, appCore *Core, sessionID string, request askquestion.AskQuestionRequest) (runtimeids.SessionResourceRef, runtimeids.ExecutionScopeID) {
	t.Helper()
	id, err := runtimeids.ParseSessionID(sessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	resource, err := runtimeids.NewSessionResourceRef(id, 1)
	if err != nil {
		t.Fatalf("NewSessionResourceRef: %v", err)
	}
	scope := runtimeids.NewExecutionScopeID()
	appCore.bundles.Runtime.runtimeRegistry.PromptPending(resource, scope, request, time.Now().UTC())
	return resource, scope
}

func coreTaskBatchAskRequest(askID string, projectID string, taskID string, sessionID string) askquestion.AskQuestionRequest {
	const (
		runID  = "11111111-1111-4111-8111-111111111111"
		stepID = "22222222-2222-4222-8222-222222222222"
	)
	return askquestion.AskQuestionRequest{
		ID:         askID,
		Question:   "Task question?",
		Origin:     askquestion.AskQuestionOriginModelTool,
		RunID:      runID,
		StepID:     stepID,
		ToolCallID: "tool-" + askID,
		QuestionBatch: &askquestion.AskQuestionBatchMetadata{
			Origin:              askquestion.AskQuestionOriginModelTool,
			RunID:               runID,
			StepID:              stepID,
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

type composedWorkflowFixtureOptions struct {
	PrepareWorkspace     func(t *testing.T, workspaceRoot string)
	RuntimeClientFactory runtimewire.RuntimeClientFactory
	Project              serverapi.ProjectCreateRequest
	CreateWorkflow       func(t *testing.T, ctx context.Context, store *workflowstore.Store) workflow.WorkflowID
}

type composedWorkflowFixture struct {
	ctx         context.Context
	config      config.App
	appCore     *Core
	projectID   string
	workspaceID string
	workflowID  workflow.WorkflowID
}

func newComposedWorkflowFixture(t *testing.T, options composedWorkflowFixtureOptions) composedWorkflowFixture {
	t.Helper()
	if options.RuntimeClientFactory == nil {
		t.Fatal("composed workflow fixture runtime client factory is required")
	}
	if options.CreateWorkflow == nil {
		t.Fatal("composed workflow fixture workflow shape is required")
	}
	ctx := context.Background()
	t.Setenv("HOME", t.TempDir())
	serverWorkspace := t.TempDir()
	workspace := t.TempDir()
	testsetup.InitializeGitRepository(t, serverWorkspace)
	testsetup.InitializeGitRepository(t, workspace)
	if options.PrepareWorkspace != nil {
		options.PrepareWorkspace(t, workspace)
	}
	resolved, err := serverbootstrap.ResolveConfig(serverbootstrap.Request{WorkspaceRoot: serverWorkspace})
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
	appCore, err := NewWithContextOptions(ctx, resolved.Config, authSupport, runtimeSupport, Options{
		RuntimeClientFactory: options.RuntimeClientFactory,
	})
	if err != nil {
		t.Fatalf("NewWithContextOptions: %v", err)
	}
	t.Cleanup(func() { _ = appCore.Close() })
	projectRequest := options.Project
	projectRequest.WorkspaceRoot = workspace
	project, err := appCore.ProjectViewClient().CreateProject(ctx, projectRequest)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	workflowStore, err := workflowstore.New(appCore.bundles.Persistence.metadataStore)
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	workflowID := options.CreateWorkflow(t, ctx, workflowStore)
	if _, err := workflowStore.LinkWorkflow(ctx, project.Binding.ProjectID, workflowID, true); err != nil {
		t.Fatalf("LinkWorkflow: %v", err)
	}
	return composedWorkflowFixture{
		ctx:         ctx,
		config:      resolved.Config,
		appCore:     appCore,
		projectID:   project.Binding.ProjectID,
		workspaceID: project.Binding.WorkspaceID,
		workflowID:  workflowID,
	}
}

func waitForCoreWorkflowTaskDone(t *testing.T, appCore *Core, taskID string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 20*time.Millisecond, func() bool {
		detail, err := appCore.WorkflowClient().GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{TaskID: taskID})
		if err != nil {
			t.Fatalf("GetWorkflowTask: %v", err)
		}
		return detail.Task.Summary.Done
	}, "workflow task %q did not complete", taskID)
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
	return createCoreWorkflowWithAgents(t, ctx, store, 1)
}

func createCoreWorkflowWithAgents(t *testing.T, ctx context.Context, store *workflowstore.Store, agentCount int) workflow.WorkflowID {
	t.Helper()
	if agentCount < 1 {
		t.Fatalf("agent count = %d, want at least one", agentCount)
	}
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
	agentIDs := make([]workflow.NodeID, agentCount)
	for index := range agentIDs {
		agentKey := fmt.Sprintf("agent_%d", index+1)
		agentDisplayName := fmt.Sprintf("Agent %d", index+1)
		agentIDs[index] = workflow.NodeID(fmt.Sprintf("node-agent-%d-%s", index+1, created.ID))
		if agentCount == 1 {
			agentKey = "agent"
			agentDisplayName = "Agent"
			agentIDs[index] = workflow.NodeID("node-agent-" + string(created.ID))
		}
		if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
			ID:           agentIDs[index],
			WorkflowID:   created.ID,
			Key:          workflow.ModelKey(agentKey),
			Kind:         workflow.NodeKindAgent,
			DisplayName:  agentDisplayName,
			SubagentRole: workflow.DefaultAgentRole,
		}); err != nil {
			t.Fatalf("AddNode agent %d: %v", index+1, err)
		}
	}
	startGroupID := workflow.TransitionGroupID("group-start-" + string(created.ID))
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: workflow.NodeIDOf(start), TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentIDs[0], ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	for index, agentID := range agentIDs {
		targetNodeID := workflow.NodeIDOf(done)
		transitionID := "done"
		displayName := "Done"
		if index+1 < len(agentIDs) {
			targetNodeID = agentIDs[index+1]
			transitionID = fmt.Sprintf("advance_%d", index+1)
			displayName = fmt.Sprintf("Advance %d", index+1)
		}
		groupID := workflow.TransitionGroupID(fmt.Sprintf("group-%s-%s", transitionID, created.ID))
		if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: groupID, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: workflow.TransitionID(transitionID), DisplayName: displayName}); err != nil {
			t.Fatalf("AddTransitionGroup %s: %v", transitionID, err)
		}
		promptTemplate := ""
		if index+1 < len(agentIDs) {
			promptTemplate = "Do work."
		}
		if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(fmt.Sprintf("edge-%s-%s", transitionID, created.ID)), WorkflowID: created.ID, TransitionGroupID: groupID, Key: workflow.ModelKey(transitionID), TargetNodeID: targetNodeID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: promptTemplate}); err != nil {
			t.Fatalf("AddEdge %s: %v", transitionID, err)
		}
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
