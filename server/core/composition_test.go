package core

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"core/server/auth"
	serverbootstrap "core/server/bootstrap"
	"core/server/llm"
	"core/server/registry"
	"core/server/session"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
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
	if appCore.bundles.Runtime == nil || appCore.bundles.Runtime.background == nil || appCore.bundles.Runtime.backgroundRouter == nil || appCore.bundles.Runtime.runtimeRegistry == nil || appCore.bundles.Runtime.runtimeControls == nil || appCore.bundles.Runtime.sessionRuntime == nil || appCore.bundles.Runtime.sessionActivity == nil {
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

func TestRuntimePendingAskResolverUsesPendingPromptSource(t *testing.T) {
	resolver := runtimePendingAskResolver{prompts: fakePendingPromptSource{items: map[string][]registry.PendingPromptSnapshot{
		"session-1": {
			{Request: askquestion.AskQuestionRequest{ID: "ask-1", Question: "Need input?"}, CreatedAt: time.Unix(1, 0)},
			{Request: askquestion.AskQuestionRequest{ID: "approval-1", Question: "Approve?", Approval: true}, CreatedAt: time.Unix(2, 0)},
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
		t.Fatal("approval prompt must not satisfy workflow ask rehydration")
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
	sessionStore, err := session.Create(sessionsDir, filepath.Base(sessionsDir), resolved.Config.WorkspaceRoot, metadataStore.AuthoritativeSessionStoreOptions()...)
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

type fakePendingPromptSource struct {
	items map[string][]registry.PendingPromptSnapshot
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
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{ID: agentID, WorkflowID: created.ID, Key: "agent", Kind: workflow.NodeKindAgent, DisplayName: "Agent", SubagentRole: "coder"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	startGroupID := workflow.TransitionGroupID("group-start-" + string(created.ID))
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: startGroupID, WorkflowID: created.ID, SourceNodeID: start.ID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-start-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentID, ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	doneGroupID := workflow.TransitionGroupID("group-done-" + string(created.ID))
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{ID: doneGroupID, WorkflowID: created.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID("edge-done-" + string(created.ID)), WorkflowID: created.ID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: done.ID, ContextMode: workflow.ContextModeNewSession}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}
	return created.ID
}

func coreWorkflowNodeByKind(t *testing.T, def workflow.Definition, kind workflow.NodeKind) workflow.Node {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind == kind {
			return node
		}
	}
	t.Fatalf("missing workflow node kind %q in %+v", kind, def.Nodes)
	return workflow.Node{}
}
